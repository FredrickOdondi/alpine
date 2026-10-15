// Command runner-smoke creates a sandbox with the configured backend, writes and reads a file,
// runs a command and tears it down. `make runner-smoke` (Docker by default; RUNNER=e2b with
// E2B_API_KEY uses E2B, which is a real paid call).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/alpine-dev/alpine/services/runner"
	"github.com/alpine-dev/alpine/services/runner/selectrunner"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "runner-smoke:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, err := selectrunner.FromEnv()
	if err != nil {
		return err
	}
	tmpl := os.Getenv("RUNNER_TEMPLATE")
	if tmpl == "" && r.Name() == "e2b" {
		tmpl = "base"
	}
	sb, err := r.Create(ctx, runner.Spec{Template: tmpl, Timeout: 5 * time.Minute, Workdir: "/tmp",
		Labels: map[string]string{"purpose": "smoke"}})
	if err != nil {
		return err
	}
	defer sb.Close(context.Background())
	fmt.Printf("%s sandbox %s\n", r.Name(), sb.ID())
	if err := sb.WriteFile(ctx, "/tmp/alpine/hello.txt", []byte("hello from alpine\n")); err != nil {
		return err
	}
	res, err := sb.Exec(ctx, runner.Command{Cmd: "sh", Args: []string{"-c", "cat /tmp/alpine/hello.txt && uname -a"}})
	if err != nil {
		return err
	}
	fmt.Printf("exit=%d (%s)\n%s%s", res.ExitCode, res.Duration.Round(time.Millisecond), res.Stdout, res.Stderr)
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d", res.ExitCode)
	}
	return nil
}
