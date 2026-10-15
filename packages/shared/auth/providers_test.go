package auth_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/alpine-dev/alpine/packages/shared/auth"
	"github.com/alpine-dev/alpine/packages/shared/auth/authtest"
)

// followAuthorize hits the provider's authorize URL (the fake redirects straight back) and
// returns the callback query.
func followAuthorize(t *testing.T, authURL string) url.Values {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(authURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	return loc.Query()
}

func newGoogle(f *authtest.FakeGoogle, hd ...string) *auth.Google {
	return auth.NewGoogle(auth.GoogleConfig{
		ClientID: f.ClientID, ClientSecret: f.Secret, RedirectURL: "http://localhost:8080/v1/auth/google/callback",
		Issuer: f.Server.URL, HostedDomains: hd,
	})
}

func googleLogin(t *testing.T, g *auth.Google) (auth.LoginProvider, string, string, string, url.Values) {
	ctx := context.Background()
	verifier, nonce, state := oauth2.GenerateVerifier(), auth.RandomToken(16), auth.RandomToken(16)
	u, err := g.AuthCodeURL(ctx, state, nonce, verifier)
	require.NoError(t, err)
	pu, _ := url.Parse(u)
	require.Equal(t, "S256", pu.Query().Get("code_challenge_method"))
	require.Equal(t, nonce, pu.Query().Get("nonce"))
	require.Contains(t, pu.Query().Get("scope"), "openid")
	q := followAuthorize(t, u)
	require.Equal(t, state, q.Get("state"))
	return g, verifier, nonce, state, q
}

func TestGoogleOIDC(t *testing.T) {
	ctx := context.Background()
	t.Run("happy path verifies ID token and nonce", func(t *testing.T) {
		f := authtest.NewFakeGoogle(t)
		f.Next = authtest.GoogleUser{Sub: "1001", Email: "Ada@Acme.dev", EmailVerified: true, Name: "Ada", HD: "acme.dev"}
		g, verifier, nonce, _, q := googleLogin(t, newGoogle(f, "acme.dev"))
		ext, err := g.Exchange(ctx, q.Get("code"), verifier, nonce)
		require.NoError(t, err)
		require.Equal(t, "google", ext.Provider)
		require.Equal(t, "1001", ext.Subject)
		require.Equal(t, "ada@acme.dev", ext.Email)
		require.True(t, ext.EmailVerified)
		require.Equal(t, "acme.dev", ext.HostedDomain)
	})
	t.Run("hosted domain is enforced on the hd claim", func(t *testing.T) {
		f := authtest.NewFakeGoogle(t)
		f.Next = authtest.GoogleUser{Sub: "1002", Email: "eve@gmail.com", EmailVerified: true}
		g, verifier, nonce, _, q := googleLogin(t, newGoogle(f, "acme.dev"))
		_, err := g.Exchange(ctx, q.Get("code"), verifier, nonce)
		require.ErrorIs(t, err, auth.ErrDomainNotAllowed)
	})
	t.Run("unverified email rejected", func(t *testing.T) {
		f := authtest.NewFakeGoogle(t)
		f.Next = authtest.GoogleUser{Sub: "1003", Email: "x@acme.dev", EmailVerified: false}
		g, verifier, nonce, _, q := googleLogin(t, newGoogle(f))
		_, err := g.Exchange(ctx, q.Get("code"), verifier, nonce)
		require.ErrorIs(t, err, auth.ErrEmailUnverified)
	})
	for name, mutate := range map[string]func(*authtest.FakeGoogle){
		"nonce mismatch":   func(f *authtest.FakeGoogle) { f.WrongNonce = true },
		"wrong audience":   func(f *authtest.FakeGoogle) { f.WrongAudience = true },
		"expired token":    func(f *authtest.FakeGoogle) { f.Expired = true },
		"forged signature": func(f *authtest.FakeGoogle) { f.SignWithOther = true },
	} {
		t.Run(name+" rejected", func(t *testing.T) {
			f := authtest.NewFakeGoogle(t)
			f.Next = authtest.GoogleUser{Sub: "1004", Email: "a@b.dev", EmailVerified: true}
			mutate(f)
			g, verifier, nonce, _, q := googleLogin(t, newGoogle(f))
			_, err := g.Exchange(ctx, q.Get("code"), verifier, nonce)
			require.Error(t, err)
		})
	}
	t.Run("wrong PKCE verifier rejected by token endpoint", func(t *testing.T) {
		f := authtest.NewFakeGoogle(t)
		f.Next = authtest.GoogleUser{Sub: "1005", Email: "a@b.dev", EmailVerified: true}
		g, _, nonce, _, q := googleLogin(t, newGoogle(f))
		_, err := g.Exchange(ctx, q.Get("code"), oauth2.GenerateVerifier(), nonce)
		require.Error(t, err)
	})
}

func TestGitHubOAuth(t *testing.T) {
	ctx := context.Background()
	f := authtest.NewFakeGitHub(t)
	gh := auth.NewGitHub(auth.GitHubConfig{ClientID: f.ClientID, ClientSecret: f.Secret,
		RedirectURL: "http://localhost:8080/v1/auth/github/callback", BaseURL: f.Server.URL, APIURL: f.APIURL()})
	login := func() (string, url.Values) {
		verifier := oauth2.GenerateVerifier()
		u, err := gh.AuthCodeURL(ctx, "st", "", verifier)
		require.NoError(t, err)
		pu, _ := url.Parse(u)
		require.Equal(t, "S256", pu.Query().Get("code_challenge_method"))
		return verifier, followAuthorize(t, u)
	}
	t.Run("uses the primary verified email only", func(t *testing.T) {
		f.Next = authtest.GitHubUser{ID: 77, Login: "octo", Emails: []authtest.GitHubEmail{
			{Email: "old@x.dev", Verified: true}, {Email: "Octo@X.dev", Primary: true, Verified: true}}}
		v, q := login()
		ext, err := gh.Exchange(ctx, q.Get("code"), v, "")
		require.NoError(t, err)
		require.Equal(t, "77", ext.Subject)
		require.Equal(t, "octo", ext.Login)
		require.Equal(t, "octo@x.dev", ext.Email)
		require.True(t, ext.EmailVerified)
	})
	t.Run("no verified email means no trusted email", func(t *testing.T) {
		f.Next = authtest.GitHubUser{ID: 78, Login: "anon", Emails: []authtest.GitHubEmail{{Email: "a@x.dev", Primary: true}}}
		v, q := login()
		ext, err := gh.Exchange(ctx, q.Get("code"), v, "")
		require.NoError(t, err)
		require.Empty(t, ext.Email)
		require.False(t, ext.EmailVerified)
	})
	t.Run("bad verifier fails", func(t *testing.T) {
		f.Next = authtest.GitHubUser{ID: 79, Login: "x"}
		_, q := login()
		_, err := gh.Exchange(ctx, q.Get("code"), oauth2.GenerateVerifier(), "")
		require.Error(t, err)
	})
}
