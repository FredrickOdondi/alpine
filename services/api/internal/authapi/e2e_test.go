package authapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/alpine-dev/alpine/packages/shared/auth"
	"github.com/alpine-dev/alpine/packages/shared/auth/authtest"
	"github.com/alpine-dev/alpine/packages/shared/db/dbtest"
	"github.com/alpine-dev/alpine/packages/shared/repo/pgrepo"
	"github.com/alpine-dev/alpine/services/api/internal/authapi"
)

const appURL = "http://app.invalid"

type env struct {
	t      *testing.T
	srv    *httptest.Server
	svc    *auth.Service
	google *authtest.FakeGoogle
	github *authtest.FakeGitHub
	mailer *auth.MemoryMailer
	mr     *miniredis.Miniredis
}

func setup(t *testing.T) *env {
	t.Helper()
	d, _ := dbtest.Open(t) // skips without ALPINE_TEST_ADMIN_URL / ALPINE_TEST_DATABASE_URL
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	e := &env{t: t, google: authtest.NewFakeGoogle(t), github: authtest.NewFakeGitHub(t), mailer: &auth.MemoryMailer{}, mr: mr}
	var h http.Handler
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(e.srv.Close)
	cfg := auth.Config{
		PublicAPIURL: e.srv.URL, AppURL: appURL,
		Google:        auth.GoogleConfig{ClientID: e.google.ClientID, ClientSecret: e.google.Secret, Issuer: e.google.Server.URL},
		GitHub:        auth.GitHubConfig{ClientID: e.github.ClientID, ClientSecret: e.github.Secret, BaseURL: e.github.Server.URL, APIURL: e.github.APIURL()},
		SessionSecret: bytes.Repeat([]byte("s"), 32), APIKeyPepper: bytes.Repeat([]byte("p"), 32),
		AllowSignup: true, MagicLinkEnable: true,
	}
	e.svc = auth.New(cfg, pgrepo.New(d), rdb, e.mailer, nil)
	h = authapi.Router(authapi.New(e.svc), e.svc)
	return e
}

// browser is a cookie-holding client that stops at the SPA redirect.
type browser struct {
	e    *env
	c    *http.Client
	csrf string
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{e: e, c: &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if req.URL.Host == "app.invalid" {
			return http.ErrUseLastResponse
		}
		return nil
	}}}
}

func (b *browser) session() string {
	u, _ := url.Parse(b.e.srv.URL)
	for _, c := range b.c.Jar.Cookies(u) {
		if c.Name == "alpine_session" {
			return c.Value
		}
	}
	return ""
}

// login runs the provider flow and returns the final SPA redirect.
func (b *browser) login(provider, redirect string) *url.URL {
	b.e.t.Helper()
	resp, err := b.c.Get(b.e.srv.URL + "/v1/auth/" + provider + "/login?redirect_to=" + url.QueryEscape(redirect))
	require.NoError(b.e.t, err)
	resp.Body.Close()
	require.Equal(b.e.t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(b.e.t, err)
	b.refreshCSRF()
	return loc
}

func (b *browser) refreshCSRF() {
	b.csrf = ""
	var tok struct{ CsrfToken string }
	if code := b.do("GET", "/v1/auth/csrf", nil, &tok); code == 200 {
		b.csrf = tok.CsrfToken
	}
}

func (b *browser) do(method, path string, body, out any) int {
	return b.doH(method, path, body, out, nil)
}

func (b *browser) doH(method, path string, body, out any, hdr map[string]string) int {
	b.e.t.Helper()
	var rd io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rd = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(method, b.e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if method != "GET" {
		req.Header.Set("Origin", appURL)
		if b.csrf != "" {
			req.Header.Set(auth.CSRFHeader, b.csrf)
		}
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := b.c.Do(req)
	require.NoError(b.e.t, err)
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		require.NoError(b.e.t, json.NewDecoder(resp.Body).Decode(out))
	}
	return resp.StatusCode
}

type orgT struct{ ID, Slug, Name string }
type meT struct {
	User        struct{ ID, Email string }
	ActiveOrg   *orgT
	Role        *string
	CsrfToken   string
	Memberships []struct {
		Org  orgT
		Role string
	}
}

func tokenFromLink(t *testing.T, link string) string {
	t.Helper()
	_, frag, ok := strings.Cut(link, "#token=")
	require.True(t, ok, "token travels in the URL fragment")
	return frag
}

func TestE2E_FullFlow(t *testing.T) {
	e := setup(t)

	// --- Google login (PKCE + state + nonce) creates Alice and a rotated session ----------------
	alice := e.browser()
	e.google.Next = authtest.GoogleUser{Sub: "g-alice", Email: "alice@acme.dev", EmailVerified: true, Name: "Alice"}
	loc := alice.login("google", "/board")
	require.Equal(t, appURL+"/board", loc.String())
	first := alice.session()
	require.NotEmpty(t, first)

	var me meT
	require.Equal(t, 200, alice.do("GET", "/v1/me", nil, &me))
	require.Equal(t, "alice@acme.dev", me.User.Email)
	require.Nil(t, me.ActiveOrg)
	require.Equal(t, alice.csrf, me.CsrfToken)

	// Logging in again rotates the session id and kills the old one.
	e.google.Next = authtest.GoogleUser{Sub: "g-alice", Email: "alice@acme.dev", EmailVerified: true, Name: "Alice"}
	alice.login("google", "/")
	require.NotEqual(t, first, alice.session())
	stale := e.browser()
	u, _ := url.Parse(e.srv.URL)
	stale.c.Jar.SetCookies(u, []*http.Cookie{{Name: "alpine_session", Value: first}})
	require.Equal(t, 401, stale.do("GET", "/v1/me", nil, nil))

	// --- CSRF: session-authenticated writes need the header and an allowed Origin ---------------
	tok := alice.csrf
	alice.csrf = ""
	require.Equal(t, 403, alice.do("POST", "/v1/orgs", map[string]any{"name": "Acme"}, nil))
	alice.csrf = tok
	require.Equal(t, 403, alice.doH("POST", "/v1/orgs", map[string]any{"name": "Acme"}, nil, map[string]string{"Origin": "https://evil.example"}))
	var org orgT
	require.Equal(t, 201, alice.do("POST", "/v1/orgs", map[string]any{"name": "Acme Robotics"}, &org))
	require.Equal(t, "acme-robotics", org.Slug)
	require.Equal(t, 200, alice.do("GET", "/v1/me", nil, &me))
	require.NotNil(t, me.ActiveOrg)
	require.Equal(t, org.ID, me.ActiveOrg.ID)
	require.Equal(t, "owner", *me.Role)

	// --- Invitation by email; Bob signs in with GitHub and accepts --------------------------------
	var inv struct{ ID, Status, Role string }
	require.Equal(t, 201, alice.do("POST", "/v1/orgs/"+org.ID+"/invitations", map[string]any{"email": "Bob@Acme.dev", "role": "member"}, &inv))
	require.Equal(t, "pending", inv.Status)
	msg, ok := e.mailer.Last()
	require.True(t, ok)
	require.Equal(t, "bob@acme.dev", msg.To)
	invToken := tokenFromLink(t, msg.Link)
	require.True(t, strings.HasPrefix(msg.Link, appURL+"/invitations/accept#token="))

	// Carol (wrong email) cannot use Bob's invitation.
	carol := e.browser()
	e.google.Next = authtest.GoogleUser{Sub: "g-carol", Email: "carol@other.dev", EmailVerified: true}
	carol.login("google", "/")
	require.Equal(t, 403, carol.do("POST", "/v1/invitations/accept", map[string]any{"token": invToken}, nil))

	bob := e.browser()
	e.github.Next = authtest.GitHubUser{ID: 4242, Login: "bobdev", Name: "Bob", Emails: []authtest.GitHubEmail{{Email: "bob@acme.dev", Primary: true, Verified: true}}}
	bob.login("github", "/")
	var om struct {
		Org  orgT
		Role string
	}
	require.Equal(t, 200, bob.do("POST", "/v1/invitations/accept", map[string]any{"token": invToken}, &om))
	require.Equal(t, "member", om.Role)
	require.Equal(t, org.ID, om.Org.ID)
	// Tokens are single use.
	require.NotEqual(t, 200, bob.do("POST", "/v1/invitations/accept", map[string]any{"token": invToken}, nil))
	// Garbage token.
	require.Equal(t, 410, bob.do("POST", "/v1/invitations/accept", map[string]any{"token": "nope"}, nil))

	// --- RBAC: members can read members but not change roles or invite ----------------------------
	var members struct{ Items []struct{ Role string } }
	require.Equal(t, 200, bob.do("GET", "/v1/orgs/"+org.ID+"/members", nil, &members))
	require.Len(t, members.Items, 2)
	require.Equal(t, 403, bob.do("PATCH", "/v1/orgs/"+org.ID+"/members/"+me.User.ID, map[string]any{"role": "viewer"}, nil))
	require.Equal(t, 403, bob.do("POST", "/v1/orgs/"+org.ID+"/invitations", map[string]any{"email": "x@acme.dev", "role": "member"}, nil))
	// The last owner cannot demote themself.
	require.Equal(t, 409, alice.do("PATCH", "/v1/orgs/"+org.ID+"/members/"+me.User.ID, map[string]any{"role": "admin"}, nil))
	// Carol is not a member: Acme is invisible to her.
	require.Equal(t, 404, carol.do("GET", "/v1/orgs/"+org.ID, nil, nil))
	require.Equal(t, 403, carol.do("GET", "/v1/orgs/"+org.ID+"/members", nil, nil))

	// --- GitHub account linking by verified email: Alice signs in with GitHub -> same user --------
	alice2 := e.browser()
	e.github.Next = authtest.GitHubUser{ID: 777, Login: "alicegh", Emails: []authtest.GitHubEmail{{Email: "alice@acme.dev", Primary: true, Verified: true}}}
	alice2.login("github", "/")
	var me2 meT
	require.Equal(t, 200, alice2.do("GET", "/v1/me", nil, &me2))
	require.Equal(t, me.User.ID, me2.User.ID)
	var ids struct{ Items []struct{ Provider string } }
	require.Equal(t, 200, alice2.do("GET", "/v1/me/identities", nil, &ids))
	require.Len(t, ids.Items, 2)

	// --- API keys: scoped, hashed, revocable; cannot cross orgs ------------------------------------
	var created struct {
		ApiKey struct{ ID, Prefix string }
		Token  string
	}
	require.Equal(t, 201, alice.do("POST", "/v1/orgs/"+org.ID+"/api-keys", map[string]any{"name": "ci", "scopes": []string{"members:read"}, "role": "viewer"}, &created))
	require.True(t, strings.HasPrefix(created.Token, "alp_"+created.ApiKey.Prefix+"_"))
	ci := e.browser()
	bearer := map[string]string{"Authorization": "Bearer " + created.Token}
	require.Equal(t, 200, ci.doH("GET", "/v1/orgs/"+org.ID+"/members", nil, nil, bearer))
	require.Equal(t, 403, ci.doH("GET", "/v1/orgs/"+org.ID+"/audit-log", nil, nil, bearer), "scope not granted")
	require.Equal(t, 403, ci.doH("POST", "/v1/orgs/"+org.ID+"/api-keys", map[string]any{"name": "x", "scopes": []string{"members:read"}}, nil, bearer), "keys cannot mint keys")
	var carolOrg orgT
	require.Equal(t, 201, carol.do("POST", "/v1/orgs", map[string]any{"name": "Other Co"}, &carolOrg))
	require.Equal(t, 403, ci.doH("GET", "/v1/orgs/"+carolOrg.ID+"/members", nil, nil, bearer), "key is bound to its org")
	require.Equal(t, 401, ci.doH("GET", "/v1/orgs/"+org.ID+"/members", nil, nil, map[string]string{"Authorization": "Bearer " + created.Token[:len(created.Token)-1] + "x"}))
	require.Equal(t, 204, alice.do("DELETE", "/v1/orgs/"+org.ID+"/api-keys/"+created.ApiKey.ID, nil, nil))
	require.Equal(t, 401, ci.doH("GET", "/v1/orgs/"+org.ID+"/members", nil, nil, bearer))

	// --- Audit log captured every org event ------------------------------------------------------
	var page struct{ Items []struct{ Action string } }
	require.Equal(t, 200, alice.do("GET", "/v1/orgs/"+org.ID+"/audit-log?limit=100", nil, &page))
	var actions []string
	for _, it := range page.Items {
		actions = append(actions, it.Action)
	}
	for _, want := range []string{"org.created", "invitation.created", "invitation.accepted", "api_key.created", "api_key.revoked"} {
		require.Contains(t, actions, want)
	}
	require.Equal(t, 403, bob.do("GET", "/v1/orgs/"+org.ID+"/audit-log", nil, nil))

	// --- Member removal + logout everywhere --------------------------------------------------------
	var bobMe meT
	require.Equal(t, 200, bob.do("GET", "/v1/me", nil, &bobMe))
	require.Equal(t, 204, alice.do("DELETE", "/v1/orgs/"+org.ID+"/members/"+bobMe.User.ID, nil, nil))
	require.Equal(t, 403, bob.do("GET", "/v1/orgs/"+org.ID+"/members", nil, nil))

	var res struct{ RevokedSessions int }
	require.Equal(t, 200, alice.do("POST", "/v1/auth/logout-all", nil, &res))
	require.Equal(t, 2, res.RevokedSessions)
	require.Equal(t, 401, alice.do("GET", "/v1/me", nil, nil))
	require.Equal(t, 401, alice2.do("GET", "/v1/me", nil, nil))

	// --- Single logout ----------------------------------------------------------------------------
	require.Equal(t, 204, carol.do("POST", "/v1/auth/logout", nil, nil))
	require.Equal(t, 401, carol.do("GET", "/v1/me", nil, nil))
}

func TestE2E_OAuthStateAndProviders(t *testing.T) {
	e := setup(t)
	b := e.browser()
	var prov struct{ Google, Github, MagicLink, DevLogin bool }
	require.Equal(t, 200, b.do("GET", "/v1/auth/providers", nil, &prov))
	require.True(t, prov.Google && prov.Github && prov.MagicLink)
	require.False(t, prov.DevLogin)

	// Callback without the state cookie / with a forged state never logs in.
	resp, err := b.c.Get(e.srv.URL + "/v1/auth/google/callback?code=x&state=forged")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), appURL+"/login?error=")
	require.Empty(t, b.session())

	// Provider error is surfaced as a login error, not a 500.
	resp, err = b.c.Get(e.srv.URL + "/v1/auth/github/callback?error=access_denied&state=x")
	require.NoError(t, err)
	resp.Body.Close()
	require.Contains(t, resp.Header.Get("Location"), "/login?error=")

	// Open redirects are neutralised.
	e.google.Next = authtest.GoogleUser{Sub: "g-1", Email: "dana@acme.dev", EmailVerified: true}
	loc := b.login("google", "//evil.example/x")
	require.Equal(t, appURL+"/", loc.String())

	// Unverified Google email is refused.
	b2 := e.browser()
	e.google.Next = authtest.GoogleUser{Sub: "g-2", Email: "eve@acme.dev", EmailVerified: false}
	loc = b2.login("google", "/")
	require.Contains(t, loc.String(), "/login?error=")
	require.Empty(t, b2.session())
}

func TestE2E_MagicLinkAndRateLimit(t *testing.T) {
	e := setup(t)
	// Existing user via Google, then signs in by magic link from another browser.
	a := e.browser()
	e.google.Next = authtest.GoogleUser{Sub: "g-m", Email: "mia@acme.dev", EmailVerified: true}
	a.login("google", "/")

	b := e.browser()
	require.Equal(t, 202, b.do("POST", "/v1/auth/magic-link", map[string]any{"email": "mia@acme.dev", "redirectTo": "/runs"}, nil))
	msg, ok := e.mailer.Last()
	require.True(t, ok)
	tok := tokenFromLink(t, msg.Link)
	// Unknown emails get the same 202 and no mail (no enumeration, sign-up off by default).
	n := len(e.mailer.Sent)
	require.Equal(t, 202, b.do("POST", "/v1/auth/magic-link", map[string]any{"email": "nobody@acme.dev"}, nil))
	require.Len(t, e.mailer.Sent, n)

	var lr struct{ RedirectTo string }
	require.Equal(t, 200, b.do("POST", "/v1/auth/magic-link/verify", map[string]any{"token": tok}, &lr))
	require.Equal(t, "/runs", lr.RedirectTo)
	require.NotEmpty(t, b.session())
	require.Equal(t, 200, b.do("GET", "/v1/me", nil, nil))
	// Single use.
	c := e.browser()
	require.NotEqual(t, 200, c.do("POST", "/v1/auth/magic-link/verify", map[string]any{"token": tok}, nil))

	// Per-IP rate limit on /v1/auth/*.
	limited := false
	for i := 0; i < 80 && !limited; i++ {
		limited = c.do("GET", "/v1/auth/providers", nil, nil) == http.StatusTooManyRequests
	}
	require.True(t, limited)
	e.mr.FastForward(2 * time.Minute)
	require.Equal(t, 200, c.do("GET", "/v1/auth/providers", nil, nil))
}
