package hmacsig

import (
	"errors"
	"strings"
	"testing"
)

func TestVerifyHex(t *testing.T) {
	secret := []byte("s3cr3t")
	body := []byte(`{"a":1}`)
	good := Sign(secret, body)
	cases := []struct {
		name   string
		secret []byte
		body   []byte
		sig    string
		want   error
	}{
		{"valid", secret, body, good, nil},
		{"valid uppercase hex", secret, body, strings.ToUpper(good), nil},
		{"tampered body", secret, []byte(`{"a":2}`), good, ErrMismatch},
		{"wrong secret", []byte("other"), body, good, ErrMismatch},
		{"empty secret", nil, body, good, ErrNoSecret},
		{"not hex", secret, body, "zz" + good[2:], ErrMalformed},
		{"truncated", secret, body, good[:62], ErrMalformed},
		{"too long", secret, body, good + "00", ErrMalformed},
		{"empty", secret, body, "", ErrMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := VerifyHex(c.secret, c.body, c.sig); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}
