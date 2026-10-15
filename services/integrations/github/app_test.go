package github

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
)

// key generates a throwaway RSA key per test binary run; no keys are committed.
func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func newTestApp(t *testing.T, f *fakeGitHub, c *clock) *App {
	t.Helper()
	a, err := NewApp(AppConfig{AppID: 12345, PrivateKey: key(t), BaseURL: f.URL + "/", Now: c.Now, UserAgent: "alpine-test", BotLogin: "alpine-dev[bot]",
		Sleep: noSleep, Rand: func() float64 { return 0.5 }})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func parseJWT(t *testing.T, bearer string) *jwt.RegisteredClaims {
	t.Helper()
	raw, ok := strings.CutPrefix(bearer, "Bearer ")
	if !ok {
		t.Fatalf("not a bearer auth header: %q", bearer)
	}
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(tok *jwt.Token) (any, error) {
		if tok.Method.Alg() != "RS256" {
			return nil, fmt.Errorf("alg %s", tok.Method.Alg())
		}
		return &key(t).PublicKey, nil
	}, jwt.WithoutClaimsValidation())
	if err != nil {
		t.Fatalf("jwt: %v", err)
	}
	return claims
}

func tokenResp(tok string, exp time.Time) route {
	return route{status: 201, body: fmt.Sprintf(`{"token":%q,"expires_at":%q,"permissions":{"contents":"write","metadata":"read"},"repository_selection":"selected"}`,
		tok, exp.Format(time.RFC3339))}
}

func TestJWTClaims(t *testing.T) {
	c := &clock{t: t0}
	a := newTestApp(t, newFake(t, map[string]route{}), c)
	s, err := a.JWT()
	if err != nil {
		t.Fatal(err)
	}
	cl := parseJWT(t, "Bearer "+s)
	if cl.Issuer != "12345" {
		t.Errorf("iss = %q", cl.Issuer)
	}
	if !cl.IssuedAt.Time.Equal(t0.Add(-60 * time.Second)) {
		t.Errorf("iat = %v, want backdated 60s", cl.IssuedAt)
	}
	if exp := cl.ExpiresAt.Time; !exp.After(t0) || exp.Sub(t0) > 10*time.Minute || exp.Sub(cl.IssuedAt.Time) > 10*time.Minute {
		t.Errorf("exp = %v must be within 10m", exp)
	}
}

func TestParsePrivateKeyPEM(t *testing.T) {
	k := key(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	der8, _ := x509.MarshalPKCS8PrivateKey(k)
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der8})
	for name, b := range map[string][]byte{"pkcs1": pkcs1, "pkcs8": pkcs8} {
		got, err := ParsePrivateKeyPEM(b)
		if err != nil || !got.Equal(k) {
			t.Errorf("%s: %v", name, err)
		}
	}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	derEC, _ := x509.MarshalPKCS8PrivateKey(ec)
	bad := map[string][]byte{
		"not pem": []byte("hello"),
		"garbage": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("nope")}),
		"ecdsa":   pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: derEC}),
	}
	for name, b := range bad {
		if _, err := ParsePrivateKeyPEM(b); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestNewAppValidation(t *testing.T) {
	cases := []AppConfig{
		{PrivateKey: key(t)},
		{AppID: 1},
		{AppID: 1, PrivateKey: key(t), BaseURL: "ftp://x"},
		{AppID: 1, PrivateKey: key(t), BaseURL: "https://"},
		{AppID: 1, PrivateKey: key(t), BaseURL: "://bad"},
		{AppID: 1, PrivateKey: key(t), BotLogin: "alpine-dev"}, // missing [bot]
	}
	for i, c := range cases {
		if _, err := NewApp(c); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	a, err := NewApp(AppConfig{AppID: 1, PrivateKey: key(t)})
	if err != nil || a.t.base != DefaultBaseURL || a.t.http != defaultHTTPClient || a.t.ua != defaultUserAgent || a.t.retry.MaxAttempts != 4 {
		t.Fatalf("defaults: %v %+v", err, a)
	}
}

func TestInstallationTokenRequestAndCache(t *testing.T) {
	c := &clock{t: t0}
	f := newFake(t, map[string]route{
		"POST /app/installations/555/access_tokens": tokenResp("ghs_fake1", t0.Add(time.Hour)),
	})
	a := newTestApp(t, f, c)
	req := TokenRequest{InstallationID: 555, Repository: "shop", Permissions: PermsPush()}
	tok, err := a.InstallationToken(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Token != "ghs_fake1" || !tok.ExpiresAt.Equal(t0.Add(time.Hour)) || tok.RepositorySelection != "selected" {
		t.Fatalf("token %+v", tok)
	}
	if s := fmt.Sprintf("%v %+v %#v %s", tok, tok, tok, tok); strings.Contains(s, "ghs_fake1") {
		t.Fatalf("token leaked via fmt: %s", s)
	}

	reqs := f.requests()
	assertSeq(t, reqs, "POST /app/installations/555/access_tokens")
	r := reqs[0]
	assertStdHeaders(t, r, r.Auth)
	if cl := parseJWT(t, r.Auth); cl.Issuer != "12345" {
		t.Errorf("iss %q", cl.Issuer)
	}
	if r.Header.Get("User-Agent") != "alpine-test" {
		t.Errorf("UA %q", r.Header.Get("User-Agent"))
	}
	body := r.JSON(t)
	if fmt.Sprint(body["repositories"]) != "[shop]" || body["repository_ids"] != nil {
		t.Errorf("repositories: %v", body)
	}
	if fmt.Sprint(body["permissions"]) != "map[contents:write metadata:read]" {
		t.Errorf("permissions: %v", body["permissions"])
	}

	// Cached while > 5 minutes left.
	c.Add(54 * time.Minute)
	if tok2, err := a.InstallationToken(context.Background(), req); err != nil || tok2.Token != "ghs_fake1" {
		t.Fatalf("cached: %v %v", tok2, err)
	}
	assertSeq(t, f.requests(), "POST /app/installations/555/access_tokens")

	// Refreshed inside the margin.
	f.set("POST /app/installations/555/access_tokens", tokenResp("ghs_fake2", c.Now().Add(time.Hour)))
	c.Add(2 * time.Minute)
	if tok3, err := a.InstallationToken(context.Background(), req); err != nil || tok3.Token != "ghs_fake2" {
		t.Fatalf("refresh: %v %v", tok3, err)
	}
	if n := len(f.requests()); n != 2 {
		t.Fatalf("expected refresh request, got %d", n)
	}

	// Different permission set => separate token.
	if _, err := a.InstallationToken(context.Background(), TokenRequest{InstallationID: 555, Repository: "shop", Permissions: PermsChecks()}); err != nil {
		t.Fatal(err)
	}
	if n := len(f.requests()); n != 3 {
		t.Fatalf("expected new request for new perms, got %d", n)
	}

	// Invalidate forces a new fetch.
	a.InvalidateToken(req)
	if _, err := a.InstallationToken(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if n := len(f.requests()); n != 4 {
		t.Fatalf("expected request after invalidate, got %d", n)
	}
}

func TestInstallationTokenByRepositoryID(t *testing.T) {
	c := &clock{t: t0}
	f := newFake(t, map[string]route{"POST /app/installations/9/access_tokens": tokenResp("ghs_x", t0.Add(time.Hour))})
	a := newTestApp(t, f, c)
	_, err := a.InstallationToken(context.Background(), TokenRequest{InstallationID: 9, RepositoryID: 777,
		Permissions: Merge(PermsPullRequests(), PermsIssueComments())})
	if err != nil {
		t.Fatal(err)
	}
	b := f.requests()[0].JSON(t)
	if fmt.Sprint(b["repository_ids"]) != "[777]" || b["repositories"] != nil {
		t.Errorf("body %v", b)
	}
	if fmt.Sprint(b["permissions"]) != "map[issues:write metadata:read pull_requests:write]" {
		t.Errorf("perms %v", b["permissions"])
	}
}

func TestInstallationTokenConcurrentSingleFetch(t *testing.T) {
	var hits atomic.Int32
	c := &clock{t: t0}
	f := newFake(t, map[string]route{})
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"token":"ghs_c","expires_at":%q}`, t0.Add(time.Hour).Format(time.RFC3339))
	})
	a := newTestApp(t, f, c)
	req := TokenRequest{InstallationID: 1, Repository: "r", Permissions: PermsPush()}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.InstallationToken(context.Background(), req); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("expected 1 token fetch, got %d", hits.Load())
	}
}

func TestInstallationTokenErrors(t *testing.T) {
	c := &clock{t: t0}
	f := newFake(t, map[string]route{
		"POST /app/installations/1/access_tokens": {status: 422, body: `{"message":"The permissions requested are not granted to this installation.","documentation_url":"https://docs.github.com/rest/apps/apps#create-an-installation-access-token-for-an-app"}`},
		"POST /app/installations/2/access_tokens": {status: 201, body: `{"token":""}`},
		"POST /app/installations/3/access_tokens": {status: 201, body: `not json`},
	})
	a := newTestApp(t, f, c)
	ctx := context.Background()
	p := PermsPush()

	_, err := a.InstallationToken(ctx, TokenRequest{InstallationID: 1, Repository: "r", Permissions: p})
	if StatusCode(err) != 422 || !strings.Contains(err.Error(), "not granted") {
		t.Errorf("422: %v", err)
	}
	if _, err := a.InstallationToken(ctx, TokenRequest{InstallationID: 2, Repository: "r", Permissions: p}); err == nil {
		t.Error("empty token: expected error")
	}
	if _, err := a.InstallationToken(ctx, TokenRequest{InstallationID: 3, Repository: "r", Permissions: p}); err == nil {
		t.Error("bad json: expected error")
	}
	if n := len(f.requests()); n != 3 {
		t.Fatalf("requests %d", n)
	}

	invalid := []TokenRequest{
		{Repository: "r", Permissions: p},
		{InstallationID: 1, Permissions: p},
		{InstallationID: 1, Repository: "r", RepositoryID: 2, Permissions: p},
		{InstallationID: 1, Repository: "o/r", Permissions: p},
		{InstallationID: 1, Repository: "r"},
		{InstallationID: 1, Repository: "r", Permissions: Permissions{"contents": "admin"}},
		{InstallationID: 1, Repository: "r", Permissions: Permissions{"": Read}},
		{InstallationID: 1, Repository: "r", Permissions: Permissions{"workflows": Write}},
		{InstallationID: 1, Repository: "r", Permissions: Permissions{"administration": Read}},
	}
	for i, r := range invalid {
		if _, err := a.InstallationToken(ctx, r); err == nil {
			t.Errorf("invalid %d: expected error", i)
		}
	}
	if n := len(f.requests()); n != 3 {
		t.Fatalf("invalid requests must not hit the network, got %d", n)
	}
	if _, err := a.RepoClient(1, "o", "r", nil); err == nil {
		t.Error("RepoClient without perms: expected error")
	}
}

func TestRepoClientUsesInstallationToken(t *testing.T) {
	c := &clock{t: t0}
	f := newFake(t, map[string]route{
		"POST /app/installations/555/access_tokens": tokenResp("ghs_repo", t0.Add(time.Hour)),
		"POST /repos/acme/shop/issues/42/comments":  {status: 201, body: `{"id":1,"html_url":"u","body":"hi"}`},
	})
	a := newTestApp(t, f, c)
	cl, err := a.RepoClient(555, "acme", "shop", PermsIssueComments(), func(cfg *ClientConfig) { cfg.WritableBranchPrefixes = []string{"bot/"} })
	if err != nil {
		t.Fatal(err)
	}
	if cl.Owner() != "acme" || cl.Repo() != "shop" || cl.prefixes[0] != "bot/" || cl.appID != a.id || cl.botLogin != a.cfg.BotLogin {
		t.Fatalf("client %+v", cl)
	}
	for i := 0; i < 2; i++ {
		if _, err := cl.CreateIssueComment(context.Background(), 42, "", "hi"); err != nil {
			t.Fatal(err)
		}
	}
	reqs := f.requests()
	assertSeq(t, reqs, "POST /app/installations/555/access_tokens",
		"POST /repos/acme/shop/issues/42/comments", "POST /repos/acme/shop/issues/42/comments")
	assertStdHeaders(t, reqs[1], "Bearer ghs_repo")
	if b := reqs[0].JSON(t); fmt.Sprint(b["repositories"]) != "[shop]" ||
		fmt.Sprint(b["permissions"]) != "map[issues:write metadata:read]" {
		t.Errorf("token body %v", b)
	}
}

func TestRepoInstallation(t *testing.T) {
	c := &clock{t: t0}
	f := newFake(t, map[string]route{
		"GET /repos/acme/shop/installation": {status: 200, body: `{"id":555,"account":{"login":"acme"},"permissions":{"contents":"write"}}`},
	})
	a := newTestApp(t, f, c)
	inst, err := a.RepoInstallation(context.Background(), "acme", "shop")
	if err != nil || inst.ID != 555 || inst.Account.Login != "acme" {
		t.Fatalf("%v %+v", err, inst)
	}
	parseJWT(t, f.requests()[0].Auth)
	if _, err := a.RepoInstallation(context.Background(), "acme", "missing"); StatusCode(err) != 404 {
		t.Fatalf("want 404, got %v", err)
	}
}

func TestPermissionsMerge(t *testing.T) {
	got := Merge(Permissions{"contents": Read, "issues": Write}, Permissions{"contents": Write, "issues": Read}, nil)
	if got["contents"] != Write || got["issues"] != Write || len(got) != 2 {
		t.Fatalf("%v", got)
	}
	a := PermsPush()
	a["contents"] = Read
	if PermsPush()["contents"] != Write {
		t.Fatal("presets must return fresh maps")
	}
}
