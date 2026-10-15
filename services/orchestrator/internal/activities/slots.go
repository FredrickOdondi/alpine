package activities

import (
	"context"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

func slotKeys(ref Ref) (string, string) {
	return "alpine:sem:tickets:org:" + ref.OrgID, "alpine:sem:tickets:repo:" + ref.RepoID
}

func (a *Activities) lease() time.Duration {
	if a.SlotLease > 0 {
		return a.SlotLease
	}
	return 3 * time.Hour
}

// AcquireTicketSlot blocks (heartbeating) until the ticket gets an implementation slot within
// its org's and repo's max_parallel_tickets. Leases expire, so a dead workflow frees its slot.
func (a *Activities) AcquireTicketSlot(ctx context.Context, in SlotInput) error {
	if a.Slots == nil {
		return nil
	}
	orgKey, repoKey := slotKeys(in.Ref)
	poll := a.SlotPoll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for {
		okOrg, err := a.Slots.TryAcquire(ctx, orgKey, in.Ref.TicketID, in.OrgLimit, a.lease())
		if err != nil {
			return err
		}
		if okOrg {
			okRepo, err := a.Slots.TryAcquire(ctx, repoKey, in.Ref.TicketID, in.RepoLimit, a.lease())
			if err != nil {
				return err
			}
			if okRepo {
				return nil
			}
			_ = a.Slots.Release(ctx, orgKey, in.Ref.TicketID)
		}
		if activity.IsActivity(ctx) {
			activity.RecordHeartbeat(ctx, "waiting for ticket slot")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// RefreshTicketSlot extends the slot lease; a lost lease is reported so the workflow re-acquires.
func (a *Activities) RefreshTicketSlot(ctx context.Context, in SlotInput) error {
	if a.Slots == nil {
		return nil
	}
	orgKey, repoKey := slotKeys(in.Ref)
	for _, k := range []string{orgKey, repoKey} {
		ok, err := a.Slots.Refresh(ctx, k, in.Ref.TicketID, a.lease())
		if err != nil {
			return err
		}
		if !ok {
			return temporal.NewNonRetryableApplicationError("ticket slot lease lost", ErrTypeSlotLost, nil)
		}
	}
	return nil
}

// ReleaseTicketSlot frees the slot (idempotent).
func (a *Activities) ReleaseTicketSlot(ctx context.Context, in SlotInput) error {
	if a.Slots == nil {
		return nil
	}
	orgKey, repoKey := slotKeys(in.Ref)
	if err := a.Slots.Release(ctx, orgKey, in.Ref.TicketID); err != nil {
		return err
	}
	return a.Slots.Release(ctx, repoKey, in.Ref.TicketID)
}
