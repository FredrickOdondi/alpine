package github

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// createOnce performs a non-idempotent create without ever duplicating it.
//
// The transport already retries rate limits (GitHub did not apply those). If
// create fails ambiguously (timeout, connection reset, 5xx — GitHub may have
// applied it) or with a conflict that suggests an earlier attempt or a
// concurrent caller succeeded (conflict(err)), find re-checks for the object;
// if it exists, that is success. Only when it is confirmed absent after an
// ambiguous failure is the create attempted again (with backoff), up to
// RetryPolicy.MaxAttempts.
func (c *Client) createOnce(ctx context.Context, create func() error, find func() (bool, error), conflict func(error) bool) error {
	for attempt := 1; ; attempt++ {
		err := create()
		if err == nil {
			return nil
		}
		ambiguous := IsAmbiguous(err)
		if !ambiguous && (conflict == nil || !conflict(err)) {
			return err
		}
		if ctx.Err() != nil {
			return err
		}
		found, ferr := find()
		if ferr != nil {
			return errors.Join(err, fmt.Errorf("github: re-check after failed create: %w", ferr))
		}
		if found {
			return nil
		}
		if !ambiguous || attempt >= c.t.retry.MaxAttempts {
			return err
		}
		if serr := c.t.sleep(ctx, c.t.backoff(attempt)); serr != nil {
			return errors.Join(err, serr)
		}
	}
}

func isUnprocessable(err error) bool { return StatusCode(err) == 422 }

// Comment idempotency markers.
//
// A keyed comment carries a hidden HTML comment at the end of its body:
//
//	<!-- alpine:idempotency-key=<key> -->
//
// Before posting, existing comments on the issue/PR are scanned for that
// marker. Only comments authored by a bot account (GitHub Apps post as
// "<slug>[bot]", user.type "Bot") count, so a human pasting the marker cannot
// suppress Alpine's comment.

var commentKeyRe = regexp.MustCompile(`^[A-Za-z0-9._:/@+=-]{1,128}$`)

// ErrInvalidIdempotencyKey is returned for keys that cannot be embedded safely.
var ErrInvalidIdempotencyKey = errors.New("github: invalid idempotency key (1-128 of [A-Za-z0-9._:/@+=-], no \"--\")")

// CommentMarker returns the hidden marker embedded in keyed comments.
func CommentMarker(key string) (string, error) {
	if !commentKeyRe.MatchString(key) || strings.Contains(key, "--") {
		return "", ErrInvalidIdempotencyKey
	}
	return "<!-- alpine:idempotency-key=" + key + " -->", nil
}

// BodyWithKey appends the idempotency marker for key to body.
func BodyWithKey(key, body string) (string, error) {
	m, err := CommentMarker(key)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(body, "\n") + "\n\n" + m, nil
}
