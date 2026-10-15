package webhook

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/alpine-dev/alpine/packages/shared/intake"
)

var testSecret = []byte("fake-webhook-secret-for-tests")

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"action":"opened","issue":{"id":1}}`)
	valid := Sign(testSecret, body)
	cases := []struct {
		name   string
		secret []byte
		body   []byte
		header string
		want   error
	}{
		{"valid", testSecret, body, valid, nil},
		{"valid with surrounding space", testSecret, body, "  " + valid + " ", nil},
		{"tampered body", testSecret, []byte(`{"action":"opened","issue":{"id":2}}`), valid, ErrSignatureMismatch},
		{"body with extra whitespace", testSecret, append(append([]byte(nil), body...), '\n'), valid, ErrSignatureMismatch},
		{"wrong secret", []byte("another-secret"), body, valid, ErrSignatureMismatch},
		{"empty secret", nil, body, valid, ErrNoSecret},
		{"missing header", testSecret, body, "", ErrMissingSignature},
		{"sha1 header", testSecret, body, "sha1=" + strings.TrimPrefix(valid, "sha256="), ErrMalformedSignature},
		{"no prefix", testSecret, body, strings.TrimPrefix(valid, "sha256="), ErrMalformedSignature},
		{"non-hex", testSecret, body, "sha256=" + strings.Repeat("g", 64), ErrMalformedSignature},
		{"short", testSecret, body, valid[:len(valid)-2], ErrMalformedSignature},
		{"empty digest", testSecret, body, "sha256=", ErrMalformedSignature},
		{"wrong digest right length", testSecret, body, "sha256=" + strings.Repeat("0", 64), ErrSignatureMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := VerifySignature(c.secret, c.body, c.header); !errors.Is(err, c.want) {
				t.Fatalf("VerifySignature: got %v, want %v", err, c.want)
			}
			h := http.Header{}
			if c.header != "" {
				h.Set(HeaderSignature256, c.header)
			}
			err := Verifier{Secret: c.secret}.Verify(h, c.body)
			if c.want == nil {
				if err != nil {
					t.Fatalf("Verify: unexpected %v", err)
				}
				return
			}
			if !errors.Is(err, intake.ErrInvalidSignature) || !errors.Is(err, c.want) {
				t.Fatalf("Verify: got %v, want %v wrapped in intake.ErrInvalidSignature", err, c.want)
			}
		})
	}
	if (Verifier{}).Source() != intake.SourceGitHub {
		t.Fatal("source")
	}
}
