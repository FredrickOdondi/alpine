package e2b

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alpine-dev/alpine/services/runner"
)

// fakeE2B mocks both the control plane and envd.
type fakeE2B struct {
	mu      sync.Mutex
	files   map[string][]byte
	killed  []string
	created []createReq
}

func (f *fakeE2B) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sandboxes", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "e2b_test" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var req createReq
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.created = append(f.created, req)
		f.mu.Unlock()
		tok := "envd-token"
		json.NewEncoder(w).Encode(sandboxResp{SandboxID: "sbx1", TemplateID: req.TemplateID, EnvdAccessToken: &tok})
	})
	mux.HandleFunc("GET /sandboxes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "sbx1" {
			http.Error(w, "nope", 404)
			return
		}
		tok := "envd-token"
		json.NewEncoder(w).Encode(sandboxResp{SandboxID: "sbx1", EnvdAccessToken: &tok})
	})
	mux.HandleFunc("DELETE /sandboxes/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.killed = append(f.killed, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	envdAuth := func(r *http.Request) bool {
		return r.Header.Get("X-Access-Token") == "envd-token" && r.Header.Get("E2b-Sandbox-Id") == "sbx1" && r.Header.Get("E2b-Sandbox-Port") == "49983"
	}
	mux.HandleFunc("POST /process.Process/Start", func(w http.ResponseWriter, r *http.Request) {
		if !envdAuth(r) || r.Header.Get("Content-Type") != "application/connect+json" {
			http.Error(w, "unauthorized", 401)
			return
		}
		_, payload, err := ReadFrame(bufio.NewReader(r.Body))
		if err != nil {
			t.Errorf("frame: %v", err)
			return
		}
		var req startRequest
		json.Unmarshal(payload, &req)
		w.Header().Set("Content-Type", "application/connect+json")
		send := func(flags byte, v any) {
			b, _ := json.Marshal(v)
			w.Write(EncodeFrame(flags, b))
		}
		if req.Process.Cmd == "badcwd" {
			send(2, map[string]any{"error": map[string]string{"code": "invalid_argument", "message": "cwd does not exist"}})
			return
		}
		b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
		send(0, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 7}}})
		send(0, map[string]any{"event": map[string]any{"data": map[string]any{"stdout": b64("hello " + strings.Join(req.Process.Args, " ") + "\n")}}})
		send(0, map[string]any{"event": map[string]any{"data": map[string]any{"stderr": b64("cwd=" + *req.Process.Cwd)}}})
		send(0, map[string]any{"event": map[string]any{"keepalive": map[string]any{}}})
		send(0, map[string]any{"event": map[string]any{"end": map[string]any{"exitCode": 3, "exited": true, "status": "exit status 3"}}})
		send(2, map[string]any{})
	})
	mux.HandleFunc("POST /files", func(w http.ResponseWriter, r *http.Request) {
		if !envdAuth(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b, _ := io.ReadAll(file)
		f.mu.Lock()
		f.files[r.URL.Query().Get("path")] = b
		f.mu.Unlock()
		w.Write([]byte(`[{"path":"ok"}]`))
	})
	mux.HandleFunc("GET /files", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		b, ok := f.files[r.URL.Query().Get("path")]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "not found", 404)
			return
		}
		w.Write(b)
	})
	return mux
}

func setup(t *testing.T) (*Runner, *fakeE2B) {
	f := &fakeE2B{files: map[string][]byte{}}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return &Runner{APIKey: "e2b_test", APIURL: srv.URL, EnvdURL: srv.URL, HTTP: srv.Client()}, f
}

func TestCreateExecFilesClose(t *testing.T) {
	r, f := setup(t)
	ctx := context.Background()
	sb, err := r.Create(ctx, runner.Spec{Template: "alpine-node22", Labels: map[string]string{"org_id": "o1"}, Workdir: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	if sb.ID() != "sbx1" || f.created[0].TemplateID != "alpine-node22" || !f.created[0].Secure || f.created[0].Metadata["org_id"] != "o1" {
		t.Fatalf("create: %+v", f.created)
	}
	if *f.created[0].AllowNet {
		t.Fatal("network must default off")
	}
	res, err := sb.Exec(ctx, runner.Command{Cmd: "echo", Args: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || string(res.Stdout) != "hello a b\n" || string(res.Stderr) != "cwd=/work" {
		t.Fatalf("exec: %+v %q %q", res, res.Stdout, res.Stderr)
	}
	if err := sb.WriteFile(ctx, "/work/a.txt", []byte("data")); err != nil {
		t.Fatal(err)
	}
	got, err := sb.ReadFile(ctx, "/work/a.txt")
	if err != nil || !bytes.Equal(got, []byte("data")) {
		t.Fatalf("read: %q %v", got, err)
	}
	if _, err := sb.ReadFile(ctx, "/nope"); err == nil {
		t.Fatal("expected not found")
	}
	sb.Close(ctx)
	sb.Close(ctx)
	if len(f.killed) != 1 {
		t.Fatalf("close must be idempotent: %v", f.killed)
	}
}

func TestExecRPCError(t *testing.T) {
	r, _ := setup(t)
	sb, _ := r.Create(context.Background(), runner.Spec{Template: "t"})
	_, err := sb.Exec(context.Background(), runner.Command{Cmd: "badcwd", Dir: "/x"})
	if err == nil || !strings.Contains(err.Error(), "invalid_argument") {
		t.Fatalf("got %v", err)
	}
}

func TestConnectAndAuth(t *testing.T) {
	r, _ := setup(t)
	if _, err := r.Connect(context.Background(), "gone"); err != runner.ErrNotFound {
		t.Fatalf("got %v", err)
	}
	sb, err := r.Connect(context.Background(), "sbx1")
	if err != nil || sb.ID() != "sbx1" {
		t.Fatal(err)
	}
	r.APIKey = "wrong"
	if _, err := r.Create(context.Background(), runner.Spec{}); err == nil {
		t.Fatal("expected 401")
	}
	r.APIKey = ""
	if _, err := r.Create(context.Background(), runner.Spec{}); err == nil || !strings.Contains(err.Error(), "E2B_API_KEY") {
		t.Fatalf("got %v", err)
	}
}
