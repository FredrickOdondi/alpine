// Package events fans out live board updates over Redis pub/sub: the orchestrator (and the inbox
// processor) publish, the API's SSE endpoint subscribes per org. Channels are per tenant.
package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// Type of a board event (OpenAPI BoardEventType).
type Type string

const (
	TicketCreated      Type = "ticket.created"
	TicketStageChanged Type = "ticket.stage_changed"
	TicketUpdated      Type = "ticket.updated"
)

// BoardEvent mirrors the OpenAPI BoardEvent schema.
type BoardEvent struct {
	Type          Type      `json:"type"`
	OrgID         string    `json:"orgId"`
	TicketID      string    `json:"ticketId"`
	RepoID        string    `json:"repoId"`
	Stage         string    `json:"stage"`
	PreviousStage string    `json:"previousStage,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	At            time.Time `json:"at"`
}

// Publisher publishes board events.
type Publisher interface {
	Publish(ctx context.Context, ev BoardEvent) error
}

// Channel returns the Redis channel for an org.
func Channel(orgID string) string { return "alpine:board:" + orgID }

// Redis is a Redis-backed publisher/subscriber.
type Redis struct{ Client *redis.Client }

// Publish implements Publisher.
func (r Redis) Publish(ctx context.Context, ev BoardEvent) error {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return r.Client.Publish(ctx, Channel(ev.OrgID), raw).Err()
}

// Subscribe streams events for one org until ctx ends.
func (r Redis) Subscribe(ctx context.Context, orgID string) (<-chan BoardEvent, func(), error) {
	sub := r.Client.Subscribe(ctx, Channel(orgID))
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, err
	}
	out := make(chan BoardEvent, 64)
	go func() {
		defer close(out)
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				var ev BoardEvent
				if json.Unmarshal([]byte(msg.Payload), &ev) == nil && ev.OrgID == orgID {
					select {
					case out <- ev:
					default: // slow consumer: drop; clients re-fetch on any event anyway
					}
				}
			}
		}
	}()
	return out, func() { _ = sub.Close() }, nil
}

// Nop discards events.
type Nop struct{}

// Publish implements Publisher.
func (Nop) Publish(context.Context, BoardEvent) error { return nil }
