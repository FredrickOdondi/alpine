// Package store declares the small repository interfaces the API depends on. Identity, orgs,
// memberships, API keys and audit come from jeff/data's sqlc repos (packages/shared/repo, used
// via internal/authapi); the ticket pipeline tables are served by packages/shared/ticketrepo.
package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/intake"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
)

// Reader serves the read API. All methods are tenant-scoped (RLS).
type Reader interface {
	ListRepos(ctx context.Context, org uuid.UUID) ([]ticketrepo.Repo, error)
	GetRepo(ctx context.Context, org, id uuid.UUID) (ticketrepo.Repo, error)
	LatestFactoryConfig(ctx context.Context, org, repoID uuid.UUID) (ticketrepo.FactoryConfigRow, error)
	ListTickets(ctx context.Context, org uuid.UUID, f ticketrepo.TicketFilter) ([]ticketrepo.Ticket, error)
	GetTicket(ctx context.Context, org, id uuid.UUID) (ticketrepo.Ticket, error)
	StageHistory(ctx context.Context, org, ticketID uuid.UUID) ([]ticketrepo.StageTransition, error)
	ListRuns(ctx context.Context, org, ticketID uuid.UUID) ([]ticketrepo.Run, error)
	ListTurns(ctx context.Context, org, runID uuid.UUID) ([]ticketrepo.Turn, bool, error)
	ListGateResults(ctx context.Context, org, ticketID uuid.UUID, runID *uuid.UUID) ([]ticketrepo.GateResult, error)
	ListArtifacts(ctx context.Context, org, ticketID uuid.UUID) ([]ticketrepo.Artifact, error)
	LatestReview(ctx context.Context, org, ticketID uuid.UUID) (ticketrepo.Review, error)
	ListPullRequests(ctx context.Context, org uuid.UUID, state *string, ticketID *uuid.UUID) ([]ticketrepo.PullRequest, error)
	GetPullRequest(ctx context.Context, org, id uuid.UUID) (ticketrepo.PullRequest, error)
	Metrics(ctx context.Context, org uuid.UUID, since time.Time, repoID *uuid.UUID) (ticketrepo.Metrics, error)
}

// Writer is the small set of API-initiated writes (human decisions).
type Writer interface {
	SetPullRequestDecision(ctx context.Context, org, id uuid.UUID, decision, actor, note string) error
}

// Store is Reader + Writer.
type Store interface {
	Reader
	Writer
}

// Inbox is what the webhook receiver and inbox processor need.
type Inbox interface {
	ResolveInstallation(ctx context.Context, provider string, installationID int64) (uuid.UUID, bool, error)
	InsertInboxEvent(ctx context.Context, org uuid.UUID, source, deliveryID, eventType, action string, payload []byte) (bool, error)
	PendingInbox(ctx context.Context, limit int) ([]ticketrepo.PendingInbox, error)
	LoadInboxEvent(ctx context.Context, org, id uuid.UUID) (ticketrepo.InboxEvent, error)
	MarkInbox(ctx context.Context, org, id uuid.UUID, outcome, dedupeKey, errMsg string) error
	UpsertRepoForIntake(ctx context.Context, org uuid.UUID, ref intake.RepoRef) (uuid.UUID, error)
	CreateTicket(ctx context.Context, t ticketrepo.NewTicket) (bool, error)
	GetTicket(ctx context.Context, org, id uuid.UUID) (ticketrepo.Ticket, error)
	ListRepos(ctx context.Context, org uuid.UUID) ([]ticketrepo.Repo, error)
}

// SentryRoutes resolves Sentry deliveries to tenants and repos. ticketrepo.Store implements it
// on sentry_installations / sentry_project_repos (migration 00008).
type SentryRoutes interface {
	OrgForInstallation(ctx context.Context, installationUUID string) (uuid.UUID, bool, error)
	RepoForProject(ctx context.Context, org uuid.UUID, project string) (uuid.UUID, bool, error)
}

var (
	_ Store        = (*ticketrepo.Store)(nil)
	_ Inbox        = (*ticketrepo.Store)(nil)
	_ SentryRoutes = (*ticketrepo.Store)(nil)
)
