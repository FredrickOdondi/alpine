// Package webhook verifies and normalizes GitHub App webhook deliveries
// against the packages/shared intake contract.
//
// It deliberately does not implement the HTTP receiver or touch the inbox
// table (owned by services/api): the receiver calls Verifier.Verify, stores
// the raw payload, and the inbox processor later calls Normalizer.Normalize.
package webhook

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/alpine-dev/alpine/packages/shared/intake"
	"github.com/alpine-dev/alpine/services/integrations/internal/hmacsig"
)

// Header names used by GitHub webhooks.
const (
	HeaderSignature256 = "X-Hub-Signature-256"
	HeaderEvent        = "X-GitHub-Event"
	HeaderDelivery     = "X-GitHub-Delivery"
)

// MaxPayloadBytes is GitHub's documented webhook payload cap (25 MB); receivers
// should not read more than this.
const MaxPayloadBytes = 25 << 20

// Specific failure reasons. Verifier.Verify wraps them together with
// intake.ErrInvalidSignature so callers can match either.
var (
	ErrMissingSignature   = errors.New("github webhook: missing " + HeaderSignature256 + " header")
	ErrMalformedSignature = errors.New("github webhook: malformed " + HeaderSignature256 + " header")
	ErrSignatureMismatch  = errors.New("github webhook: signature mismatch")
	ErrNoSecret           = errors.New("github webhook: empty webhook secret")
)

// VerifySignature checks an X-Hub-Signature-256 header value ("sha256=<hex>")
// against HMAC-SHA256(secret, body) in constant time. body must be the raw,
// unmodified request body.
func VerifySignature(secret, body []byte, header string) error {
	if len(secret) == 0 {
		return ErrNoSecret
	}
	header = strings.TrimSpace(header)
	if header == "" {
		return ErrMissingSignature
	}
	algo, hexSig, ok := strings.Cut(header, "=")
	if !ok || algo != "sha256" {
		return ErrMalformedSignature
	}
	switch err := hmacsig.VerifyHex(secret, body, hexSig); {
	case err == nil:
		return nil
	case errors.Is(err, hmacsig.ErrMalformed):
		return ErrMalformedSignature
	default:
		return ErrSignatureMismatch
	}
}

// Sign returns the X-Hub-Signature-256 header value for body (tests/fixtures).
func Sign(secret, body []byte) string {
	return "sha256=" + hmacsig.Sign(secret, body)
}

// Verifier implements intake.Verifier for GitHub App webhooks.
type Verifier struct {
	Secret []byte // the App's webhook secret
}

var _ intake.Verifier = Verifier{}

// Source implements intake.Verifier.
func (Verifier) Source() intake.Source { return intake.SourceGitHub }

// Verify implements intake.Verifier. Errors match both intake.ErrInvalidSignature
// and the specific reason (ErrMissingSignature, ErrSignatureMismatch, ...).
func (v Verifier) Verify(h http.Header, body []byte) error {
	if err := VerifySignature(v.Secret, body, h.Get(HeaderSignature256)); err != nil {
		return fmt.Errorf("%w: %w", intake.ErrInvalidSignature, err)
	}
	return nil
}
