package sentry

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/alpine-dev/alpine/packages/shared/intake"
)

var testSecret = []byte("fake-sentry-client-secret")

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"action":"created","data":{"issue":{"id":"1"}}}`)
	valid := Sign(testSecret, body)
	cases := []struct {
		name   string
		secret []byte
		body   []byte
		header string
		want   error
	}{
		{"valid", testSecret, body, valid, nil},
		{"valid uppercase", testSecret, body, strings.ToUpper(valid), nil},
		{"tampered body", testSecret, bytes.Replace(body, []byte(`"1"`), []byte(`"2"`), 1), valid, ErrSignatureMismatch},
		{"wrong secret", []byte("not-the-secret"), body, valid, ErrSignatureMismatch},
		{"empty secret", nil, body, valid, ErrNoSecret},
		{"missing header", testSecret, body, "", ErrMissingSignature},
		{"whitespace header", testSecret, body, "   ", ErrMissingSignature},
		{"github-style prefix", testSecret, body, "sha256=" + valid, ErrMalformedSignature},
		{"non-hex", testSecret, body, strings.Repeat("x", 64), ErrMalformedSignature},
		{"truncated", testSecret, body, valid[:40], ErrMalformedSignature},
		{"right length wrong mac", testSecret, body, strings.Repeat("a", 64), ErrSignatureMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := VerifySignature(c.secret, c.body, c.header); !errors.Is(err, c.want) {
				t.Fatalf("VerifySignature: got %v, want %v", err, c.want)
			}
			h := http.Header{}
			if c.header != "" {
				h.Set(HeaderSignature, c.header)
			}
			err := Verifier{ClientSecret: c.secret}.Verify(h, c.body)
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
	if (Verifier{}).Source() != intake.SourceSentry {
		t.Fatal("source")
	}
}
