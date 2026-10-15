package gates

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrGateRunCapReached is returned when a job has used all of its gate runs.
// Callers should stop the implement loop and escalate to a human.
var ErrGateRunCapReached = errors.New("gates: gate-run cap reached")

// RunCounter tracks how many gate runs each job has used. Implementations
// must make Acquire atomic per job (check-and-increment), so concurrent
// callers can never exceed the cap. The in-memory implementation is for tests
// and single-process use; the backend should persist it (e.g. the
// runs.gate_iterations column, incremented with a conditional UPDATE).
type RunCounter interface {
	// Acquire records one gate run for jobID if fewer than limit runs have
	// been used, and returns the count including this run. If the cap is
	// already reached it returns the current count and an error wrapping
	// ErrGateRunCapReached, without incrementing.
	Acquire(ctx context.Context, jobID string, limit int) (used int, err error)
	// Used returns how many gate runs jobID has used.
	Used(ctx context.Context, jobID string) (int, error)
}

// MemoryCounter is an in-memory, goroutine-safe RunCounter.
type MemoryCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

// NewMemoryCounter returns an empty MemoryCounter.
func NewMemoryCounter() *MemoryCounter { return &MemoryCounter{counts: map[string]int{}} }

// Acquire implements RunCounter.
func (c *MemoryCounter) Acquire(ctx context.Context, jobID string, limit int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = DefaultGateRunCap
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	used := c.counts[jobID]
	if used >= limit {
		return used, fmt.Errorf("%w: job %s used %d of %d gate runs", ErrGateRunCapReached, jobID, used, limit)
	}
	used++
	c.counts[jobID] = used
	return used, nil
}

// Used implements RunCounter.
func (c *MemoryCounter) Used(_ context.Context, jobID string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[jobID], nil
}
