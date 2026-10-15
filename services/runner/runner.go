// Package runner abstracts the per-run sandbox where implementer agents and quality gates execute
// untrusted code. Production uses E2B (Firecracker microVMs); local dev uses Docker. One sandbox
// per run, never shared across tickets or orgs.
package runner

import (
	"context"
	"errors"
	"time"
)

// Spec describes a sandbox to create.
type Spec struct {
	// Template is the E2B template id or the Docker image.
	Template string
	// Timeout bounds the sandbox lifetime (E2B auto-kills after it).
	Timeout time.Duration
	Env     map[string]string
	// Labels are attached as metadata (org_id, ticket_id, run_id) for cost attribution and reaping.
	Labels   map[string]string
	CPUs     float64
	MemoryMB int
	// Network enables egress. Off by default for the Docker runner; E2B egress is policy-controlled.
	Network bool
	// Workdir is the default working directory for Exec.
	Workdir string
}

// Command is one process execution.
type Command struct {
	Cmd     string
	Args    []string
	Dir     string
	Env     map[string]string
	Timeout time.Duration
	Stdin   []byte
}

// ExecResult is the outcome of a Command. A non-zero ExitCode is not an error.
type ExecResult struct {
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Duration  time.Duration
	Truncated bool
}

// Sandbox is a live, isolated environment.
type Sandbox interface {
	ID() string
	Exec(ctx context.Context, c Command) (ExecResult, error)
	WriteFile(ctx context.Context, path string, data []byte) error
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// Close destroys the sandbox. Safe to call more than once.
	Close(ctx context.Context) error
}

// Runner creates and reconnects to sandboxes.
type Runner interface {
	Name() string
	Create(ctx context.Context, spec Spec) (Sandbox, error)
	// Connect re-attaches to a sandbox created earlier (activity retry/resume).
	Connect(ctx context.Context, id string) (Sandbox, error)
}

// MaxOutputBytes caps captured stdout/stderr per stream to keep turn records bounded.
const MaxOutputBytes = 1 << 20

// ErrNotFound is returned by Connect when the sandbox no longer exists.
var ErrNotFound = errors.New("runner: sandbox not found")

// CappedBuffer accumulates up to Limit bytes and records truncation.
type CappedBuffer struct {
	Limit     int
	buf       []byte
	Truncated bool
}

// Write implements io.Writer, never failing.
func (b *CappedBuffer) Write(p []byte) (int, error) {
	limit := b.Limit
	if limit <= 0 {
		limit = MaxOutputBytes
	}
	room := limit - len(b.buf)
	if room <= 0 {
		b.Truncated = b.Truncated || len(p) > 0
		return len(p), nil
	}
	if len(p) > room {
		b.buf = append(b.buf, p[:room]...)
		b.Truncated = true
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// Bytes returns the captured bytes.
func (b *CappedBuffer) Bytes() []byte { return b.buf }
