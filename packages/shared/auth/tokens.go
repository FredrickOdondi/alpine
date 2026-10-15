package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// RandomToken returns n random bytes, base64url-encoded without padding.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken is SHA-256(token): how invitation tokens are stored.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// keyed returns hex(HMAC-SHA256(secret, purpose|value)), used to derive Redis keys from secret
// or personal values (session ids, IPs, emails) so neither appears in Redis.
func keyed(secret []byte, purpose, value string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write([]byte(value))
	return hex.EncodeToString(m.Sum(nil)[:20])
}

// ConstantTimeEqual compares strings without leaking timing.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// API keys --------------------------------------------------------------------------------------

const apiKeyAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// APIKeyPrefixLen is the length of the public lookup prefix.
const APIKeyPrefixLen = 12

// ErrMalformedAPIKey is returned by ParseAPIKey.
var ErrMalformedAPIKey = errors.New("auth: malformed API key")

func randomAlnum(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	for i := range b {
		b[i] = apiKeyAlphabet[int(b[i])%len(apiKeyAlphabet)] // 256 % 36 bias is negligible for 40+ chars
	}
	return string(b)
}

// GenerateAPIKey returns a new key "alp_<prefix>_<secret>" plus its prefix and secret.
// The secret carries ~206 bits of entropy.
func GenerateAPIKey() (token, prefix, secret string) {
	prefix = randomAlnum(APIKeyPrefixLen)
	secret = randomAlnum(40)
	return "alp_" + prefix + "_" + secret, prefix, secret
}

// ParseAPIKey splits a presented key into prefix and secret.
func ParseAPIKey(token string) (prefix, secret string, err error) {
	rest, ok := strings.CutPrefix(token, "alp_")
	if !ok {
		return "", "", ErrMalformedAPIKey
	}
	prefix, secret, ok = strings.Cut(rest, "_")
	if !ok || len(prefix) != APIKeyPrefixLen || len(secret) < 32 || len(secret) > 128 {
		return "", "", ErrMalformedAPIKey
	}
	for _, c := range prefix + secret {
		if !strings.ContainsRune(apiKeyAlphabet, c) {
			return "", "", ErrMalformedAPIKey
		}
	}
	return prefix, secret, nil
}

// HashAPIKeySecret is HMAC-SHA256(pepper, secret). The pepper lives only in the environment, so a
// database dump alone cannot be used to brute-force or verify keys offline.
func HashAPIKeySecret(pepper []byte, secret string) []byte {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(secret))
	return m.Sum(nil)
}

// VerifyAPIKeySecret checks secret against a stored hash in constant time.
func VerifyAPIKeySecret(pepper []byte, secret string, hash []byte) bool {
	return hmac.Equal(HashAPIKeySecret(pepper, secret), hash)
}

// MaskEmail renders an email for logs without exposing it: "a***@e***.com".
func MaskEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain == "" {
		return "***"
	}
	dot := strings.LastIndex(domain, ".")
	tld := ""
	if dot > 0 {
		tld = domain[dot:]
		domain = domain[:dot]
	}
	return local[:1] + "***@" + domain[:1] + "***" + tld
}
