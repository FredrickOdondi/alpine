// Package hmacsig implements constant-time verification of hex-encoded
// HMAC-SHA256 webhook signatures shared by the GitHub and Sentry verifiers.
package hmacsig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

var (
	ErrNoSecret  = errors.New("signature: empty secret")
	ErrMalformed = errors.New("signature: malformed signature")
	ErrMismatch  = errors.New("signature: mismatch")
)

// Sign returns hex(HMAC-SHA256(secret, body)).
func Sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// VerifyHex checks that sigHex is hex(HMAC-SHA256(secret, body)) using a
// constant-time comparison of the decoded MACs.
func VerifyHex(secret, body []byte, sigHex string) error {
	if len(secret) == 0 {
		return ErrNoSecret
	}
	got, err := hex.DecodeString(sigHex)
	if err != nil || len(got) != sha256.Size {
		return ErrMalformed
	}
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	if !hmac.Equal(got, m.Sum(nil)) {
		return ErrMismatch
	}
	return nil
}
