// Package authtest provides httptest fakes of Google's OIDC endpoints and GitHub's OAuth/REST
// endpoints so login flows are tested end to end with no real network.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// GoogleUser is what the fake Google asserts for the next login.
type GoogleUser struct {
	Sub           string
	Email         string
	EmailVerified bool
	Name          string
	HD            string
}

// FakeGoogle is an OIDC provider: discovery, authorize (records params), token (verifies PKCE and
// returns an RS256-signed ID token carrying the nonce from the authorize request), JWKS.
type FakeGoogle struct {
	Server   *httptest.Server
	ClientID string
	Secret   string

	key *rsa.PrivateKey
	mu  sync.Mutex
	// Next is the identity returned for the next code.
	Next GoogleUser
	// codes issued by Authorize: code -> pending login
	codes map[string]pendingCode
	// Overrides for negative tests.
	WrongNonce    bool
	WrongAudience bool
	Expired       bool
	SignWithOther bool
}

type pendingCode struct {
	challenge, nonce, redirect string
	user                       GoogleUser
}

// NewFakeGoogle starts the fake.
func NewFakeGoogle(t testing.TB) *FakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &FakeGoogle{ClientID: "google-client-id.apps.googleusercontent.com", Secret: "google-test-secret", key: key, codes: map[string]pendingCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer": f.Server.URL, "authorization_endpoint": f.Server.URL + "/o/oauth2/v2/auth",
			"token_endpoint": f.Server.URL + "/token", "jwks_uri": f.Server.URL + "/certs",
			"userinfo_endpoint": f.Server.URL + "/userinfo", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	mux.HandleFunc("/certs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/o/oauth2/v2/auth", func(w http.ResponseWriter, r *http.Request) {
		// Simulates the user consenting: redirect straight back with a code.
		q := r.URL.Query()
		code := randomString()
		f.mu.Lock()
		f.codes[code] = pendingCode{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri"), user: f.Next}
		f.mu.Unlock()
		u, _ := url.Parse(q.Get("redirect_uri"))
		rq := u.Query()
		rq.Set("code", code)
		rq.Set("state", q.Get("state"))
		u.RawQuery = rq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, ok := r.BasicAuth()
		if !ok {
			id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		if id != f.ClientID || secret != f.Secret {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		pc, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		f.mu.Unlock()
		if !ok || pc.redirect != r.PostForm.Get("redirect_uri") || s256(r.PostForm.Get("code_verifier")) != pc.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"access_token": "ya29.fake", "token_type": "Bearer", "expires_in": 3600, "id_token": f.idToken(t, pc)})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

func (f *FakeGoogle) idToken(t testing.TB, pc pendingCode) string {
	key := f.key
	if f.SignWithOther {
		key, _ = rsa.GenerateKey(rand.Reader, 2048)
	}
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	aud := f.ClientID
	if f.WrongAudience {
		aud = "someone-else"
	}
	nonce := pc.nonce
	if f.WrongNonce {
		nonce = "not-the-nonce"
	}
	exp := now.Add(time.Hour)
	if f.Expired {
		exp = now.Add(-time.Hour)
	}
	claims := map[string]any{
		"iss": f.Server.URL, "aud": aud, "sub": pc.user.Sub, "iat": now.Unix(), "exp": exp.Unix(), "nonce": nonce,
		"email": pc.user.Email, "email_verified": pc.user.EmailVerified, "name": pc.user.Name,
	}
	if pc.user.HD != "" {
		claims["hd"] = pc.user.HD
	}
	raw, err := jwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// GitHubUser is what the fake GitHub returns.
type GitHubUser struct {
	ID     int64
	Login  string
	Name   string
	Emails []GitHubEmail
}

// GitHubEmail is one /user/emails entry.
type GitHubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// FakeGitHub serves /login/oauth/authorize, /login/oauth/access_token, /user and /user/emails.
type FakeGitHub struct {
	Server   *httptest.Server
	ClientID string
	Secret   string
	Next     GitHubUser

	mu     sync.Mutex
	codes  map[string]pendingGH
	tokens map[string]GitHubUser
}

type pendingGH struct {
	challenge string
	user      GitHubUser
}

// NewFakeGitHub starts the fake.
func NewFakeGitHub(t testing.TB) *FakeGitHub {
	t.Helper()
	f := &FakeGitHub{ClientID: "Iv1.fakeclient", Secret: "github-test-secret", codes: map[string]pendingGH{}, tokens: map[string]GitHubUser{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := randomString()
		f.mu.Lock()
		f.codes[code] = pendingGH{challenge: q.Get("code_challenge"), user: f.Next}
		f.mu.Unlock()
		u, _ := url.Parse(q.Get("redirect_uri"))
		rq := u.Query()
		rq.Set("code", code)
		rq.Set("state", q.Get("state"))
		u.RawQuery = rq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, ok := r.BasicAuth()
		if !ok {
			id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		f.mu.Lock()
		pc, found := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		f.mu.Unlock()
		if id != f.ClientID || secret != f.Secret || !found || s256(r.PostForm.Get("code_verifier")) != pc.challenge {
			writeJSON(w, map[string]any{"error": "bad_verification_code"})
			return
		}
		tok := "gho_" + randomString()
		f.mu.Lock()
		f.tokens[tok] = pc.user
		f.mu.Unlock()
		writeJSON(w, map[string]any{"access_token": tok, "token_type": "bearer", "scope": "read:user,user:email"})
	})
	user := func(r *http.Request) (GitHubUser, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		u, ok := f.tokens[r.Header.Get("Authorization")[len("Bearer "):]]
		return u, ok
	}
	mux.HandleFunc("/api/user", func(w http.ResponseWriter, r *http.Request) {
		u, ok := user(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"id": u.ID, "login": u.Login, "name": u.Name, "avatar_url": "https://avatars.example/u/" + strconv.FormatInt(u.ID, 10),
			"email": "unverified-public@example.com"})
	})
	mux.HandleFunc("/api/user/emails", func(w http.ResponseWriter, r *http.Request) {
		u, ok := user(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, u.Emails)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// APIURL is the fake REST base URL.
func (f *FakeGitHub) APIURL() string { return f.Server.URL + "/api" }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func s256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func randomString() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
