// Package e2b implements runner.Runner on E2B sandboxes: the REST control plane
// (https://api.e2b.dev) creates/kills sandboxes and envd (Connect RPC + REST, routed via
// https://sandbox.e2b.app with E2b-Sandbox-Id) runs processes and moves files.
package e2b

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/alpine-dev/alpine/services/runner"
)

const (
	DefaultAPIURL  = "https://api.e2b.dev"
	DefaultEnvdURL = "https://sandbox.e2b.app"
	EnvdPort       = 49983
)

// Runner talks to E2B. APIKey comes from E2B_API_KEY; tests point the URLs at httptest servers.
type Runner struct {
	APIKey  string
	APIURL  string
	EnvdURL string
	HTTP    *http.Client
	// User is the in-sandbox user for processes and files (E2B default "user").
	User string
}

// New builds a Runner with defaults.
func New(apiKey string) *Runner { return &Runner{APIKey: apiKey} }

func (r *Runner) Name() string { return "e2b" }

func (r *Runner) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 0}
}

func (r *Runner) apiURL() string {
	if r.APIURL != "" {
		return r.APIURL
	}
	return DefaultAPIURL
}

func (r *Runner) envdURL() string {
	if r.EnvdURL != "" {
		return r.EnvdURL
	}
	return DefaultEnvdURL
}

func (r *Runner) user() string {
	if r.User != "" {
		return r.User
	}
	return "user"
}

type createReq struct {
	TemplateID string            `json:"templateID"`
	Timeout    int               `json:"timeout"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	EnvVars    map[string]string `json:"envVars,omitempty"`
	Secure     bool              `json:"secure"`
	AllowNet   *bool             `json:"allow_internet_access,omitempty"`
}

type sandboxResp struct {
	SandboxID       string  `json:"sandboxID"`
	TemplateID      string  `json:"templateID"`
	EnvdVersion     string  `json:"envdVersion"`
	EnvdAccessToken *string `json:"envdAccessToken"`
}

// APIError is a non-2xx response from E2B.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string { return fmt.Sprintf("e2b: http %d: %s", e.Status, e.Body) }

func (r *Runner) do(ctx context.Context, method, path string, body any, out any) error {
	if r.APIKey == "" {
		return errors.New("e2b: E2B_API_KEY is not set")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.apiURL()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", r.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return runner.ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		return &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Create starts a secure sandbox from spec.Template.
func (r *Runner) Create(ctx context.Context, spec runner.Spec) (runner.Sandbox, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = time.Hour
	}
	net := spec.Network
	var out sandboxResp
	err := r.do(ctx, http.MethodPost, "/sandboxes", createReq{
		TemplateID: spec.Template, Timeout: int(timeout.Seconds()), Metadata: spec.Labels,
		EnvVars: spec.Env, Secure: true, AllowNet: &net,
	}, &out)
	if err != nil {
		return nil, err
	}
	return r.sandbox(out, spec.Workdir), nil
}

// Connect re-attaches to an existing sandbox and refreshes its access token.
func (r *Runner) Connect(ctx context.Context, id string) (runner.Sandbox, error) {
	var out sandboxResp
	if err := r.do(ctx, http.MethodGet, "/sandboxes/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	if out.SandboxID == "" {
		out.SandboxID = id
	}
	return r.sandbox(out, ""), nil
}

func (r *Runner) sandbox(s sandboxResp, workdir string) *Sandbox {
	sb := &Sandbox{r: r, id: s.SandboxID, workdir: workdir}
	if s.EnvdAccessToken != nil {
		sb.token = *s.EnvdAccessToken
	}
	return sb
}

// Sandbox is a live E2B sandbox.
type Sandbox struct {
	r       *Runner
	id      string
	token   string
	workdir string

	closeOnce sync.Once
	closeErr  error
}

func (s *Sandbox) ID() string { return s.id }

func (s *Sandbox) envdRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.r.envdURL()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("E2b-Sandbox-Id", s.id)
	req.Header.Set("E2b-Sandbox-Port", strconv.Itoa(EnvdPort))
	if s.token != "" {
		req.Header.Set("X-Access-Token", s.token)
	}
	req.SetBasicAuth(s.r.user(), "")
	return req, nil
}

type processConfig struct {
	Cmd  string            `json:"cmd"`
	Args []string          `json:"args,omitempty"`
	Envs map[string]string `json:"envs,omitempty"`
	Cwd  *string           `json:"cwd,omitempty"`
}

type startRequest struct {
	Process processConfig `json:"process"`
	Stdin   bool          `json:"stdin"`
}

type processEvent struct {
	Start *struct {
		Pid uint32 `json:"pid"`
	} `json:"start"`
	Data *struct {
		Stdout string `json:"stdout"` // base64 (proto3 JSON bytes)
		Stderr string `json:"stderr"`
	} `json:"data"`
	End *struct {
		ExitCode int    `json:"exitCode"`
		Exited   bool   `json:"exited"`
		Status   string `json:"status"`
		Error    string `json:"error"`
	} `json:"end"`
}

type endStream struct {
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// EncodeFrame writes one Connect streaming envelope.
func EncodeFrame(flags byte, payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

// ReadFrame reads one Connect streaming envelope.
func ReadFrame(r *bufio.Reader) (flags byte, payload []byte, err error) {
	var hdr [5]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > 16<<20 {
		return 0, nil, fmt.Errorf("e2b: frame too large (%d bytes)", n)
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(r, payload)
	return hdr[0], payload, err
}

// Exec runs a process via envd's process.Process/Start server stream.
func (s *Sandbox) Exec(ctx context.Context, c runner.Command) (runner.ExecResult, error) {
	if len(c.Stdin) > 0 {
		return runner.ExecResult{}, errors.New("e2b: stdin not supported; write a file instead")
	}
	cfg := processConfig{Cmd: c.Cmd, Args: c.Args, Envs: c.Env}
	dir := c.Dir
	if dir == "" {
		dir = s.workdir
	}
	if dir != "" {
		cfg.Cwd = &dir
	}
	body, _ := json.Marshal(startRequest{Process: cfg})
	req, err := s.envdRequest(ctx, http.MethodPost, "/process.Process/Start", bytes.NewReader(EncodeFrame(0, body)))
	if err != nil {
		return runner.ExecResult{}, err
	}
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if c.Timeout > 0 {
		req.Header.Set("Connect-Timeout-Ms", strconv.FormatInt(c.Timeout.Milliseconds(), 10))
	}
	started := time.Now()
	resp, err := s.r.client().Do(req)
	if err != nil {
		return runner.ExecResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return runner.ExecResult{}, &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	stdout := &runner.CappedBuffer{}
	stderr := &runner.CappedBuffer{}
	res := runner.ExecResult{ExitCode: -1}
	br := bufio.NewReader(resp.Body)
	ended := false
	for {
		flags, payload, err := ReadFrame(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return res, fmt.Errorf("e2b: read stream: %w", err)
		}
		if flags&0x02 != 0 { // end-of-stream
			var es endStream
			_ = json.Unmarshal(payload, &es)
			if es.Error != nil {
				return res, fmt.Errorf("e2b: %s: %s", es.Error.Code, es.Error.Message)
			}
			break
		}
		var msg struct {
			Event processEvent `json:"event"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil {
			return res, fmt.Errorf("e2b: decode event: %w", err)
		}
		ev := msg.Event
		if ev.Data != nil {
			if ev.Data.Stdout != "" {
				b, _ := base64.StdEncoding.DecodeString(ev.Data.Stdout)
				stdout.Write(b)
			}
			if ev.Data.Stderr != "" {
				b, _ := base64.StdEncoding.DecodeString(ev.Data.Stderr)
				stderr.Write(b)
			}
		}
		if ev.End != nil {
			ended = true
			res.ExitCode = ev.End.ExitCode
			if ev.End.Error != "" && !ev.End.Exited {
				stderr.Write([]byte(ev.End.Error))
			}
		}
	}
	res.Stdout, res.Stderr = stdout.Bytes(), stderr.Bytes()
	res.Truncated = stdout.Truncated || stderr.Truncated
	res.Duration = time.Since(started)
	if !ended {
		return res, errors.New("e2b: process stream ended without an end event")
	}
	return res, nil
}

// WriteFile uploads data to path (envd POST /files, multipart).
func (s *Sandbox) WriteFile(ctx context.Context, path string, data []byte) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", path)
	if err != nil {
		return err
	}
	fw.Write(data)
	mw.Close()
	q := url.Values{"path": {path}, "username": {s.r.user()}}
	req, err := s.envdRequest(ctx, http.MethodPost, "/files?"+q.Encode(), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := s.r.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return &APIError{Status: resp.StatusCode, Body: string(b)}
	}
	return nil
}

// ReadFile downloads path (envd GET /files).
func (s *Sandbox) ReadFile(ctx context.Context, path string) ([]byte, error) {
	q := url.Values{"path": {path}, "username": {s.r.user()}}
	req, err := s.envdRequest(ctx, http.MethodGet, "/files?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.r.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("e2b: %s: %w", path, runner.ErrNotFound)
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, &APIError{Status: resp.StatusCode, Body: string(b)}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// Close kills the sandbox. A sandbox that is already gone counts as closed.
func (s *Sandbox) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		err := s.r.do(ctx, http.MethodDelete, "/sandboxes/"+url.PathEscape(s.id), nil, nil)
		if errors.Is(err, runner.ErrNotFound) {
			err = nil
		}
		s.closeErr = err
	})
	return s.closeErr
}

var _ runner.Runner = (*Runner)(nil)
