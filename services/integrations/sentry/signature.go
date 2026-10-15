// Package sentry verifies and normalizes Sentry integration-platform webhooks
// (issue, error and event_alert resources) against the packages/shared intake
// contract.
package sentry

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/alpine-dev/alpine/packages/shared/intake"
	"github.com/alpine-dev/alpine/services/integrations/internal/hmacsig"
)

// Header names sent by Sentry integration-platform webhooks.
const (
	HeaderSignature = "Sentry-Hook-Signature"
	HeaderResource  = "Sentry-Hook-Resource"
	HeaderTimestamp = "Sentry-Hook-Timestamp"
	HeaderRequestID = "Request-ID"
)

// MaxPayloadBytes is a suggested cap for receivers. Sentry error payloads can
// carry full stack traces with context lines; 5 MB is generous.
const MaxPayloadBytes = 5 << 20

// Specific failure reasons; Verifier.Verify wraps them with intake.ErrInvalidSignature.
var (
	ErrMissingSignature   = errors.New("sentry webhook: missing " + HeaderSignature + " header")
	ErrMalformedSignature = errors.New("sentry webhook: malformed " + HeaderSignature + " header")
	ErrSignatureMismatch  = errors.New("sentry webhook: signature mismatch")
	ErrNoSecret           = errors.New("sentry webhook: empty client secret")
)

// VerifySignature checks a Sentry-Hook-Signature header: the hex
// HMAC-SHA256 of the raw request body keyed with the integration's client
// secret. Comparison is constant time on the decoded MAC.
func VerifySignature(clientSecret, body []byte, header string) error {
	if len(clientSecret) == 0 {
		return ErrNoSecret
	}
	header = strings.TrimSpace(header)
	if header == "" {
		return ErrMissingSignature
	}
	switch err := hmacsig.VerifyHex(clientSecret, body, header); {
	case err == nil:
		return nil
	case errors.Is(err, hmacsig.ErrMalformed):
		return ErrMalformedSignature
	default:
		return ErrSignatureMismatch
	}
}

// Sign returns the Sentry-Hook-Signature value for body (tests/fixtures).
func Sign(clientSecret, body []byte) string { return hmacsig.Sign(clientSecret, body) }

// Verifier implements intake.Verifier for Sentry integration webhooks.
type Verifier struct {
	ClientSecret []byte
}

var _ intake.Verifier = Verifier{}

// Source implements intake.Verifier.
func (Verifier) Source() intake.Source { return intake.SourceSentry }

// Verify implements intake.Verifier. Errors match both intake.ErrInvalidSignature
// and the specific reason.
func (v Verifier) Verify(h http.Header, body []byte) error {
	if err := VerifySignature(v.ClientSecret, body, h.Get(HeaderSignature)); err != nil {
		return fmt.Errorf("%w: %w", intake.ErrInvalidSignature, err)
	}
	return nil
}
