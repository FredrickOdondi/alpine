package docker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alpine-dev/alpine/services/runner"
)

func TestCreateArgsAreLockedDown(t *testing.T) {
	r := &Runner{}
	args := strings.Join(r.CreateArgs(runner.Spec{Labels: map[string]string{"org_id": "o1"}, Env: map[string]string{"B": "2", "A": "1"},
		CPUs: 2, MemoryMB: 2048, Timeout: 10 * time.Minute, Workdir: "/work"}), " ")
	for _, want := range []string{"--network none", "--cap-drop ALL", "no-new-privileges", "--cpus 2", "--memory 2048m",
		"--label alpine.org_id=o1", "--env A=1 --env B=2", "node:22-bookworm-slim sleep 600", "--workdir /work"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	m := strings.Join(r.CreateArgs(runner.Spec{User: "1000:1000", Mounts: []runner.Mount{{Source: "/tmp/w", Target: "/work"}, {Source: "/c", Target: "/cache", ReadOnly: true}}}), " ")
	for _, want := range []string{"--user 1000:1000", "--mount type=bind,source=/tmp/w,target=/work", "--mount type=bind,source=/c,target=/cache,readonly"} {
		if !strings.Contains(m, want) {
			t.Errorf("missing %q in %s", want, m)
		}
	}
	if strings.Contains(strings.Join(r.CreateArgs(runner.Spec{Network: true}), " "), "--network none") {
		t.Error("network should be enabled when requested")
	}
}

func TestLifecycleWithFakeCLI(t *testing.T) {
	var calls [][]string
	files := map[string][]byte{}
	r := &Runner{Exec: func(_ context.Context, stdin []byte, stdout, stderr *runner.CappedBuffer, args ...string) (int, error) {
		calls = append(calls, args)
		switch {
		case args[0] == "run":
			stdout.Write([]byte("cid123\n"))
		case args[0] == "exec" && args[1] == "-i" && args[3] == "sh":
			files[args[len(args)-1]] = stdin
		case args[0] == "exec" && args[2] == "cat":
			b, ok := files[args[4]]
			if !ok {
				stderr.Write([]byte("No such file"))
				return 1, nil
			}
			stdout.Write(b)
		case args[0] == "exec":
			stdout.Write([]byte("out"))
			return 2, nil
		}
		return 0, nil
	}}
	ctx := context.Background()
	sb, err := r.Create(ctx, runner.Spec{Workdir: "/w"})
	if err != nil || sb.ID() != "cid123" {
		t.Fatal(sb, err)
	}
	res, err := sb.Exec(ctx, runner.Command{Cmd: "npm", Args: []string{"test"}, Env: map[string]string{"CI": "1"}})
	if err != nil || res.ExitCode != 2 || string(res.Stdout) != "out" {
		t.Fatalf("%+v %v", res, err)
	}
	if got := strings.Join(calls[1], " "); got != "exec -w /w -e CI=1 cid123 npm test" {
		t.Fatalf("exec args: %s", got)
	}
	if err := sb.WriteFile(ctx, "/w/a/../b.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if b, err := sb.ReadFile(ctx, "/w/b.txt"); err != nil || string(b) != "hi" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := sb.ReadFile(ctx, "/nope"); err == nil {
		t.Fatal("expected error")
	}
	sb.Close(ctx)
	sb.Close(ctx)
	if last := calls[len(calls)-1]; last[0] != "rm" || len(calls) != 6 {
		t.Fatalf("close: %v", calls)
	}
}

// TestRealDocker runs only when ALPINE_TEST_DOCKER=1 and a docker daemon is reachable.
func TestRealDocker(t *testing.T) {
	if os.Getenv("ALPINE_TEST_DOCKER") != "1" {
		t.Skip("set ALPINE_TEST_DOCKER=1 to run against a real docker daemon")
	}
	r := &Runner{Bin: os.Getenv("DOCKER_BIN"), DefaultImage: "alpine:3.20"}
	ctx := context.Background()
	sb, err := r.Create(ctx, runner.Spec{Timeout: time.Minute, Workdir: "/tmp", Labels: map[string]string{"test": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close(ctx)
	if err := sb.WriteFile(ctx, "/tmp/x/hello.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	res, err := sb.Exec(ctx, runner.Command{Cmd: "sh", Args: []string{"-c", "cat x/hello.txt; wget -q -T 2 http://example.com || echo nonet >&2; exit 3"}})
	if err != nil || res.ExitCode != 3 || string(res.Stdout) != "hello" || !strings.Contains(string(res.Stderr), "nonet") {
		t.Fatalf("%+v %q %q %v", res, res.Stdout, res.Stderr, err)
	}
	if _, err := r.Connect(ctx, sb.ID()); err != nil {
		t.Fatal(err)
	}
}
