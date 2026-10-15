package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/intake"
)

// Inbox is the subset of store.Inbox the receivers need.
type Inbox interface {
	ResolveInstallation(ctx context.Context, provider string, installationID int64) (uuid.UUID, bool, error)
	InsertInboxEvent(ctx context.Context, org uuid.UUID, source, deliveryID, eventType, action string, payload []byte) (bool, error)
}

// SentryOrgs resolves a Sentry installation to an org.
type SentryOrgs interface {
	OrgForInstallation(ctx context.Context, installationUUID string) (uuid.UUID, bool, error)
}

// Ack statuses (WebhookAck.status).
const (
	StatusAccepted  = "accepted"
	StatusDuplicate = "duplicate"
	StatusIgnored   = "ignored"
)

// Result of a receive.
type Result struct {
	HTTPStatus int
	Status     string // accepted|duplicate|ignored, or an error code
	Message    string
	DeliveryID string
}

// Receiver handles webhook deliveries.
type Receiver struct {
	Inbox          Inbox
	GitHubVerifier intake.Verifier
	SentryVerifier intake.Verifier
	SentryOrg      SentryOrgs
	Logger         *slog.Logger
	// Notify is called after a new inbox row is written (wakes the inbox processor).
	Notify func()
}

func (rc *Receiver) log() *slog.Logger {
	if rc.Logger != nil {
		return rc.Logger
	}
	return slog.Default()
}

// readLimited reads at most limit bytes; tooLarge reports an oversized body.
func readLimited(w http.ResponseWriter, r *http.Request, limit int64) (body []byte, tooLarge bool, err error) {
	if r.ContentLength > limit {
		return nil, true, nil
	}
	body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return nil, true, nil
	}
	return body, false, err
}

// GitHub processes one GitHub App delivery.
func (rc *Receiver) GitHub(w http.ResponseWriter, r *http.Request) Result {
	delivery := r.Header.Get("X-GitHub-Delivery")
	event := r.Header.Get("X-GitHub-Event")
	if delivery == "" || event == "" {
		return Result{HTTPStatus: http.StatusBadRequest, Status: "bad_request", Message: "missing X-GitHub-Event or X-GitHub-Delivery"}
	}
	body, tooLarge, err := readLimited(w, r, MaxGitHubBody)
	if tooLarge {
		return Result{HTTPStatus: http.StatusRequestEntityTooLarge, Status: "payload_too_large", Message: "body exceeds 25 MB", DeliveryID: delivery}
	}
	if err != nil {
		return Result{HTTPStatus: http.StatusBadRequest, Status: "bad_request", Message: "unreadable body"}
	}
	if err := rc.GitHubVerifier.Verify(r.Header, body); err != nil {
		rc.log().Warn("github webhook signature rejected", "delivery", delivery, "event", event)
		return Result{HTTPStatus: http.StatusUnauthorized, Status: "invalid_signature", Message: "signature missing or invalid", DeliveryID: delivery}
	}
	var env struct {
		Action       string `json:"action"`
		Installation *struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return Result{HTTPStatus: http.StatusBadRequest, Status: "bad_request", Message: "invalid JSON", DeliveryID: delivery}
	}
	if event == "ping" || env.Installation == nil {
		return Result{HTTPStatus: http.StatusAccepted, Status: StatusIgnored, DeliveryID: delivery}
	}
	org, ok, err := rc.Inbox.ResolveInstallation(r.Context(), "github", env.Installation.ID)
	if err != nil {
		rc.log().Error("resolve installation", "err", err)
		return Result{HTTPStatus: http.StatusInternalServerError, Status: "internal", Message: "try again", DeliveryID: delivery}
	}
	if !ok {
		// Unknown/uninstalled installation: acknowledge so GitHub stops retrying.
		rc.log().Info("github webhook for unknown installation", "installation", env.Installation.ID, "delivery", delivery)
		return Result{HTTPStatus: http.StatusAccepted, Status: StatusIgnored, DeliveryID: delivery}
	}
	return rc.insert(r.Context(), org, intake.SourceGitHub, delivery, event, env.Action, body)
}

// Sentry processes one Sentry integration delivery; Request-ID is the dedupe key.
func (rc *Receiver) Sentry(w http.ResponseWriter, r *http.Request) Result {
	delivery := r.Header.Get("Request-ID")
	resource := r.Header.Get("Sentry-Hook-Resource")
	if delivery == "" || resource == "" {
		return Result{HTTPStatus: http.StatusBadRequest, Status: "bad_request", Message: "missing Request-ID or Sentry-Hook-Resource"}
	}
	body, tooLarge, err := readLimited(w, r, MaxSentryBody)
	if tooLarge {
		return Result{HTTPStatus: http.StatusRequestEntityTooLarge, Status: "payload_too_large", Message: "body exceeds 5 MB", DeliveryID: delivery}
	}
	if err != nil {
		return Result{HTTPStatus: http.StatusBadRequest, Status: "bad_request", Message: "unreadable body"}
	}
	if rc.SentryVerifier == nil || rc.SentryVerifier.Verify(r.Header, body) != nil {
		rc.log().Warn("sentry webhook signature rejected", "request_id", delivery)
		return Result{HTTPStatus: http.StatusUnauthorized, Status: "invalid_signature", Message: "signature missing or invalid", DeliveryID: delivery}
	}
	var env struct {
		Action       string `json:"action"`
		Installation struct {
			UUID string `json:"uuid"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return Result{HTTPStatus: http.StatusBadRequest, Status: "bad_request", Message: "invalid JSON", DeliveryID: delivery}
	}
	if resource == "installation" || env.Installation.UUID == "" || rc.SentryOrg == nil {
		return Result{HTTPStatus: http.StatusAccepted, Status: StatusIgnored, DeliveryID: delivery}
	}
	org, ok, err := rc.SentryOrg.OrgForInstallation(r.Context(), env.Installation.UUID)
	if err != nil {
		return Result{HTTPStatus: http.StatusInternalServerError, Status: "internal", Message: "try again", DeliveryID: delivery}
	}
	if !ok {
		rc.log().Info("sentry webhook for unknown installation", "request_id", delivery)
		return Result{HTTPStatus: http.StatusAccepted, Status: StatusIgnored, DeliveryID: delivery}
	}
	return rc.insert(r.Context(), org, intake.SourceSentry, delivery, resource, env.Action, body)
}

func (rc *Receiver) insert(ctx context.Context, org uuid.UUID, src intake.Source, delivery, event, action string, body []byte) Result {
	inserted, err := rc.Inbox.InsertInboxEvent(ctx, org, string(src), delivery, event, action, body)
	if err != nil {
		rc.log().Error("insert inbox event", "err", err, "source", src)
		return Result{HTTPStatus: http.StatusInternalServerError, Status: "internal", Message: "try again", DeliveryID: delivery}
	}
	if !inserted {
		return Result{HTTPStatus: http.StatusAccepted, Status: StatusDuplicate, DeliveryID: delivery}
	}
	rc.log().Info("webhook accepted", "source", src, "event", event, "action", action, "delivery", delivery, "org", org)
	if rc.Notify != nil {
		rc.Notify()
	}
	return Result{HTTPStatus: http.StatusAccepted, Status: StatusAccepted, DeliveryID: delivery}
}
