package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/alpine-dev/alpine/packages/shared/repo"
)

// LoginProvider is an OAuth/OIDC login method. AuthCodeURL always uses PKCE (S256) and a state;
// OIDC providers also bind a nonce into the ID token.
type LoginProvider interface {
	Name() string
	AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error)
	Exchange(ctx context.Context, code, verifier, nonce string) (repo.ExternalIdentity, error)
}

// Google -----------------------------------------------------------------------------------------

// Google is Sign in with Google via OIDC (authorization code + PKCE + nonce). The ID token is
// verified against Google's JWKS (signature, iss, aud, exp) before any claim is trusted.
type Google struct {
	cfg GoogleConfig

	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
}

// NewGoogle returns a Google provider; OIDC discovery happens lazily on first use.
func NewGoogle(cfg GoogleConfig) *Google {
	if cfg.Issuer == "" {
		cfg.Issuer = "https://accounts.google.com"
	}
	return &Google{cfg: cfg}
}

// Name implements LoginProvider.
func (g *Google) Name() string { return "google" }

func (g *Google) client(ctx context.Context) context.Context {
	if g.cfg.HTTPClient != nil {
		return oidc.ClientContext(ctx, g.cfg.HTTPClient)
	}
	return ctx
}

func (g *Google) init(ctx context.Context) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.provider != nil {
		return g.provider, g.verifier, nil
	}
	// Discovery uses a detached context so a cancelled request does not poison the cached provider
	// (the JWKS cache inside go-oidc keeps using it).
	p, err := oidc.NewProvider(g.client(context.WithoutCancel(ctx)), g.cfg.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: google discovery: %w", err)
	}
	g.provider = p
	g.verifier = p.Verifier(&oidc.Config{ClientID: g.cfg.ClientID})
	return g.provider, g.verifier, nil
}

func (g *Google) oauth(p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID: g.cfg.ClientID, ClientSecret: g.cfg.ClientSecret, RedirectURL: g.cfg.RedirectURL,
		Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

// AuthCodeURL implements LoginProvider.
func (g *Google) AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	p, _, err := g.init(ctx)
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("prompt", "select_account")}
	switch len(g.cfg.HostedDomains) {
	case 0:
	case 1:
		opts = append(opts, oauth2.SetAuthURLParam("hd", g.cfg.HostedDomains[0]))
	default:
		opts = append(opts, oauth2.SetAuthURLParam("hd", "*")) // UI hint only; enforced on the hd claim
	}
	return g.oauth(p).AuthCodeURL(state, opts...), nil
}

type googleClaims struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	HD            string `json:"hd"`
}

// Exchange implements LoginProvider.
func (g *Google) Exchange(ctx context.Context, code, verifier, nonce string) (repo.ExternalIdentity, error) {
	p, v, err := g.init(ctx)
	if err != nil {
		return repo.ExternalIdentity{}, err
	}
	tok, err := g.oauth(p).Exchange(g.client(ctx), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return repo.ExternalIdentity{}, fmt.Errorf("auth: google token exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return repo.ExternalIdentity{}, errors.New("auth: google response has no id_token")
	}
	idt, err := v.Verify(g.client(ctx), raw)
	if err != nil {
		return repo.ExternalIdentity{}, fmt.Errorf("auth: google id_token: %w", err)
	}
	if nonce == "" || !ConstantTimeEqual(idt.Nonce, nonce) {
		return repo.ExternalIdentity{}, errors.New("auth: google id_token nonce mismatch")
	}
	var c googleClaims
	if err := idt.Claims(&c); err != nil {
		return repo.ExternalIdentity{}, fmt.Errorf("auth: google claims: %w", err)
	}
	if len(g.cfg.HostedDomains) > 0 && !slices.Contains(g.cfg.HostedDomains, strings.ToLower(c.HD)) {
		return repo.ExternalIdentity{}, ErrDomainNotAllowed
	}
	if c.Email == "" || !c.EmailVerified {
		return repo.ExternalIdentity{}, ErrEmailUnverified
	}
	return repo.ExternalIdentity{
		Provider: "google", Subject: idt.Subject, Email: strings.ToLower(c.Email), EmailVerified: true,
		Name: c.Name, AvatarURL: c.Picture, HostedDomain: strings.ToLower(c.HD),
	}, nil
}

// GitHub -----------------------------------------------------------------------------------------

// GitHub is GitHub OAuth login (OAuth App or the GitHub App's user-to-server flow) with PKCE.
// It is NOT the GitHub App installation used for repo access.
type GitHub struct{ cfg GitHubConfig }

// NewGitHub returns a GitHub provider.
func NewGitHub(cfg GitHubConfig) *GitHub {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://github.com"
	}
	if cfg.APIURL == "" {
		cfg.APIURL = "https://api.github.com"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	cfg.APIURL = strings.TrimRight(cfg.APIURL, "/")
	return &GitHub{cfg: cfg}
}

// Name implements LoginProvider.
func (g *GitHub) Name() string { return "github" }

func (g *GitHub) oauth() *oauth2.Config {
	return &oauth2.Config{
		ClientID: g.cfg.ClientID, ClientSecret: g.cfg.ClientSecret, RedirectURL: g.cfg.RedirectURL,
		Endpoint: oauth2.Endpoint{AuthURL: g.cfg.BaseURL + "/login/oauth/authorize", TokenURL: g.cfg.BaseURL + "/login/oauth/access_token"},
		Scopes:   []string{"read:user", "user:email"},
	}
}

func (g *GitHub) httpClient() *http.Client {
	if g.cfg.HTTPClient != nil {
		return g.cfg.HTTPClient
	}
	return http.DefaultClient
}

// AuthCodeURL implements LoginProvider (nonce unused: GitHub OAuth is not OIDC).
func (g *GitHub) AuthCodeURL(_ context.Context, state, _ string, verifier string) (string, error) {
	return g.oauth().AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), nil
}

// Exchange implements LoginProvider.
func (g *GitHub) Exchange(ctx context.Context, code, verifier, _ string) (repo.ExternalIdentity, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.httpClient())
	tok, err := g.oauth().Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return repo.ExternalIdentity{}, fmt.Errorf("auth: github token exchange: %w", err)
	}
	var u struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := g.get(ctx, tok.AccessToken, "/user", &u); err != nil {
		return repo.ExternalIdentity{}, err
	}
	if u.ID == 0 {
		return repo.ExternalIdentity{}, errors.New("auth: github user has no id")
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := g.get(ctx, tok.AccessToken, "/user/emails", &emails); err != nil {
		return repo.ExternalIdentity{}, err
	}
	// Only a VERIFIED address is trusted (primary preferred); the /user "email" field is not.
	var email string
	for _, e := range emails {
		if e.Verified && (e.Primary || email == "") {
			email = strings.ToLower(e.Email)
		}
	}
	return repo.ExternalIdentity{
		Provider: "github", Subject: strconv.FormatInt(u.ID, 10), GitHubID: u.ID, Login: u.Login,
		Name: u.Name, AvatarURL: u.AvatarURL, Email: email, EmailVerified: email != "",
	}, nil
}

func (g *GitHub) get(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.cfg.APIURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("auth: github %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: github %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
