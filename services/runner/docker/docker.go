// Package docker is the local-dev runner.Runner: one container per sandbox via the docker CLI.
// No network by default, resource-limited, labelled for reaping. Not a security boundary for
// hostile code; production uses E2B microVMs.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alpine-dev/alpine/services/runner"
)

// Execer runs the docker binary; swapped in tests.
type Execer func(ctx context.Context, stdin []byte, stdout, stderr *runner.CappedBuffer, args ...string) (exitCode int, err error)

// Runner creates containers with the docker CLI.
type Runner struct {
	Bin          string // default "docker"
	DefaultImage string // default "node:22-bookworm-slim"
	Exec         Execer
}

func (r *Runner) Name() string { return "docker" }

func (r *Runner) bin() string {
	if r.Bin != "" {
		return r.Bin
	}
	return "docker"
}

func (r *Runner) run(ctx context.Context, stdin []byte, args ...string) (runner.ExecResult, error) {
	stdout, stderr := &runner.CappedBuffer{}, &runner.CappedBuffer{}
	start := time.Now()
	ex := r.Exec
	if ex == nil {
		ex = r.cli
	}
	code, err := ex(ctx, stdin, stdout, stderr, args...)
	return runner.ExecResult{ExitCode: code, Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Duration: time.Since(start),
		Truncated: stdout.Truncated || stderr.Truncated}, err
}

func (r *Runner) cli(ctx context.Context, stdin []byte, stdout, stderr *runner.CappedBuffer, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, r.bin(), args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// CreateArgs builds the `docker run` arguments for spec (exported for tests).
func (r *Runner) CreateArgs(spec runner.Spec) []string {
	image := spec.Template
	if image == "" {
		image = r.DefaultImage
	}
	if image == "" {
		image = "node:22-bookworm-slim"
	}
	args := []string{"run", "-d", "--rm", "--init", "--label", "alpine.sandbox=1",
		"--security-opt", "no-new-privileges", "--cap-drop", "ALL", "--pids-limit", "512"}
	if !spec.Network {
		args = append(args, "--network", "none")
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%g", spec.CPUs))
	}
	if spec.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", spec.MemoryMB))
	}
	if spec.Workdir != "" {
		args = append(args, "--workdir", spec.Workdir)
	}
	for _, k := range sortedKeys(spec.Labels) {
		args = append(args, "--label", "alpine."+k+"="+spec.Labels[k])
	}
	for _, k := range sortedKeys(spec.Env) {
		args = append(args, "--env", k+"="+spec.Env[k])
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = time.Hour
	}
	// The container exits (and --rm removes it) when the lifetime elapses, like E2B's timeout.
	return append(args, image, "sleep", fmt.Sprintf("%d", int(timeout.Seconds())))
}

// Create starts a container.
func (r *Runner) Create(ctx context.Context, spec runner.Spec) (runner.Sandbox, error) {
	res, err := r.run(ctx, nil, r.CreateArgs(spec)...)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("docker run: exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	id := strings.TrimSpace(string(res.Stdout))
	if id == "" {
		return nil, errors.New("docker run: no container id")
	}
	return &Sandbox{r: r, id: id, workdir: spec.Workdir}, nil
}

// Connect re-attaches to a running container.
func (r *Runner) Connect(ctx context.Context, id string) (runner.Sandbox, error) {
	res, err := r.run(ctx, nil, "inspect", "-f", "{{.State.Running}}", id)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 || strings.TrimSpace(string(res.Stdout)) != "true" {
		return nil, runner.ErrNotFound
	}
	return &Sandbox{r: r, id: id}, nil
}

// Sandbox is one container.
type Sandbox struct {
	r       *Runner
	id      string
	workdir string
	once    sync.Once
	err     error
}

func (s *Sandbox) ID() string { return s.id }

// Exec runs `docker exec`.
func (s *Sandbox) Exec(ctx context.Context, c runner.Command) (runner.ExecResult, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	args := []string{"exec"}
	if c.Stdin != nil {
		args = append(args, "-i")
	}
	dir := c.Dir
	if dir == "" {
		dir = s.workdir
	}
	if dir != "" {
		args = append(args, "-w", dir)
	}
	for _, k := range sortedKeys(c.Env) {
		args = append(args, "-e", k+"="+c.Env[k])
	}
	args = append(append(args, s.id, c.Cmd), c.Args...)
	return s.r.run(ctx, c.Stdin, args...)
}

// WriteFile streams data into the container through stdin.
func (s *Sandbox) WriteFile(ctx context.Context, p string, data []byte) error {
	res, err := s.r.run(ctx, data, "exec", "-i", s.id, "sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", path.Clean(p))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("docker write %s: %s", p, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// ReadFile cats a file out of the container.
func (s *Sandbox) ReadFile(ctx context.Context, p string) ([]byte, error) {
	stdout, stderr := &runner.CappedBuffer{Limit: 64 << 20}, &runner.CappedBuffer{}
	ex := s.r.Exec
	if ex == nil {
		ex = s.r.cli
	}
	code, err := ex(ctx, nil, stdout, stderr, "exec", s.id, "cat", "--", path.Clean(p))
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("docker read %s: %s: %w", p, strings.TrimSpace(string(stderr.Bytes())), runner.ErrNotFound)
	}
	return stdout.Bytes(), nil
}

// Close removes the container.
func (s *Sandbox) Close(ctx context.Context) error {
	s.once.Do(func() {
		res, err := s.r.run(ctx, nil, "rm", "-f", s.id)
		if err == nil && res.ExitCode != 0 && !strings.Contains(string(res.Stderr), "No such container") {
			err = fmt.Errorf("docker rm: %s", strings.TrimSpace(string(res.Stderr)))
		}
		s.err = err
	})
	return s.err
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var _ runner.Runner = (*Runner)(nil)
