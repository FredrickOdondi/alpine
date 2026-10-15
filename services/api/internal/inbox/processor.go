// Package inbox is the inbox processor: it drains inbox_events, normalizes them into
// intake.TicketIntake, creates tickets (deduped) and starts/signals their Temporal workflows.
// Every step is idempotent so a crash at any point is repaired by reprocessing the row.
package inbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/alpine-dev/alpine/packages/shared/budget"
	"github.com/alpine-dev/alpine/packages/shared/events"
	"github.com/alpine-dev/alpine/packages/shared/intake"
	"github.com/alpine-dev/alpine/packages/shared/ticket"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
	"github.com/alpine-dev/alpine/services/api/internal/store"
	"github.com/alpine-dev/alpine/services/api/internal/workflows"
	"github.com/alpine-dev/alpine/services/integrations/intakemeta"
)

// ticketNS namespaces deterministic ticket ids (uuid v5).
var ticketNS = uuid.MustParse("5b0c1f9e-3f5e-4c43-9d55-2f5d2f2b7a10")

// TicketID is the deterministic ticket id for (org, source, dedupe key): the same problem
// reported twice (a GitHub redelivery under a new delivery id, a repeat Sentry event) maps to the
// same ticket, so CreateTicket and the workflow start are naturally idempotent.
func TicketID(org uuid.UUID, src intake.Source, dedupeKey string) uuid.UUID {
	return uuid.NewSHA1(ticketNS, []byte(org.String()+"|"+string(src)+"|"+dedupeKey))
}

// Processor drains the inbox.
type Processor struct {
	Store      store.Inbox
	Sentry     store.SentryRoutes
	Normalizer intake.Registry
	Engine     workflows.Engine
	Events     events.Publisher
	Logger     *slog.Logger

	Caps                   budget.TicketCaps
	MaxParallelTicketsOrg  int
	MaxParallelTicketsRepo int

	Interval    time.Duration // poll interval (default 2s)
	Batch       int           // rows per poll (default 50)
	Concurrency int           // parallel rows (default 4)

	wake chan struct{}
	once sync.Once

	processed metric.Int64Counter
}

func (p *Processor) init() {
	p.once.Do(func() {
		p.wake = make(chan struct{}, 1)
		if p.Logger == nil {
			p.Logger = slog.Default()
		}
		if p.Events == nil {
			p.Events = events.Nop{}
		}
		p.processed, _ = otel.Meter("alpine/inbox").Int64Counter("alpine.inbox.events", metric.WithDescription("inbox events processed by outcome"))
	})
}

// Notify wakes the processor (called by the webhook receiver after an insert).
func (p *Processor) Notify() {
	p.init()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run polls until ctx is cancelled.
func (p *Processor) Run(ctx context.Context) {
	p.init()
	interval := p.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := p.Drain(ctx); err != nil && ctx.Err() == nil {
			p.Logger.Error("inbox drain", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.wake:
		}
	}
}

// Drain processes one batch of pending rows and returns how many it handled.
func (p *Processor) Drain(ctx context.Context) (int, error) {
	p.init()
	batch := p.Batch
	if batch <= 0 {
		batch = 50
	}
	pending, err := p.Store.PendingInbox(ctx, batch)
	if err != nil {
		return 0, err
	}
	conc := max(1, p.Concurrency)
	if p.Concurrency == 0 {
		conc = 4
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, item := range pending {
		sem <- struct{}{}
		wg.Add(1)
		go func(item ticketrepo.PendingInbox) {
			defer wg.Done()
			defer func() { <-sem }()
			p.processOne(ctx, item)
		}(item)
	}
	wg.Wait()
	return len(pending), nil
}

func (p *Processor) processOne(ctx context.Context, item ticketrepo.PendingInbox) {
	ev, err := p.Store.LoadInboxEvent(ctx, item.OrgID, item.ID)
	if err != nil {
		p.Logger.Error("load inbox event", "id", item.ID, "err", err)
		return
	}
	outcome, dedupe, err := p.process(ctx, ev)
	log := p.Logger.With("inbox_id", ev.ID, "org", ev.OrgID, "source", ev.Source, "event", ev.EventType, "delivery", ev.DeliveryID)
	if err != nil {
		log.Warn("inbox event failed; will retry", "err", err, "attempt", ev.Attempts+1)
		_ = p.Store.MarkInbox(ctx, ev.OrgID, ev.ID, "", dedupe, err.Error())
		p.processed.Add(ctx, 1, metric.WithAttributes())
		return
	}
	log.Info("inbox event processed", "outcome", outcome)
	if err := p.Store.MarkInbox(ctx, ev.OrgID, ev.ID, outcome, dedupe, ""); err != nil {
		log.Error("mark inbox", "err", err)
	}
	p.processed.Add(ctx, 1)
}

// Process handles one event and returns its outcome. A returned error means retry.
func (p *Processor) Process(ctx context.Context, ev ticketrepo.InboxEvent) (string, error) {
	outcome, _, err := p.process(ctx, ev)
	return outcome, err
}

// process also returns the issue-level dedupe key of the first intake (inbox_events.dedupe_key).
func (p *Processor) process(ctx context.Context, ev ticketrepo.InboxEvent) (outcome, dedupe string, err error) {
	p.init()
	intakes, err := p.Normalizer.Normalize(ctx, intake.Source(ev.Source), ev.EventType, ev.Payload)
	if errors.Is(err, intake.ErrIgnored) {
		return "ignored", "", nil
	}
	if err != nil {
		// Malformed payloads will never succeed; record them as rejected instead of retrying.
		return "rejected: " + truncate(err.Error(), 200), "", nil
	}
	var outcomes []string
	for _, in := range intakes {
		in.DeliveryID = ev.DeliveryID
		if in.Source == "" {
			in.Source = intake.Source(ev.Source)
		}
		if dedupe == "" {
			dedupe = dedupeKey(in)
		}
		o, err := p.apply(ctx, ev, in)
		if err != nil {
			return "", dedupe, err
		}
		outcomes = append(outcomes, o)
	}
	if len(outcomes) == 0 {
		return "ignored", dedupe, nil
	}
	return strings.Join(outcomes, ";"), dedupe, nil
}

func dedupeKey(in intake.TicketIntake) string {
	if in.DedupeKey != "" {
		return in.DedupeKey
	}
	return in.ExternalID
}

func (p *Processor) apply(ctx context.Context, ev ticketrepo.InboxEvent, in intake.TicketIntake) (string, error) {
	org := ev.OrgID
	switch in.Action {
	case intake.ActionCreate:
		return p.create(ctx, ev, in)
	case intake.ActionClose:
		return p.signal(ctx, org, TicketID(org, in.Source, dedupeKey(in)), ticketflow.ActionClose, "closed at "+string(in.Source))
	case intake.ActionPRMerged:
		ulid, _, ok := strings.Cut(strings.TrimPrefix(in.Metadata[MetaHeadBranch], "alpine/"), "-")
		tid, err := ticketflow.ParseTicketULID(ulid)
		if !ok || err != nil {
			return "ignored:pr_branch", nil
		}
		return p.signal(ctx, org, tid, ticketflow.ActionMarkMerged, fmt.Sprintf("PR #%d merged", in.Number))
	default:
		// update/reopen/comment: carried in the inbox for triage context; Phase 1 feeds them to
		// the running workflow.
		return "ignored:" + string(in.Action), nil
	}
}

func (p *Processor) create(ctx context.Context, ev ticketrepo.InboxEvent, in intake.TicketIntake) (string, error) {
	org := ev.OrgID
	if in.Metadata[intakemeta.AuthorIsBot] == "true" {
		return "ignored:bot_author", nil // never turn our own (or another bot's) issues into tickets
	}
	key := dedupeKey(in)
	if key == "" {
		return "rejected:no_external_id", nil
	}
	var repoID uuid.UUID
	switch in.Source {
	case intake.SourceSentry:
		// Sentry is not repo-scoped: map the project to a repo (sentry_project -> repo).
		if p.Sentry == nil || in.SentryProject == "" {
			return "ignored:unmapped_sentry_project", nil
		}
		id, ok, err := p.Sentry.RepoForProject(ctx, org, in.SentryProject)
		if err != nil {
			return "", err
		}
		if !ok {
			return "ignored:unmapped_sentry_project", nil
		}
		repoID = id
	default:
		id, err := p.Store.UpsertRepoForIntake(ctx, org, in.Repo)
		if errors.Is(err, ticketrepo.ErrNotFound) {
			return "ignored:unknown_installation", nil
		}
		if err != nil {
			return "", err
		}
		repoID = id
	}

	tid := TicketID(org, in.Source, key)
	wfID := ticketflow.WorkflowID(org.String(), tid.String())
	created, err := p.Store.CreateTicket(ctx, ticketrepo.NewTicket{
		ID: tid, OrgID: org, RepoID: repoID, Key: ticketKey(in, tid), Title: truncate(in.Title, 500), Body: in.Body,
		Source: string(in.Source), SourceURL: in.ExternalURL, ExternalID: in.ExternalID, InboxEventID: ev.ID,
		WorkflowID: wfID, Caps: p.Caps,
	})
	if err != nil {
		return "", err
	}
	// Start even when the ticket already existed: a crash between insert and start is repaired
	// here, and an existing workflow id is success.
	started, err := p.Engine.StartTicket(ctx, ticketflow.Input{
		OrgID: org.String(), TicketID: tid.String(), RepoID: repoID.String(), Caps: p.Caps,
		MaxParallelTicketsOrg: p.MaxParallelTicketsOrg, MaxParallelTicketsRepo: p.MaxParallelTicketsRepo,
	})
	if err != nil {
		return "", fmt.Errorf("start workflow: %w", err)
	}
	if created {
		_ = p.Events.Publish(ctx, events.BoardEvent{Type: events.TicketCreated, OrgID: org.String(), TicketID: tid.String(),
			RepoID: repoID.String(), Stage: string(ticket.StageIntake), Reason: "created from " + string(in.Source), At: time.Now().UTC()})
	}
	switch {
	case created:
		return "ticket_created:" + tid.String(), nil
	case started:
		return "workflow_started:" + tid.String(), nil
	default:
		return "duplicate:" + tid.String(), nil
	}
}

func (p *Processor) signal(ctx context.Context, org, tid uuid.UUID, action, note string) (string, error) {
	t, err := p.Store.GetTicket(ctx, org, tid)
	if errors.Is(err, ticketrepo.ErrNotFound) {
		return "ignored:no_ticket", nil
	}
	if err != nil {
		return "", err
	}
	if t.WorkflowID == nil || t.Stage.Terminal() {
		return "ignored:terminal", nil
	}
	err = p.Engine.Signal(ctx, *t.WorkflowID, ticketflow.HumanDecision{Action: action, Note: note, Actor: "integration:github"})
	if workflows.IsNotFound(err) {
		return "ignored:workflow_closed", nil
	}
	if err != nil {
		return "", err
	}
	return action + ":" + tid.String(), nil
}

func ticketKey(in intake.TicketIntake, tid uuid.UUID) string {
	if in.Source == intake.SourceGitHub && in.Number > 0 {
		return fmt.Sprintf("GH-%d", in.Number)
	}
	return strings.ToUpper(string(in.Source)) + "-" + strings.ToUpper(tid.String()[:6])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
