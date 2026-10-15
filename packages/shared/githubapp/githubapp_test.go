package githubapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func creds() Credentials {
	return Credentials{AppID: 7, Slug: "alpine-dev", Name: "Alpine dev", PrivateKeyPEM: "-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----\n",
		WebhookSecret: "whsec", ClientID: "Iv1.abc", ClientSecret: "cs"}
}

func TestFileStoreRoundTripAndMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "github-app.json")
	s := FileStore{Path: p}
	if _, err := s.Load(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
	if err := s.Save(context.Background(), creds()); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", st.Mode(), err)
	}
	got, err := s.Load(context.Background())
	if err != nil || got.AppID != 7 || got.WebhookSecret != "whsec" || got.Source != "file" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestCredentialsNeverPrintSecrets(t *testing.T) {
	c := creds()
	for _, s := range []string{c.String(), c.GoString()} {
		if strings.Contains(s, "whsec") || strings.Contains(s, "PRIVATE") || strings.Contains(s, "cs}") {
			t.Fatalf("secret leaked: %s", s)
		}
	}
}

func TestCachedEnvWinsAndRefusesSave(t *testing.T) {
	env := creds()
	c := &Cached{Env: &env}
	if err := c.Save(context.Background(), creds()); err == nil {
		t.Fatal("save must be refused when env credentials are set")
	}
	got, _ := c.Load(context.Background())
	if got.AppID != 7 {
		t.Fatal("env creds not returned")
	}
}

func TestValidate(t *testing.T) {
	c := creds()
	c.WebhookSecret = ""
	if c.Validate() == nil {
		t.Fatal("missing webhook secret must fail")
	}
}
