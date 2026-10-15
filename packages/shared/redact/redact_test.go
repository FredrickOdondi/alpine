package redact

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	r := New()
	cases := map[string]string{
		"openai":  "use sk-proj-abcdefghijklmnopqrstuvwxyz0123 please",
		"github":  "token ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"aws":     "AKIAABCDEFGHIJKLMNOP",
		"assign":  `password = "hunter2hunter2"`,
		"bearer":  "Authorization: Bearer abcdefghijklmnopqrstuvwxyz",
		"url":     "postgres://user:s3cretpass@db:5432/x",
		"privkey": "-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----",
	}
	for name, in := range cases {
		out, hits := r.Redact(in)
		if len(hits) == 0 || !strings.Contains(out, "[REDACTED") {
			t.Errorf("%s: not redacted: %q", name, out)
		}
		for _, secret := range []string{"abcdefghijklmnopqrstuvwxyz0123", "hunter2hunter2", "s3cretpass", "MIIabc", "AKIAABCDEFGHIJKLMNOP"} {
			if strings.Contains(in, secret) && strings.Contains(out, secret) {
				t.Errorf("%s: secret leaked: %q", name, out)
			}
		}
	}
	clean := "TypeError: cannot read properties of undefined (reading 'id') at Login.tsx:42"
	if out, hits := r.Redact(clean); out != clean || len(hits) != 0 {
		t.Errorf("clean text modified: %q %v", out, hits)
	}
}
