package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// noSleep makes retry waits instant in tests.
func noSleep(context.Context, time.Duration) error { return nil }

// recorded is one request seen by the fake GitHub API.
type recorded struct {
	Method, Path, Auth string
	Query              string
	Header             http.Header
	Body               []byte
}

func (r recorded) JSON(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("%s %s: body is not a JSON object: %v (%s)", r.Method, r.Path, err, r.Body)
	}
	return m
}

type route struct {
	status int
	body   string
	hdr    map[string]string // extra response headers
	hangup bool              // close the connection without a response
}

// fakeGitHub is an httptest server with canned responses keyed by
// "METHOD /escaped/path". Unknown routes return 404.
type fakeGitHub struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]route
	queues map[string][]route // scripted one-shot responses, consumed before routes
	reqs   []recorded
}

func newFake(t *testing.T, routes map[string]route) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{routes: routes, queues: map[string][]route{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			if _, ok := f.routesHas(key + "?" + r.URL.RawQuery); ok {
				key += "?" + r.URL.RawQuery
			}
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), Header: r.Header.Clone(), Body: b})
		rt, ok := f.routes[key]
		if q := f.queues[key]; len(q) > 0 {
			rt, ok = q[0], true
			f.queues[key] = q[1:]
		}
		f.mu.Unlock()
		if ok && rt.hangup {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		for k, v := range rt.hdr {
			w.Header().Set(k, strings.ReplaceAll(v, "{{base}}", f.URL))
		}
		w.WriteHeader(rt.status)
		_, _ = io.WriteString(w, rt.body)
	}))
	t.Cleanup(f.Close)
	return f
}

// routesHas reports whether a route or queue exists for key (used to match
// query-specific routes such as pagination pages).
func (f *fakeGitHub) routesHas(key string) (route, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if q := f.queues[key]; len(q) > 0 {
		return q[0], true
	}
	r, ok := f.routes[key]
	return r, ok
}

func (f *fakeGitHub) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

// script queues one-shot responses for key, served in order before the default route.
func (f *fakeGitHub) script(key string, rts ...route) {
	f.mu.Lock()
	f.queues[key] = append(f.queues[key], rts...)
	f.mu.Unlock()
}

func (f *fakeGitHub) set(key string, rt route) {
	f.mu.Lock()
	f.routes[key] = rt
	f.mu.Unlock()
}

// assertSeq checks that requests were exactly the given "METHOD path" sequence.
func assertSeq(t *testing.T, got []recorded, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		var g []string
		for _, r := range got {
			g = append(g, r.Method+" "+r.Path)
		}
		t.Fatalf("got %d requests %v, want %v", len(got), g, want)
	}
	for i, w := range want {
		if g := got[i].Method + " " + got[i].Path; g != w {
			t.Errorf("request %d: got %q want %q", i, g, w)
		}
	}
}

// assertStdHeaders checks headers every GitHub API call must carry.
func assertStdHeaders(t *testing.T, r recorded, wantAuth string) {
	t.Helper()
	if r.Auth != wantAuth {
		t.Errorf("%s %s: Authorization = %q, want %q", r.Method, r.Path, r.Auth, wantAuth)
	}
	if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != APIVersion ||
		r.Header.Get("User-Agent") == "" {
		t.Errorf("%s %s: missing standard headers: %v", r.Method, r.Path, r.Header)
	}
	if len(r.Body) > 0 && r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("%s %s: Content-Type = %q", r.Method, r.Path, r.Header.Get("Content-Type"))
	}
}
