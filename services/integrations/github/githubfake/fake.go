// Package githubfake is an in-memory GitHub (github.com pages + REST API) for tests and the
// onboarding Playwright run. It implements just enough of the App manifest flow, App
// installation + OAuth, installation tokens, contents, the Git Data API and pull requests for
// Alpine's onboarding and SCM code, and can deliver signed webhooks. It is a test double: every
// HTML page it serves says so.
package githubfake

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Repo is a seeded repository.
type Repo struct {
	ID            int64
	FullName      string
	Private       bool
	DefaultBranch string
	Files         map[string]string // files on the default branch
}

// App is a registered App.
type App struct {
	ID            int64
	Slug          string
	Name          string
	ClientID      string
	ClientSecret  string
	WebhookSecret string
	Key           *rsa.PrivateKey
	Owner         string
	Manifest      map[string]any
}

// Installation of an App on the user's account.
type Installation struct {
	ID      int64
	AppID   int64
	Account string
	RepoIDs []int64
}

type commit struct {
	tree   map[string][]byte
	parent string
}

type repoState struct {
	Repo
	refs    map[string]string
	commits map[string]*commit
	pulls   []map[string]any
}

type token struct {
	installationID int64
	perms          map[string]string
	repos          []string // bare names; empty = all
}

// Server is the fake.
type Server struct {
	User     string // the signed-in GitHub user (login)
	UserType string

	mu        sync.Mutex
	nextID    int64
	apps      map[string]*App // by slug
	appsByID  map[int64]*App
	manifests map[string]pendingManifest // code -> manifest
	insts     map[int64]*Installation
	repos     map[string]*repoState // by full name
	repoByID  map[int64]*repoState
	tokens    map[string]token
	userToks  map[string]string // user token -> login
	oauth     map[string]string // code -> client id
	blobs     map[string][]byte
	trees     map[string]map[string][]byte
	// Requests records every API request (method + path) for assertions.
	Requests []string
	// Deliveries records webhook deliveries (event.action -> status).
	Deliveries []string
	// HTTP is used to deliver webhooks (default http.DefaultClient).
	HTTP *http.Client
}

type pendingManifest struct {
	manifest map[string]any
	owner    string
	state    string
}

// New returns a fake with the given user and repos (all visible to any installation).
func New(user string, repos ...Repo) *Server {
	s := &Server{User: user, UserType: "User", nextID: 1000, apps: map[string]*App{}, appsByID: map[int64]*App{},
		manifests: map[string]pendingManifest{}, insts: map[int64]*Installation{}, repos: map[string]*repoState{},
		repoByID: map[int64]*repoState{}, tokens: map[string]token{}, userToks: map[string]string{}, oauth: map[string]string{},
		blobs: map[string][]byte{}, trees: map[string]map[string][]byte{}}
	for _, r := range repos {
		s.AddRepo(r)
	}
	return s
}

// AddRepo seeds a repository with an initial commit on its default branch.
func (s *Server) AddRepo(r Repo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.DefaultBranch == "" {
		r.DefaultBranch = "main"
	}
	rs := &repoState{Repo: r, refs: map[string]string{}, commits: map[string]*commit{}}
	tree := map[string][]byte{}
	for p, c := range r.Files {
		tree[p] = []byte(c)
	}
	sha := s.shaOf("commit", r.FullName, "init")
	rs.commits[sha] = &commit{tree: tree}
	rs.refs[r.DefaultBranch] = sha
	s.repos[r.FullName] = rs
	s.repoByID[r.ID] = rs
}

func (s *Server) id() int64 { s.nextID++; return s.nextID }

func (s *Server) shaOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))[:40]
}

func randToken(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) { writeJSON(w, 404, map[string]string{"message": "Not Found"}) }

// Apps returns registered apps (for assertions).
func (s *Server) Apps() []App {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []App
	for _, a := range s.apps {
		out = append(out, *a)
	}
	return out
}

// Installations returns installations (for assertions).
func (s *Server) Installations() []Installation {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Installation
	for _, i := range s.insts {
		out = append(out, *i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// Pulls returns the open PRs of a repo.
func (s *Server) Pulls(full string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.repos[full]; r != nil {
		return append([]map[string]any(nil), r.pulls...)
	}
	return nil
}

// FileAt reads a file at a branch (for assertions).
func (s *Server) FileAt(full, branch, path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repos[full]
	if r == nil {
		return "", false
	}
	c := r.commits[r.refs[branch]]
	if c == nil {
		return "", false
	}
	b, ok := c.tree[path]
	return string(b), ok
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	p := r.URL.Path
	switch {
	// ---- github.com pages ----
	case p == "/settings/apps/new" || strings.HasPrefix(p, "/organizations/") && strings.HasSuffix(p, "/settings/apps/new"):
		s.manifestPage(w, r)
	case p == "/settings/apps/new/confirm":
		s.manifestConfirm(w, r)
	case strings.HasPrefix(p, "/apps/") && strings.HasSuffix(p, "/installations/new"):
		s.installPage(w, r)
	case strings.HasPrefix(p, "/apps/") && strings.HasSuffix(p, "/installations/confirm"):
		s.installConfirm(w, r)
	case p == "/login/oauth/authorize":
		s.authorize(w, r)
	case p == "/login/oauth/access_token":
		s.accessToken(w, r)
	// ---- fake control ----
	case strings.HasPrefix(p, "/_fake/"):
		s.control(w, r)
	// ---- REST API (served on the same host; set GITHUB_API_URL to this server) ----
	case strings.HasPrefix(p, "/app-manifests/") && strings.HasSuffix(p, "/conversions") && r.Method == http.MethodPost:
		s.convert(w, r)
	case p == "/user/installations":
		s.userInstallations(w, r)
	case strings.HasPrefix(p, "/app/installations/"):
		s.appInstallation(w, r)
	case p == "/installation/repositories":
		s.installationRepos(w, r)
	case strings.HasPrefix(p, "/repos/"):
		s.repoAPI(w, r)
	default:
		notFound(w)
	}
}

// ---- pages ----------------------------------------------------------------------------------

var page = template.Must(template.New("p").Parse(`<!doctype html><html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font:14px -apple-system,Segoe UI,Helvetica,Arial,sans-serif;background:#f6f8fa;color:#1f2328;margin:0}
.bar{background:#24292f;color:#fff;padding:12px 24px;font-weight:600}.bar span{font-weight:400;opacity:.7;margin-left:8px}
main{max-width:640px;margin:32px auto;background:#fff;border:1px solid #d0d7de;border-radius:6px;padding:24px}
h1{font-size:20px;margin:0 0 16px}dl{display:grid;grid-template-columns:160px 1fr;gap:6px 12px}dt{color:#59636e}
button{background:#1f883d;color:#fff;border:0;border-radius:6px;padding:8px 16px;font-weight:600;font-size:14px;cursor:pointer}
input{border:1px solid #d0d7de;border-radius:6px;padding:6px 8px;font-size:14px;width:300px}
.note{color:#59636e;font-size:12px;margin-top:16px}</style></head><body>
<div class="bar">GitHub<span>Fake GitHub (test double used by Alpine's tests)</span></div><main>{{.Body}}</main></body></html>`))

func render(w http.ResponseWriter, title string, body template.HTML) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = page.Execute(w, map[string]any{"Title": title, "Body": body})
}

func (s *Server) manifestPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "the manifest flow POSTs a form", 405)
		return
	}
	_ = r.ParseForm()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.PostForm.Get("manifest")), &m); err != nil {
		http.Error(w, "invalid manifest: "+err.Error(), 422)
		return
	}
	owner := s.User
	if strings.HasPrefix(r.URL.Path, "/organizations/") {
		owner = strings.Split(r.URL.Path, "/")[2]
	}
	code := randToken("")[:20]
	s.mu.Lock()
	s.manifests[code] = pendingManifest{manifest: m, owner: owner, state: r.URL.Query().Get("state")}
	s.mu.Unlock()
	perms, _ := json.Marshal(m["default_permissions"])
	events, _ := json.Marshal(m["default_events"])
	hook, _ := m["hook_attributes"].(map[string]any)
	body := template.HTML(fmt.Sprintf(`<h1>Create GitHub App for %s</h1>
<form method="post" action="/settings/apps/new/confirm"><input type="hidden" name="code" value="%s">
<dl><dt>GitHub App name</dt><dd><input name="name" value="%s"></dd>
<dt>Webhook URL</dt><dd>%s</dd><dt>Permissions</dt><dd><code>%s</code></dd><dt>Events</dt><dd><code>%s</code></dd></dl>
<p><button type="submit">Create GitHub App for %s</button></p></form>`,
		template.HTMLEscapeString(owner), code, template.HTMLEscapeString(fmt.Sprint(m["name"])), template.HTMLEscapeString(fmt.Sprint(hook["url"])),
		template.HTMLEscapeString(string(perms)), template.HTMLEscapeString(string(events)), template.HTMLEscapeString(owner)))
	render(w, "Create GitHub App", body)
}

func (s *Server) manifestConfirm(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	code := r.PostForm.Get("code")
	s.mu.Lock()
	pm, ok := s.manifests[code]
	if ok && r.PostForm.Get("name") != "" {
		pm.manifest["name"] = r.PostForm.Get("name")
		s.manifests[code] = pm
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "unknown manifest", 404)
		return
	}
	u, _ := url.Parse(fmt.Sprint(pm.manifest["redirect_url"]))
	q := u.Query()
	q.Set("code", code)
	if pm.state != "" {
		q.Set("state", pm.state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func (s *Server) convert(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/app-manifests/"), "/conversions")
	s.mu.Lock()
	pm, ok := s.manifests[code]
	delete(s.manifests, code)
	s.mu.Unlock()
	if !ok {
		notFound(w)
		return
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	name := fmt.Sprint(pm.manifest["name"])
	s.mu.Lock()
	a := &App{ID: s.id(), Slug: slugify(name), Name: name, ClientID: "Iv1." + randToken("")[:16], ClientSecret: randToken("cs_"),
		WebhookSecret: randToken("wh_"), Key: key, Owner: pm.owner, Manifest: pm.manifest}
	s.apps[a.Slug], s.appsByID[a.ID] = a, a
	s.mu.Unlock()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	writeJSON(w, 201, map[string]any{"id": a.ID, "slug": a.Slug, "name": a.Name, "html_url": "https://github.com/apps/" + a.Slug,
		"client_id": a.ClientID, "client_secret": a.ClientSecret, "webhook_secret": a.WebhookSecret, "pem": string(pemBytes),
		"owner": map[string]any{"login": a.Owner, "type": "User"}, "permissions": pm.manifest["default_permissions"],
		"events": pm.manifest["default_events"]})
}

func (s *Server) installPage(w http.ResponseWriter, r *http.Request) {
	slug := strings.Split(r.URL.Path, "/")[2]
	s.mu.Lock()
	a := s.apps[slug]
	var names []string
	for _, rs := range s.repos {
		names = append(names, rs.FullName)
	}
	s.mu.Unlock()
	if a == nil {
		http.Error(w, "no such app", 404)
		return
	}
	sort.Strings(names)
	body := template.HTML(fmt.Sprintf(`<h1>Install %s</h1>
<form method="post" action="/apps/%s/installations/confirm"><input type="hidden" name="state" value="%s">
<dl><dt>Install on</dt><dd>@%s</dd><dt>Repository access</dt><dd>All repositories (%s)</dd></dl>
<p><button type="submit">Install &amp; Authorize</button></p></form>`, template.HTMLEscapeString(a.Name), slug,
		template.HTMLEscapeString(r.URL.Query().Get("state")), template.HTMLEscapeString(s.User), template.HTMLEscapeString(strings.Join(names, ", "))))
	render(w, "Install "+a.Name, body)
}

func (s *Server) installConfirm(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	slug := strings.Split(r.URL.Path, "/")[2]
	s.mu.Lock()
	a := s.apps[slug]
	if a == nil {
		s.mu.Unlock()
		http.Error(w, "no such app", 404)
		return
	}
	inst := &Installation{ID: s.id(), AppID: a.ID, Account: s.User}
	for id := range s.repoByID {
		inst.RepoIDs = append(inst.RepoIDs, id)
	}
	s.insts[inst.ID] = inst
	code := randToken("")[:20]
	s.oauth[code] = a.ClientID
	s.mu.Unlock()
	cb := fmt.Sprint(a.Manifest["setup_url"])
	if urls, ok := a.Manifest["callback_urls"].([]any); ok && len(urls) > 0 && a.Manifest["request_oauth_on_install"] == true {
		cb = fmt.Sprint(urls[0])
	}
	u, _ := url.Parse(cb)
	q := u.Query()
	q.Set("installation_id", strconv.FormatInt(inst.ID, 10))
	q.Set("setup_action", "install")
	q.Set("code", code)
	if st := r.PostForm.Get("state"); st != "" {
		q.Set("state", st)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := randToken("")[:20]
	s.mu.Lock()
	s.oauth[code] = q.Get("client_id")
	s.mu.Unlock()
	u, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || u.Host == "" {
		http.Error(w, "bad redirect_uri", 400)
		return
	}
	v := u.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) accessToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	cid, ok := s.oauth[r.PostForm.Get("code")]
	delete(s.oauth, r.PostForm.Get("code"))
	var app *App
	for _, a := range s.apps {
		if a.ClientID == cid {
			app = a
		}
	}
	if !ok || app == nil || r.PostForm.Get("client_id") != cid || r.PostForm.Get("client_secret") != app.ClientSecret {
		writeJSON(w, 200, map[string]string{"error": "bad_verification_code"})
		return
	}
	t := randToken("ghu_")
	s.userToks[t] = s.User
	writeJSON(w, 200, map[string]string{"access_token": t, "token_type": "bearer"})
}

// ---- API ------------------------------------------------------------------------------------

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (s *Server) userInstallations(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.userToks[bearer(r)]; !ok {
		writeJSON(w, 401, map[string]string{"message": "Bad credentials"})
		return
	}
	var out []map[string]any
	for _, i := range s.insts {
		out = append(out, map[string]any{"id": i.ID, "app_id": i.AppID, "account": map[string]any{"login": i.Account}})
	}
	writeJSON(w, 200, map[string]any{"total_count": len(out), "installations": out})
}

// appJWT verifies an App JWT and returns the app.
func (s *Server) appJWT(r *http.Request) *App {
	var app *App
	_, err := jwt.ParseWithClaims(bearer(r), &jwt.RegisteredClaims{}, func(t *jwt.Token) (any, error) {
		iss, _ := t.Claims.GetIssuer()
		id, _ := strconv.ParseInt(iss, 10, 64)
		s.mu.Lock()
		app = s.appsByID[id]
		s.mu.Unlock()
		if app == nil {
			return nil, fmt.Errorf("unknown app %s", iss)
		}
		return &app.Key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		return nil
	}
	return app
}

func (s *Server) appInstallation(w http.ResponseWriter, r *http.Request) {
	app := s.appJWT(r)
	if app == nil {
		writeJSON(w, 401, map[string]string{"message": "A JSON web token could not be decoded"})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/app/installations/"), "/")
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	s.mu.Lock()
	inst := s.insts[id]
	s.mu.Unlock()
	if inst == nil || inst.AppID != app.ID {
		notFound(w)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{"id": inst.ID, "app_id": inst.AppID, "account": map[string]any{"login": inst.Account, "type": s.UserType},
			"repository_selection": "all", "permissions": app.Manifest["default_permissions"]})
		return
	}
	if len(parts) == 2 && parts[1] == "access_tokens" && r.Method == http.MethodPost {
		var in struct {
			Permissions  map[string]string `json:"permissions"`
			Repositories []string          `json:"repositories"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		t := randToken("ghs_")
		s.mu.Lock()
		s.tokens[t] = token{installationID: id, perms: in.Permissions, repos: in.Repositories}
		s.mu.Unlock()
		writeJSON(w, 201, map[string]any{"token": t, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "permissions": in.Permissions})
		return
	}
	notFound(w)
}

func (s *Server) installationToken(r *http.Request) (token, *Installation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[bearer(r)]
	if !ok {
		return t, nil, false
	}
	inst := s.insts[t.installationID]
	return t, inst, inst != nil
}

func (s *Server) installationRepos(w http.ResponseWriter, r *http.Request) {
	_, inst, ok := s.installationToken(r)
	if !ok {
		writeJSON(w, 401, map[string]string{"message": "Bad credentials"})
		return
	}
	s.mu.Lock()
	var out []map[string]any
	ids := append([]int64(nil), inst.RepoIDs...)
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	for _, id := range ids {
		if rs := s.repoByID[id]; rs != nil {
			out = append(out, map[string]any{"id": rs.ID, "full_name": rs.FullName, "name": strings.Split(rs.FullName, "/")[1],
				"private": rs.Private, "default_branch": rs.DefaultBranch})
		}
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"total_count": len(out), "repositories": out})
}

func (s *Server) repoAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	if len(parts) < 3 {
		notFound(w)
		return
	}
	full := parts[0] + "/" + parts[1]
	t, inst, ok := s.installationToken(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.repos[full]
	if !ok || rs == nil {
		notFound(w)
		return
	}
	allowed := false
	for _, id := range inst.RepoIDs {
		if id == rs.ID {
			allowed = true
		}
	}
	if len(t.repos) > 0 {
		in := false
		for _, n := range t.repos {
			if n == parts[1] {
				in = true
			}
		}
		allowed = allowed && in
	}
	if !allowed {
		notFound(w)
		return
	}
	need := func(perm, level string) bool {
		got := t.perms[perm]
		if got == "write" || got == level {
			return true
		}
		writeJSON(w, 403, map[string]string{"message": "Resource not accessible by integration"})
		return false
	}
	rest := parts[2:]
	switch {
	case rest[0] == "contents" && r.Method == http.MethodGet:
		if !need("contents", "read") {
			return
		}
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = rs.DefaultBranch
		}
		c := rs.commits[rs.refs[ref]]
		path := strings.Join(rest[1:], "/")
		if c == nil {
			notFound(w)
			return
		}
		b, ok := c.tree[path]
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, 200, map[string]any{"type": "file", "encoding": "base64", "size": len(b), "sha": s.shaOf("blob", path),
			"content": base64.StdEncoding.EncodeToString(b)})
	case rest[0] == "git" && len(rest) >= 3 && rest[1] == "ref" && rest[2] == "heads" && r.Method == http.MethodGet:
		br := strings.Join(rest[3:], "/")
		sha, ok := rs.refs[br]
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, 200, map[string]any{"ref": "refs/heads/" + br, "object": map[string]any{"sha": sha, "type": "commit"}})
	case rest[0] == "git" && len(rest) == 2 && rest[1] == "refs" && r.Method == http.MethodPost:
		if !need("contents", "write") {
			return
		}
		var in struct{ Ref, SHA string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		br := strings.TrimPrefix(in.Ref, "refs/heads/")
		if _, exists := rs.refs[br]; exists {
			writeJSON(w, 422, map[string]string{"message": "Reference already exists"})
			return
		}
		rs.refs[br] = in.SHA
		writeJSON(w, 201, map[string]any{"ref": in.Ref, "object": map[string]any{"sha": in.SHA}})
	case rest[0] == "git" && len(rest) >= 3 && rest[1] == "refs" && rest[2] == "heads" && r.Method == http.MethodPatch:
		if !need("contents", "write") {
			return
		}
		var in struct{ SHA string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		br := strings.Join(rest[3:], "/")
		if c := rs.commits[in.SHA]; c == nil || c.parent != rs.refs[br] {
			writeJSON(w, 422, map[string]string{"message": "Update is not a fast forward"})
			return
		}
		rs.refs[br] = in.SHA
		writeJSON(w, 200, map[string]any{"object": map[string]any{"sha": in.SHA}})
	case rest[0] == "git" && len(rest) == 3 && rest[1] == "commits" && r.Method == http.MethodGet:
		c := rs.commits[rest[2]]
		if c == nil {
			notFound(w)
			return
		}
		treeSHA := s.shaOf("tree", rest[2])
		s.trees[treeSHA] = c.tree
		writeJSON(w, 200, map[string]any{"sha": rest[2], "tree": map[string]any{"sha": treeSHA}})
	case rest[0] == "git" && len(rest) == 2 && rest[1] == "blobs" && r.Method == http.MethodPost:
		var in struct{ Content, Encoding string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		b, _ := base64.StdEncoding.DecodeString(in.Content)
		sha := s.shaOf("blob", string(b))
		s.blobs[sha] = b
		writeJSON(w, 201, map[string]any{"sha": sha})
	case rest[0] == "git" && len(rest) == 2 && rest[1] == "trees" && r.Method == http.MethodPost:
		var in struct {
			BaseTree string `json:"base_tree"`
			Tree     []struct {
				Path string  `json:"path"`
				SHA  *string `json:"sha"`
			} `json:"tree"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		tree := map[string][]byte{}
		for k, v := range s.trees[in.BaseTree] {
			tree[k] = v
		}
		for _, e := range in.Tree {
			if e.SHA == nil {
				delete(tree, e.Path)
			} else {
				tree[e.Path] = s.blobs[*e.SHA]
			}
		}
		sha := s.shaOf("tree")
		s.trees[sha] = tree
		writeJSON(w, 201, map[string]any{"sha": sha})
	case rest[0] == "git" && len(rest) == 2 && rest[1] == "commits" && r.Method == http.MethodPost:
		var in struct {
			Message string   `json:"message"`
			Tree    string   `json:"tree"`
			Parents []string `json:"parents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		sha := s.shaOf("commit", in.Message)
		c := &commit{tree: s.trees[in.Tree]}
		if len(in.Parents) > 0 {
			c.parent = in.Parents[0]
		}
		rs.commits[sha] = c
		writeJSON(w, 201, map[string]any{"sha": sha, "tree": map[string]any{"sha": in.Tree}})
	case rest[0] == "pulls" && len(rest) == 1 && r.Method == http.MethodGet:
		head := r.URL.Query().Get("head")
		var out []map[string]any
		for _, pr := range rs.pulls {
			if head == "" || head == parts[0]+":"+fmt.Sprint(pr["head"].(map[string]any)["ref"]) {
				out = append(out, pr)
			}
		}
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, 200, out)
	case rest[0] == "pulls" && len(rest) == 1 && r.Method == http.MethodPost:
		if !need("pull_requests", "write") {
			return
		}
		var in struct{ Title, Head, Base, Body string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		n := len(rs.pulls) + 1
		pr := map[string]any{"id": s.id(), "number": n, "html_url": fmt.Sprintf("https://github.com/%s/pull/%d", full, n), "state": "open",
			"title": in.Title, "body": in.Body, "head": map[string]any{"ref": in.Head, "sha": rs.refs[in.Head]}, "base": map[string]any{"ref": in.Base}}
		rs.pulls = append(rs.pulls, pr)
		writeJSON(w, 201, pr)
	default:
		notFound(w)
	}
}

// ---- control + webhooks ---------------------------------------------------------------------

// control endpoints (test-only):
//
//	POST /_fake/installations/{id}/uninstall           deliver installation.deleted
//	POST /_fake/installations/{id}/remove-repo?repo=ID  deliver installation_repositories.removed
//	POST /_fake/issues?repo=owner/name&title=..&body=..  deliver issues.opened
func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/_fake/"), "/"), "/")
	switch {
	case len(parts) == 3 && parts[0] == "installations" && parts[2] == "uninstall":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		status, err := s.Uninstall(id)
		writeJSON(w, 200, map[string]any{"status": status, "error": errString(err)})
	case len(parts) == 3 && parts[0] == "installations" && parts[2] == "remove-repo":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		repo, _ := strconv.ParseInt(r.URL.Query().Get("repo"), 10, 64)
		status, err := s.RemoveRepo(id, repo)
		writeJSON(w, 200, map[string]any{"status": status, "error": errString(err)})
	case len(parts) == 1 && parts[0] == "issues":
		q := r.URL.Query()
		status, err := s.OpenIssue(q.Get("repo"), q.Get("title"), q.Get("body"))
		writeJSON(w, 200, map[string]any{"status": status, "error": errString(err)})
	default:
		notFound(w)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Uninstall removes an installation and delivers installation.deleted.
func (s *Server) Uninstall(id int64) (int, error) {
	s.mu.Lock()
	inst := s.insts[id]
	delete(s.insts, id)
	s.mu.Unlock()
	if inst == nil {
		return 0, fmt.Errorf("no installation %d", id)
	}
	return s.deliver(inst.AppID, "installation", map[string]any{"action": "deleted",
		"installation": map[string]any{"id": id, "account": map[string]any{"login": inst.Account}}})
}

// RemoveRepo removes a repo from an installation and delivers installation_repositories.removed.
func (s *Server) RemoveRepo(id, repoID int64) (int, error) {
	s.mu.Lock()
	inst := s.insts[id]
	var full string
	if inst != nil {
		kept := inst.RepoIDs[:0]
		for _, x := range inst.RepoIDs {
			if x != repoID {
				kept = append(kept, x)
			}
		}
		inst.RepoIDs = kept
		if rs := s.repoByID[repoID]; rs != nil {
			full = rs.FullName
		}
	}
	s.mu.Unlock()
	if inst == nil {
		return 0, fmt.Errorf("no installation %d", id)
	}
	return s.deliver(inst.AppID, "installation_repositories", map[string]any{"action": "removed", "repository_selection": "selected",
		"installation":         map[string]any{"id": id},
		"repositories_removed": []map[string]any{{"id": repoID, "full_name": full}}, "repositories_added": []any{}})
}

// OpenIssue delivers issues.opened for repo from the user (to every installation that has it).
func (s *Server) OpenIssue(full, title, body string) (int, error) {
	s.mu.Lock()
	rs := s.repos[full]
	var inst *Installation
	for _, i := range s.insts {
		for _, id := range i.RepoIDs {
			if rs != nil && id == rs.ID {
				inst = i
			}
		}
	}
	s.mu.Unlock()
	if rs == nil || inst == nil {
		return 0, fmt.Errorf("repo %s is not installed", full)
	}
	n := int(time.Now().Unix() % 100000)
	return s.deliver(inst.AppID, "issues", map[string]any{"action": "opened", "installation": map[string]any{"id": inst.ID},
		"repository": map[string]any{"id": rs.ID, "full_name": rs.FullName, "default_branch": rs.DefaultBranch, "private": rs.Private},
		"issue": map[string]any{"number": n, "title": title, "body": body, "html_url": fmt.Sprintf("https://github.com/%s/issues/%d", full, n),
			"state": "open", "user": map[string]any{"login": s.User, "type": "User"}, "labels": []any{}},
		"sender": map[string]any{"login": s.User, "type": "User"}})
}

func (s *Server) deliver(appID int64, event string, payload map[string]any) (int, error) {
	s.mu.Lock()
	app := s.appsByID[appID]
	s.mu.Unlock()
	if app == nil {
		return 0, fmt.Errorf("no app %d", appID)
	}
	hook, _ := app.Manifest["hook_attributes"].(map[string]any)
	target, _ := hook["url"].(string)
	body, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, []byte(app.WebhookSecret))
	mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", randToken("")[:32])
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	hc := s.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	s.mu.Lock()
	s.Deliveries = append(s.Deliveries, fmt.Sprintf("%s.%v -> %d", event, payload["action"], res.StatusCode))
	s.mu.Unlock()
	return res.StatusCode, nil
}
