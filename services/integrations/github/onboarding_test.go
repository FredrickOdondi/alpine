package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewManifestPermissionsAndValidate(t *testing.T) {
	m := NewManifest(ManifestOptions{Name: "Alpine", HomepageURL: "http://localhost:5173", WebhookURL: "https://smee.io/abc",
		RedirectURL: "http://localhost:8080/v1/setup/github-app/callback", SetupURL: "http://localhost:8080/v1/github/setup"})
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"contents": "write", "pull_requests": "write", "issues": "write", "metadata": "read", "checks": "read"}
	if len(m.DefaultPermissions) != len(want) {
		t.Fatalf("permissions %v", m.DefaultPermissions)
	}
	for k, v := range want {
		if m.DefaultPermissions[k] != v {
			t.Fatalf("%s=%s want %s", k, m.DefaultPermissions[k], v)
		}
	}
	for _, f := range []string{"workflows", "administration", "secrets"} {
		if _, ok := m.DefaultPermissions[f]; ok {
			t.Fatalf("forbidden permission %s", f)
		}
	}
	if !m.RequestOAuthOnInstall || m.SetupURL == "" || m.HookAttributes.URL != "https://smee.io/abc" {
		t.Fatalf("manifest %+v", m)
	}
	bad := m
	bad.DefaultPermissions = map[string]string{"workflows": "write"}
	if bad.Validate() == nil {
		t.Fatal("workflows must be refused")
	}
	bad = m
	bad.HookAttributes.URL = ""
	if bad.Validate() == nil {
		t.Fatal("missing webhook URL must be refused")
	}
}

func TestManifestPostURL(t *testing.T) {
	if got := ManifestPostURL("", "", "s t"); got != "https://github.com/settings/apps/new?state=s+t" {
		t.Fatal(got)
	}
	if got := ManifestPostURL("http://gh.test/", "acme", "x"); got != "http://gh.test/organizations/acme/settings/apps/new?state=x" {
		t.Fatal(got)
	}
}

func TestConvertManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/abc123/conversions" || r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected", 400)
			return
		}
		_, _ = w.Write([]byte(`{"id":99,"slug":"alpine-dev","name":"Alpine dev","html_url":"https://github.com/apps/alpine-dev","client_id":"Iv1.x","client_secret":"cs","webhook_secret":"wh","pem":"PEM","owner":{"login":"fred","type":"User"}}`))
	}))
	defer srv.Close()
	c, err := ConvertManifest(context.Background(), srv.Client(), srv.URL, "abc123")
	if err != nil || c.ID != 99 || c.Owner.Login != "fred" || c.WebhookSecret != "wh" {
		t.Fatalf("%v %v", c, err)
	}
	if strings.Contains(c.String(), "wh") && strings.Contains(c.String(), "PEM") {
		t.Fatal("secrets leaked in String")
	}
	if _, err := ConvertManifest(context.Background(), srv.Client(), srv.URL, "../evil"); err == nil {
		t.Fatal("bad code must be refused before any request")
	}
}

func TestExchangeUserCodeAndInstallationAccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_ = r.ParseForm()
			if r.Form.Get("code") != "good" || r.Form.Get("client_secret") != "cs" {
				_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"ghu_x","token_type":"bearer"}`))
		case "/user/installations":
			if r.Header.Get("Authorization") != "Bearer ghu_x" {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"total_count":2,"installations":[{"id":5},{"id":6}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	tok, err := ExchangeUserCode(ctx, srv.Client(), srv.URL, "Iv1.x", "cs", "good")
	if err != nil || tok != "ghu_x" {
		t.Fatalf("%q %v", tok, err)
	}
	if _, err := ExchangeUserCode(ctx, srv.Client(), srv.URL, "Iv1.x", "cs", "bad"); err == nil {
		t.Fatal("bad code must fail")
	}
	ok, err := UserCanAccessInstallation(ctx, srv.Client(), srv.URL, tok, 6)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	ok, _ = UserCanAccessInstallation(ctx, srv.Client(), srv.URL, tok, 7)
	if ok {
		t.Fatal("installation 7 is not the user's")
	}
}

func TestAppInstallationReadsAndGetFile(t *testing.T) {
	var tokenBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/installations/5" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":5,"app_id":12345,"account":{"login":"fred","type":"User"},"repository_selection":"selected"}`))
		case r.URL.Path == "/app/installations/5/access_tokens":
			_ = json.NewDecoder(r.Body).Decode(&tokenBody)
			_, _ = w.Write([]byte(`{"token":"ghs_list","expires_at":"2099-01-01T00:00:00Z"}`))
		case r.URL.Path == "/installation/repositories":
			if r.Header.Get("Authorization") != "Bearer ghs_list" {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"total_count":1,"repositories":[{"id":11,"full_name":"fred/testbed","name":"testbed","private":true,"default_branch":"main"}]}`))
		case r.URL.Path == "/app/installations/5/access_tokens/x":
		case r.URL.Path == "/repos/fred/testbed/contents/.alpine/factory.yaml":
			if r.URL.Query().Get("ref") != "main" {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write([]byte(`{"type":"file","encoding":"base64","sha":"abc","size":9,"content":"` + base64.StdEncoding.EncodeToString([]byte("version: 1")) + `"}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer srv.Close()
	a, err := NewApp(AppConfig{AppID: 12345, PrivateKey: key(t), BaseURL: srv.URL, BotLogin: "alpine-dev[bot]", Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	inst, err := a.GetInstallation(ctx, 5)
	if err != nil || inst.Account.Login != "fred" || inst.RepositorySelection != "selected" {
		t.Fatalf("%+v %v", inst, err)
	}
	repos, err := a.ListInstallationRepos(ctx, 5)
	if err != nil || len(repos) != 1 || repos[0].FullName != "fred/testbed" {
		t.Fatalf("%+v %v", repos, err)
	}
	if p, _ := tokenBody["permissions"].(map[string]any); len(p) != 1 || p["metadata"] != "read" {
		t.Fatalf("listing token must be metadata:read only, asked %v", tokenBody)
	}
	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Owner: "fred", Repo: "testbed", Tokens: StaticToken("t"), Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	b, sha, err := c.GetFile(ctx, ".alpine/factory.yaml", "main")
	if err != nil || string(b) != "version: 1" || sha != "abc" {
		t.Fatalf("%q %q %v", b, sha, err)
	}
	if _, _, err := c.GetFile(ctx, ".alpine/factory.yaml", "dev"); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("want ErrFileNotFound, got %v", err)
	}
}
