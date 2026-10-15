// Package server implements apigen.ServerInterface for the core API (repos, tickets, runs, PR
// inbox, metrics, SSE, webhooks). It embeds *authapi.Server (jeff/data), which serves the auth,
// org, member, invitation, API-key and audit operations, and is mounted with authapi.Router so the
// shared auth middleware (sessions, API keys, CSRF, CORS, rate limits) fronts every route.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	sharedauth "github.com/alpine-dev/alpine/packages/shared/auth"
	"github.com/alpine-dev/alpine/packages/shared/events"
	"github.com/alpine-dev/alpine/packages/shared/ticket"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
	"github.com/alpine-dev/alpine/services/api/internal/apigen"
	"github.com/alpine-dev/alpine/services/api/internal/auth"
	"github.com/alpine-dev/alpine/services/api/internal/authapi"
	"github.com/alpine-dev/alpine/services/api/internal/onboarding"
	"github.com/alpine-dev/alpine/services/api/internal/store"
	"github.com/alpine-dev/alpine/services/api/internal/webhook"
	"github.com/alpine-dev/alpine/services/api/internal/workflows"
)

// Subscriber streams board events for an org.
type Subscriber interface {
	Subscribe(ctx context.Context, orgID string) (<-chan events.BoardEvent, func(), error)
}

// Checker is a named readiness probe.
type Checker func(ctx context.Context) error

// ArtifactURLs turns an artifact's object key into a URL the browser can load (a short-lived
// signed URL in production).
type ArtifactURLs interface {
	URL(ctx context.Context, key string) (string, error)
}

// StaticArtifactURLs is the local-dev ArtifactURLs: Base + "/" + key (no signing).
type StaticArtifactURLs struct{ Base string }

// URL implements ArtifactURLs.
func (s StaticArtifactURLs) URL(_ context.Context, key string) (string, error) {
	return s.Base + "/" + key, nil
}

// ReviewMirror mirrors a human PR decision to the SCM (a GitHub PR review). Optional.
type ReviewMirror interface {
	MirrorReview(ctx context.Context, pr ticketrepo.PullRequest, decision, note string) error
}

// Server implements apigen.ServerInterface.
type Server struct {
	*authapi.Server

	Store         store.Store
	Authenticator auth.Authenticator // default auth.Principals{}
	Engine        workflows.Engine
	Events        Subscriber
	Webhooks      *webhook.Receiver
	Artifacts     ArtifactURLs
	Reviews       ReviewMirror
	Onboarding    *onboarding.Service
	Ready         map[string]Checker
	Logger        *slog.Logger
	// SSEPing is the keepalive interval (default 15s).
	SSEPing time.Duration
}

var _ apigen.ServerInterface = (*Server)(nil)

// Handler returns the full HTTP handler: authapi.Router (security headers, CORS, rate limits,
// session/API-key auth, CSRF) over this server.
func (s *Server) Handler() http.Handler {
	return authapi.Router(s, s.Server.Auth)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apigen.Error{Code: code, Message: msg})
}

func list[T any](items []T) map[string][]T {
	if items == nil {
		items = []T{}
	}
	return map[string][]T{"items": items}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, ticketrepo.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	s.log().Error("request failed", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal", "internal error")
}

func (s *Server) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// caller authenticates the request and checks perm; it writes 401/403 and returns ok=false.
func (s *Server) caller(w http.ResponseWriter, r *http.Request, perm sharedauth.Permission) (auth.Identity, bool) {
	a := s.Authenticator
	if a == nil {
		a = auth.Principals{}
	}
	id, err := auth.FromRequest(a, r)
	if err != nil {
		if p := sharedauth.PrincipalFrom(r.Context()); p != nil && p.OrgID == uuid.Nil {
			writeErr(w, http.StatusForbidden, "no_active_org", "create or join an organization first")
			return id, false
		}
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "sign in required")
		return id, false
	}
	if !id.Can(perm) {
		writeErr(w, http.StatusForbidden, "forbidden", "missing permission "+string(perm))
		return id, false
	}
	return id, true
}

// ---- health ----

func (s *Server) GetHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, apigen.Health{Status: "ok", Checks: map[string]string{}})
}

func (s *Server) GetReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	checks, status, code := map[string]string{}, apigen.HealthStatus("ok"), 200
	for name, c := range s.Ready {
		if err := c(ctx); err != nil {
			checks[name], status, code = "error: "+err.Error(), "degraded", 503
		} else {
			checks[name] = "ok"
		}
	}
	writeJSON(w, code, apigen.Health{Status: status, Checks: checks})
}

// ---- repos ----

func (s *Server) ListRepos(w http.ResponseWriter, r *http.Request) {
	id, ok := s.caller(w, r, sharedauth.PermReposRead)
	if !ok {
		return
	}
	rs, err := s.Store.ListRepos(r.Context(), id.OrgID)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]apigen.Repo, 0, len(rs))
	for _, x := range rs {
		out = append(out, apigen.Repo{Id: x.ID, OrgId: x.OrgID, FullName: x.FullName, DefaultBranch: x.DefaultBranch,
			GithubInstallationId: x.GitHubInstallationID, ConfigSha: x.ConfigSHA, CreatedAt: x.CreatedAt})
	}
	writeJSON(w, 200, list(out))
}

func (s *Server) GetRepoConfig(w http.ResponseWriter, r *http.Request, repoID apigen.RepoId) {
	id, ok := s.caller(w, r, sharedauth.PermFactoryConfigRead)
	if !ok {
		return
	}
	if _, err := s.Store.GetRepo(r.Context(), id.OrgID, repoID); err != nil {
		s.fail(w, err)
		return
	}
	row, err := s.Store.LatestFactoryConfig(r.Context(), id.OrgID, repoID)
	if errors.Is(err, ticketrepo.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "No .alpine/factory.yaml on the default branch")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, factoryConfig(row))
}

// ---- tickets ----

func toSummary(t ticketrepo.Ticket) apigen.TicketSummary {
	src := apigen.TicketSummarySource(t.Source)
	return apigen.TicketSummary{Id: t.ID, OrgId: t.OrgID, RepoId: t.RepoID, RepoFullName: t.RepoFullName, Key: t.Key, Title: t.Title,
		Type: apigen.TicketType(t.Type), Stage: apigen.Stage(t.Stage), Source: &src, SourceUrl: t.SourceURL,
		Priority: (*apigen.Priority)(t.Priority), Size: (*apigen.Size)(t.Size), BrPredicted: t.BRPredicted, BrFinal: t.BRFinal,
		TurnsUsed: t.TurnsUsed, GateIterations: t.GateIterations, CostUsd: t.CostUSD, EscalationReason: t.EscalationReason,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

func (s *Server) ListTickets(w http.ResponseWriter, r *http.Request, p apigen.ListTicketsParams) {
	id, ok := s.caller(w, r, sharedauth.PermTicketsRead)
	if !ok {
		return
	}
	f := ticketrepo.TicketFilter{RepoID: p.RepoId}
	if p.Limit != nil {
		f.Limit = *p.Limit
	}
	if p.Stage != nil {
		for _, st := range *p.Stage {
			f.Stages = append(f.Stages, ticket.Stage(st))
		}
	}
	ts, err := s.Store.ListTickets(r.Context(), id.OrgID, f)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]apigen.TicketSummary, 0, len(ts))
	for _, t := range ts {
		out = append(out, toSummary(t))
	}
	writeJSON(w, 200, list(out))
}

func toRun(x ticketrepo.Run) apigen.AgentRun {
	return apigen.AgentRun{Id: x.ID, TicketId: x.TicketID, Kind: apigen.RunKind(x.Kind), Status: apigen.RunStatus(x.Status), Model: x.Model,
		Turns: x.Turns, GateIterations: x.GateIterations, CostUsd: x.CostUSD, StartedAt: x.StartedAt, EndedAt: x.EndedAt}
}

func gateSummary(p ticketrepo.PullRequest) apigen.GateSummary {
	return apigen.GateSummary{Passed: p.GatesPassed, Failed: p.GatesFailed, Warned: p.GatesWarned, Total: p.GatesTotal}
}

func toPR(p ticketrepo.PullRequest) apigen.PullRequest {
	gs := gateSummary(p)
	out := apigen.PullRequest{Id: p.ID, TicketId: p.TicketID, TicketKey: p.TicketKey, TicketTitle: p.TicketTitle, RepoFullName: p.RepoFullName,
		Number: p.Number, Url: p.URL, HeadBranch: p.HeadBranch, HeadSha: p.HeadSHA, State: apigen.PullRequestState(p.State),
		BlastRadius: apigen.BlastRadius(p.BlastRadius), Priority: (*apigen.Priority)(p.Priority), TurnsUsed: p.TurnsUsed, CostUsd: p.CostUSD,
		OpenedAt: p.OpenedAt, MergedAt: p.MergedAt, GateSummary: &gs, HumanDecision: (*apigen.HumanDecision)(p.HumanDecision)}
	if p.Additions != nil || p.Deletions != nil || p.FilesChanged != nil {
		out.DiffSummary = &apigen.DiffSummary{Additions: deref(p.Additions), Deletions: deref(p.Deletions), FilesChanged: deref(p.FilesChanged)}
	}
	return out
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func (s *Server) GetTicket(w http.ResponseWriter, r *http.Request, tid apigen.TicketId) {
	id, ok := s.caller(w, r, sharedauth.PermTicketsRead)
	if !ok {
		return
	}
	ctx, org := r.Context(), id.OrgID
	t, err := s.Store.GetTicket(ctx, org, tid)
	if err != nil {
		s.fail(w, err)
		return
	}
	hist, err := s.Store.StageHistory(ctx, org, tid)
	if err != nil {
		s.fail(w, err)
		return
	}
	runs, err := s.Store.ListRuns(ctx, org, tid)
	if err != nil {
		s.fail(w, err)
		return
	}
	prs, err := s.Store.ListPullRequests(ctx, org, nil, &tid)
	if err != nil {
		s.fail(w, err)
		return
	}
	sum := toSummary(t)
	src := apigen.TicketDetailSource(t.Source)
	d := apigen.TicketDetail{Id: sum.Id, OrgId: sum.OrgId, RepoId: sum.RepoId, RepoFullName: sum.RepoFullName, Key: sum.Key, Title: sum.Title,
		Type: sum.Type, Stage: sum.Stage, Source: &src, SourceUrl: sum.SourceUrl, Priority: sum.Priority, Size: sum.Size,
		BrPredicted: sum.BrPredicted, BrFinal: sum.BrFinal, TurnsUsed: sum.TurnsUsed, GateIterations: sum.GateIterations,
		CostUsd: sum.CostUsd, EscalationReason: sum.EscalationReason, CreatedAt: sum.CreatedAt, UpdatedAt: sum.UpdatedAt,
		Body: t.Body, Confidence: t.Confidence, WorkflowId: t.WorkflowID,
		Limits:       apigen.Limits{MaxTurns: t.MaxTurns, MaxGateIterations: t.MaxGateIters, MaxCostUsd: t.MaxCostUSD},
		StageHistory: []apigen.StageTransition{}, Runs: []apigen.AgentRun{}, PullRequests: []apigen.PullRequest{}}
	for _, h := range hist {
		d.StageHistory = append(d.StageHistory, apigen.StageTransition{Id: h.ID, FromStage: (*apigen.Stage)(h.FromStage),
			ToStage: apigen.Stage(h.ToStage), Reason: h.Reason, Actor: h.Actor, At: h.At})
	}
	for _, x := range runs {
		d.Runs = append(d.Runs, toRun(x))
	}
	for _, p := range prs {
		d.PullRequests = append(d.PullRequests, toPR(p))
	}
	writeJSON(w, 200, d)
}

func (s *Server) PostTicketAction(w http.ResponseWriter, r *http.Request, tid apigen.TicketId) {
	var req apigen.TicketActionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	perm := sharedauth.PermTicketsWrite
	if req.Action == apigen.TicketActionApprove || req.Action == apigen.TicketActionReject {
		perm = sharedauth.PermTicketsApprove
	}
	who, ok := s.caller(w, r, perm)
	if !ok {
		return
	}
	t, err := s.Store.GetTicket(r.Context(), who.OrgID, tid)
	if err != nil {
		s.fail(w, err)
		return
	}
	note := ""
	if req.Note != nil {
		note = *req.Note
	}
	if !s.signal(w, r, t, ticketflow.HumanDecision{Action: string(req.Action), Note: note, Actor: who.Actor()}) {
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// signal sends a human decision to the ticket workflow; it writes the error response itself.
func (s *Server) signal(w http.ResponseWriter, r *http.Request, t ticketrepo.Ticket, d ticketflow.HumanDecision) bool {
	if t.WorkflowID == nil {
		writeErr(w, http.StatusConflict, "no_workflow", "ticket has no workflow")
		return false
	}
	err := s.Engine.Signal(r.Context(), *t.WorkflowID, d)
	if workflows.IsNotFound(err) {
		writeErr(w, http.StatusConflict, "workflow_closed", "ticket workflow has finished")
		return false
	}
	if err != nil {
		s.fail(w, err)
		return false
	}
	return true
}

func (s *Server) ListTicketRuns(w http.ResponseWriter, r *http.Request, tid apigen.TicketId) {
	id, ok := s.caller(w, r, sharedauth.PermTicketsRead)
	if !ok {
		return
	}
	runs, err := s.Store.ListRuns(r.Context(), id.OrgID, tid)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]apigen.AgentRun, 0, len(runs))
	for _, x := range runs {
		out = append(out, toRun(x))
	}
	writeJSON(w, 200, list(out))
}

func (s *Server) ListRunTurns(w http.ResponseWriter, r *http.Request, runID apigen.RunId) {
	id, ok := s.caller(w, r, sharedauth.PermTicketsRead)
	if !ok {
		return
	}
	turns, found, err := s.Store.ListTurns(r.Context(), id.OrgID, runID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !found {
		writeErr(w, 404, "not_found", "run not found")
		return
	}
	out := make([]apigen.AgentTurn, 0, len(turns))
	for _, t := range turns {
		tc := []apigen.ToolCallSummary{}
		for _, c := range t.ToolCalls {
			sum := c.Summary
			tc = append(tc, apigen.ToolCallSummary{Name: c.Name, Summary: &sum})
		}
		out = append(out, apigen.AgentTurn{Id: t.ID, RunId: t.RunID, N: t.N, Model: t.Model, TokensIn: t.TokensIn, TokensOut: t.TokensOut,
			TokensCached: t.TokensCached, CostUsd: t.CostUSD, ToolCalls: tc, TranscriptRef: t.TranscriptRef, CreatedAt: t.CreatedAt})
	}
	writeJSON(w, 200, list(out))
}

func (s *Server) ListTicketGateResults(w http.ResponseWriter, r *http.Request, tid apigen.TicketId, p apigen.ListTicketGateResultsParams) {
	id, ok := s.caller(w, r, sharedauth.PermTicketsRead)
	if !ok {
		return
	}
	res, err := s.Store.ListGateResults(r.Context(), id.OrgID, tid, p.RunId)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]apigen.GateResult, 0, len(res))
	for _, g := range res {
		fs := []apigen.GateFinding{}
		for _, f := range g.Findings {
			sev := apigen.GateFindingSeverity(f.Severity)
			fs = append(fs, apigen.GateFinding{Message: f.Message, File: strPtr(f.File), Line: intPtr(f.Line), Column: intPtr(f.Column),
				Rule: strPtr(f.Rule), Severity: &sev, Suggestion: strPtr(f.Suggestion)})
		}
		created := g.CreatedAt
		out = append(out, apigen.GateResult{Id: g.ID, RunId: g.RunID, TicketId: g.TicketID, Iteration: g.Iteration, GateId: g.GateID,
			Status: apigen.GateStatus(g.Status), DurationMs: g.DurationMs, Findings: fs, ArtifactsRef: g.ArtifactsRef, CreatedAt: &created})
	}
	writeJSON(w, 200, list(out))
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// ListTicketArtifacts serves repro evidence; URLs come from s.Artifacts.
func (s *Server) ListTicketArtifacts(w http.ResponseWriter, r *http.Request, tid apigen.TicketId) {
	id, ok := s.caller(w, r, sharedauth.PermTicketsRead)
	if !ok {
		return
	}
	if _, err := s.Store.GetTicket(r.Context(), id.OrgID, tid); err != nil {
		s.fail(w, err)
		return
	}
	arts, err := s.Store.ListArtifacts(r.Context(), id.OrgID, tid)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]apigen.Artifact, 0, len(arts))
	for _, a := range arts {
		if a.Kind == "transcript" { // transcripts are shown per turn, not as repro evidence
			continue
		}
		url := ""
		if s.Artifacts != nil {
			if url, err = s.Artifacts.URL(r.Context(), a.S3Key); err != nil {
				s.fail(w, err)
				return
			}
		}
		ct := "application/octet-stream"
		if a.ContentType != nil {
			ct = *a.ContentType
		}
		out = append(out, apigen.Artifact{Id: a.ID, TicketId: a.TicketID, Kind: apigen.ArtifactKind(a.Kind), Label: artifactLabel(a),
			Url: url, ContentType: ct, CreatedAt: a.CreatedAt})
	}
	writeJSON(w, 200, list(out))
}

func artifactLabel(a ticketrepo.Artifact) string {
	key := a.S3Key
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '/' {
			return key[i+1:]
		}
	}
	return key
}

func toReviewFindings(fs []ticketrepo.ReviewFinding) []apigen.ReviewFinding {
	out := make([]apigen.ReviewFinding, 0, len(fs))
	for _, f := range fs {
		sev := apigen.ReviewFindingSeverity(f.Severity)
		switch sev {
		case "info", "minor", "major", "blocker":
		default:
			sev = "minor"
		}
		out = append(out, apigen.ReviewFinding{Severity: sev, Category: f.Category, File: f.File, Message: f.Message})
	}
	return out
}

// GetTicketReview serves the latest reviewer verdict (persisted by the orchestrator's Review activity).
func (s *Server) GetTicketReview(w http.ResponseWriter, r *http.Request, tid apigen.TicketId) {
	id, ok := s.caller(w, r, sharedauth.PermTicketsRead)
	if !ok {
		return
	}
	rv, err := s.Store.LatestReview(r.Context(), id.OrgID, tid)
	if errors.Is(err, ticketrepo.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "No review yet")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, apigen.Review{Id: rv.ID, TicketId: rv.TicketID, Verdict: apigen.ReviewVerdict(rv.Verdict), Model: rv.Model,
		Findings: toReviewFindings(rv.Findings), CreatedAt: rv.CreatedAt})
}

// ---- pull requests ----

func (s *Server) ListPullRequests(w http.ResponseWriter, r *http.Request, p apigen.ListPullRequestsParams) {
	id, ok := s.caller(w, r, sharedauth.PermTicketsRead)
	if !ok {
		return
	}
	var state *string
	if p.State != nil {
		st := string(*p.State)
		state = &st
	}
	prs, err := s.Store.ListPullRequests(r.Context(), id.OrgID, state, nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]apigen.PullRequest, 0, len(prs)) // already sorted by blast radius desc
	for _, x := range prs {
		out = append(out, toPR(x))
	}
	writeJSON(w, 200, list(out))
}

func (s *Server) evidence(ctx context.Context, org, prID apigen.PullRequestId) (apigen.PullRequestEvidence, error) {
	pr, err := s.Store.GetPullRequest(ctx, org, prID)
	if err != nil {
		return apigen.PullRequestEvidence{}, err
	}
	ev := apigen.PullRequestEvidence{PullRequestId: pr.ID, Additions: deref(pr.Additions), Deletions: deref(pr.Deletions),
		Files: []apigen.PullRequestFile{}, GateSummary: gateSummary(pr), VisualDiffs: []apigen.VisualDiff{},
		HumanDecision: (*apigen.HumanDecision)(pr.HumanDecision)}
	rv, err := s.Store.LatestReview(ctx, org, pr.TicketID)
	switch {
	case err == nil:
		v := apigen.PullRequestEvidenceReviewVerdict(rv.Verdict)
		ev.ReviewVerdict = &v
	case !errors.Is(err, ticketrepo.ErrNotFound):
		return ev, err
	}
	return ev, nil
}

// GetPullRequestEvidence serves the PR inbox row detail. Per-file stats and visual diffs need the
// SCM/visual gate artifacts and are empty until those are wired.
func (s *Server) GetPullRequestEvidence(w http.ResponseWriter, r *http.Request, prID apigen.PullRequestId) {
	id, ok := s.caller(w, r, sharedauth.PermTicketsRead)
	if !ok {
		return
	}
	ev, err := s.evidence(r.Context(), id.OrgID, prID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, ev)
}

// ReviewPullRequest records a human approve / send-back on an open PR. Send-back signals the
// ticket workflow (resume with the note as PR feedback -> back to implement). The decision is
// mirrored to GitHub when a ReviewMirror is configured.
func (s *Server) ReviewPullRequest(w http.ResponseWriter, r *http.Request, prID apigen.PullRequestId) {
	var req apigen.PullRequestReviewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil ||
		(req.Action != apigen.PullRequestReviewRequestActionApprove && req.Action != apigen.PullRequestReviewRequestActionRequestChanges) {
		writeErr(w, http.StatusBadRequest, "bad_request", "action must be approve or request_changes")
		return
	}
	who, ok := s.caller(w, r, sharedauth.PermTicketsApprove)
	if !ok {
		return
	}
	ctx := r.Context()
	pr, err := s.Store.GetPullRequest(ctx, who.OrgID, prID)
	if err != nil {
		s.fail(w, err)
		return
	}
	note := ""
	if req.Note != nil {
		note = *req.Note
	}
	decision := "approved"
	if req.Action == apigen.PullRequestReviewRequestActionRequestChanges {
		decision = "changes_requested"
		if note == "" {
			writeErr(w, http.StatusBadRequest, "bad_request", "a note is required when sending a PR back")
			return
		}
	}
	err = s.Store.SetPullRequestDecision(ctx, who.OrgID, prID, decision, who.Actor(), note)
	if errors.Is(err, ticketrepo.ErrPullRequestClosed) {
		writeErr(w, http.StatusConflict, "conflict", "pull request is not open")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if decision == "changes_requested" {
		t, err := s.Store.GetTicket(ctx, who.OrgID, pr.TicketID)
		if err != nil {
			s.fail(w, err)
			return
		}
		if !s.signal(w, r, t, ticketflow.HumanDecision{Action: ticketflow.ActionResume, Note: note, Actor: who.Actor()}) {
			return
		}
	}
	if s.Reviews != nil {
		if err := s.Reviews.MirrorReview(ctx, pr, decision, note); err != nil {
			s.log().Warn("mirror PR review", "pr", pr.Number, "err", err)
		}
	}
	ev, err := s.evidence(ctx, who.OrgID, prID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, ev)
}

// ---- metrics ----

func (s *Server) GetMetrics(w http.ResponseWriter, r *http.Request, p apigen.GetMetricsParams) {
	id, ok := s.caller(w, r, sharedauth.PermMetricsRead)
	if !ok {
		return
	}
	window := apigen.N30d
	if p.Window != nil {
		window = *p.Window
	}
	days := map[apigen.MetricsWindow]int{apigen.N7d: 7, apigen.N30d: 30, apigen.N90d: 90}[window]
	if days == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "window must be 7d, 30d or 90d")
		return
	}
	since := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	m, err := s.Store.Metrics(r.Context(), id.OrgID, since, p.RepoId)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, toMetrics(window, p.RepoId, m))
}

func toMetrics(window apigen.MetricsWindow, repoID *apigen.RepoId, m ticketrepo.Metrics) apigen.Metrics {
	out := apigen.Metrics{Window: window, RepoId: repoID,
		Totals: apigen.MetricsTotals{TicketsMerged: m.TicketsMerged, TurnsP50: m.TurnsP50, TurnsP90: m.TurnsP90,
			CostPerTicketUsd: m.CostPerTicketUSD, CostTotalUsd: m.CostTotalUSD, GatePassRate: m.GatePassRate,
			EscalationRate: m.EscalationRate, LeadTimeHoursP50: m.LeadTimeHoursP50},
		Series: []apigen.MetricsPoint{}, GatePassRateByGate: []apigen.GatePassRate{}, TopFailureReasons: []apigen.GateFailureReason{}}
	for _, d := range m.Series {
		out.Series = append(out.Series, apigen.MetricsPoint{Date: dateOf(d.Date), TicketsMerged: d.TicketsMerged, TurnsP50: d.TurnsP50,
			TurnsP90: d.TurnsP90, CostUsd: d.CostUSD, GatePassRate: d.GatePassRate})
	}
	for _, g := range m.ByGate {
		out.GatePassRateByGate = append(out.GatePassRateByGate, apigen.GatePassRate{GateId: g.GateID, PassRate: g.PassRate, Runs: g.Runs})
	}
	for _, f := range m.TopFailures {
		out.TopFailureReasons = append(out.TopFailureReasons, apigen.GateFailureReason{GateId: f.GateID, Rule: f.Rule, Count: f.Count})
	}
	return out
}

// ---- SSE ----

func (s *Server) StreamEvents(w http.ResponseWriter, r *http.Request, p apigen.StreamEventsParams) {
	id, ok := s.caller(w, r, sharedauth.PermTicketsRead)
	if !ok {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok || s.Events == nil {
		writeErr(w, http.StatusInternalServerError, "no_stream", "streaming unsupported")
		return
	}
	org := id.OrgID
	ch, closeFn, err := s.Events.Subscribe(r.Context(), org.String())
	if err != nil {
		s.fail(w, err)
		return
	}
	defer closeFn()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	ping := s.SSEPing
	if ping <= 0 {
		ping = 15 * time.Second
	}
	t := time.NewTicker(ping)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.OrgID != org.String() || (p.RepoId != nil && ev.RepoID != p.RepoId.String()) {
				continue
			}
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
			fl.Flush()
		}
	}
}

// ---- webhooks (public; verified by signature) ----

func (s *Server) ack(w http.ResponseWriter, res webhook.Result) {
	if res.HTTPStatus == http.StatusAccepted {
		writeJSON(w, 202, apigen.WebhookAck{DeliveryId: res.DeliveryID, Status: apigen.WebhookAckStatus(res.Status)})
		return
	}
	writeErr(w, res.HTTPStatus, res.Status, res.Message)
}

func (s *Server) ReceiveGithubWebhook(w http.ResponseWriter, r *http.Request, _ apigen.ReceiveGithubWebhookParams) {
	s.ack(w, s.Webhooks.GitHub(w, r))
}

func (s *Server) ReceiveSentryWebhook(w http.ResponseWriter, r *http.Request, _ apigen.ReceiveSentryWebhookParams) {
	s.ack(w, s.Webhooks.Sentry(w, r))
}
