package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultWebURL is github.com (OAuth and App-creation pages live there, not on the API host).
const DefaultWebURL = "https://github.com"

// ---- App manifest flow ----------------------------------------------------------------------

// Manifest is a GitHub App manifest (docs: "Registering a GitHub App from a manifest").
type Manifest struct {
	Name                  string            `json:"name,omitempty"`
	URL                   string            `json:"url"`
	Description           string            `json:"description,omitempty"`
	HookAttributes        HookAttributes    `json:"hook_attributes"`
	RedirectURL           string            `json:"redirect_url"`
	CallbackURLs          []string          `json:"callback_urls,omitempty"`
	SetupURL              string            `json:"setup_url,omitempty"`
	SetupOnUpdate         bool              `json:"setup_on_update"`
	Public                bool              `json:"public"`
	RequestOAuthOnInstall bool              `json:"request_oauth_on_install"`
	DefaultPermissions    map[string]string `json:"default_permissions"`
	DefaultEvents         []string          `json:"default_events"`
}

// HookAttributes configures the App webhook.
type HookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// AlpinePermissions is exactly what Alpine requests from customers: contents, pull requests and
// issues write; metadata and checks read. Never workflows, administration or secrets.
func AlpinePermissions() map[string]string {
	return map[string]string{"contents": "write", "pull_requests": "write", "issues": "write", "metadata": "read", "checks": "read"}
}

// AlpineEvents are the webhook events the App subscribes to. installation and
// installation_repositories are delivered to every App automatically and cannot be listed.
func AlpineEvents() []string { return []string{"issues", "issue_comment", "pull_request"} }

// ForbiddenPermissions must never appear in a manifest Alpine builds.
var ForbiddenPermissions = []string{"workflows", "administration", "secrets", "actions", "environments", "organization_administration"}

// ManifestOptions are the deployment URLs a manifest needs.
type ManifestOptions struct {
	Name        string // App name (GitHub lets the operator edit it)
	HomepageURL string // e.g. APP_URL
	WebhookURL  string // public URL GitHub posts to (a smee.io channel in local dev)
	RedirectURL string // manifest callback: <api>/v1/setup/github-app/callback
	SetupURL    string // installation callback: <api>/v1/github/setup
	Public      bool
}

// NewManifest builds Alpine's manifest. Installing the App asks the user to authorize it
// (request_oauth_on_install) so the setup callback can prove the user can access the
// installation before linking it to their workspace.
func NewManifest(o ManifestOptions) Manifest {
	return Manifest{
		Name: o.Name, URL: o.HomepageURL,
		Description:           "Alpine turns issues into verified pull requests. It pushes alpine/* branches and opens PRs; a human merges.",
		HookAttributes:        HookAttributes{URL: o.WebhookURL, Active: true},
		RedirectURL:           o.RedirectURL,
		CallbackURLs:          []string{o.SetupURL},
		SetupURL:              o.SetupURL,
		SetupOnUpdate:         true,
		Public:                o.Public,
		RequestOAuthOnInstall: true,
		DefaultPermissions:    AlpinePermissions(),
		DefaultEvents:         AlpineEvents(),
	}
}

// Validate refuses forbidden permissions and missing URLs.
func (m Manifest) Validate() error {
	for _, p := range ForbiddenPermissions {
		if _, ok := m.DefaultPermissions[p]; ok {
			return fmt.Errorf("github: manifest must not request %q", p)
		}
	}
	for name, u := range map[string]string{"url": m.URL, "hook_attributes.url": m.HookAttributes.URL, "redirect_url": m.RedirectURL} {
		pu, err := url.Parse(u)
		if u == "" || err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || pu.Host == "" {
			return fmt.Errorf("github: manifest %s must be an absolute http(s) URL, got %q", name, u)
		}
	}
	return nil
}

// ManifestPostURL is where the browser POSTs the manifest (personal account, or an org).
func ManifestPostURL(webBase, org, state string) string {
	webBase = strings.TrimRight(orString(webBase, DefaultWebURL), "/")
	p := "/settings/apps/new"
	if org != "" {
		p = "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	return webBase + p + "?state=" + url.QueryEscape(state)
}

// ManifestConversion is the response of POST /app-manifests/{code}/conversions.
// String() redacts secrets.
type ManifestConversion struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	Owner         struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
	Permissions map[string]string `json:"permissions"`
	Events      []string          `json:"events"`
}

func (m ManifestConversion) String() string {
	return fmt.Sprintf("ManifestConversion{id=%d slug=%s secrets=REDACTED}", m.ID, m.Slug)
}

// GoString redacts secrets.
func (m ManifestConversion) GoString() string { return m.String() }

var codeRe = func(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// ConvertManifest exchanges the one-hour manifest code for the App's id, slug and secrets.
func ConvertManifest(ctx context.Context, hc *http.Client, apiBase, code string) (*ManifestConversion, error) {
	if !codeRe(code) {
		return nil, errors.New("github: invalid manifest code")
	}
	var out ManifestConversion
	if err := plainJSON(ctx, hc, http.MethodPost, strings.TrimRight(orString(apiBase, DefaultBaseURL), "/")+"/app-manifests/"+code+"/conversions", "", nil, &out); err != nil {
		return nil, err
	}
	if out.ID <= 0 || out.Slug == "" || out.PEM == "" || out.WebhookSecret == "" {
		return nil, errors.New("github: manifest conversion response incomplete")
	}
	return &out, nil
}

// ---- user-to-server OAuth (install verification) --------------------------------------------

// ExchangeUserCode trades an OAuth code (from installing with request_oauth_on_install) for a
// user access token. The token is used once, to list the user's installations, and discarded.
func ExchangeUserCode(ctx context.Context, hc *http.Client, webBase, clientID, clientSecret, code string) (string, error) {
	if !codeRe(code) {
		return "", errors.New("github: invalid oauth code")
	}
	form := url.Values{"client_id": {clientID}, "client_secret": {clientSecret}, "code": {code}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(orString(webBase, DefaultWebURL), "/")+"/login/oauth/access_token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := orDefault(hc).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("github: oauth token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("github: oauth exchange failed: %s", orString(out.Error, res.Status))
	}
	return out.AccessToken, nil
}

// UserCanAccessInstallation reports whether the user (by user token) can access installation id
// (GET /user/installations, paginated).
func UserCanAccessInstallation(ctx context.Context, hc *http.Client, apiBase, userToken string, id int64) (bool, error) {
	base := strings.TrimRight(orString(apiBase, DefaultBaseURL), "/")
	for page := 1; page <= 10; page++ {
		var out struct {
			Installations []struct {
				ID int64 `json:"id"`
			} `json:"installations"`
		}
		if err := plainJSON(ctx, hc, http.MethodGet, base+"/user/installations?per_page=100&page="+strconv.Itoa(page), userToken, nil, &out); err != nil {
			return false, err
		}
		for _, i := range out.Installations {
			if i.ID == id {
				return true, nil
			}
		}
		if len(out.Installations) < 100 {
			return false, nil
		}
	}
	return false, nil
}

func plainJSON(ctx context.Context, hc *http.Client, method, u, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", APIVersion)
	req.Header.Set("User-Agent", "alpine")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := orDefault(hc).Do(req)
	if err != nil {
		return &TransportError{Method: method, Path: req.URL.Path, Err: err}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 5<<20))
	if res.StatusCode >= 300 {
		return &APIError{StatusCode: res.StatusCode, Method: method, Path: req.URL.Path, Message: strings.TrimSpace(string(b))}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// ---- App-level installation reads -----------------------------------------------------------

// InstallationDetails is GET /app/installations/{id}.
type InstallationDetails struct {
	ID      int64 `json:"id"`
	AppID   int64 `json:"app_id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	RepositorySelection string            `json:"repository_selection"`
	Permissions         map[string]string `json:"permissions"`
	SuspendedAt         *time.Time        `json:"suspended_at"`
	HTMLURL             string            `json:"html_url"`
}

// GetInstallation reads an installation of this App (404 if it belongs to another App).
func (a *App) GetInstallation(ctx context.Context, id int64) (*InstallationDetails, error) {
	var out InstallationDetails
	if err := a.doAsApp(ctx, http.MethodGet, "/app/installations/"+strconv.FormatInt(id, 10), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// InstallationRepo is one repository an installation can access.
type InstallationRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
	Archived      bool   `json:"archived"`
}

// ListInstallationRepos lists the repositories an installation can access, with a short-lived
// installation token restricted to metadata:read (never cached, never returned).
func (a *App) ListInstallationRepos(ctx context.Context, id int64) ([]InstallationRepo, error) {
	var tok InstallationToken
	if err := a.doAsApp(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(id, 10)+"/access_tokens",
		map[string]any{"permissions": map[string]string{"metadata": "read"}}, &tok); err != nil {
		return nil, err
	}
	var all []InstallationRepo
	for page := 1; page <= 10; page++ {
		var out struct {
			TotalCount   int                `json:"total_count"`
			Repositories []InstallationRepo `json:"repositories"`
		}
		_, err := a.t.do(ctx, call{method: http.MethodGet, path: "/installation/repositories?per_page=100&page=" + strconv.Itoa(page),
			out: &out, mode: retrySafe, auth: func(context.Context) (string, error) { return tok.Token, nil }})
		if err != nil {
			return all, err
		}
		all = append(all, out.Repositories...)
		if len(out.Repositories) < 100 || len(all) >= out.TotalCount {
			break
		}
	}
	return all, nil
}

// ---- contents -------------------------------------------------------------------------------

// ErrFileNotFound is returned by GetFile for a missing path.
var ErrFileNotFound = errors.New("github: file not found")

// GetFile reads a file at ref (contents:read). Files over 1 MB are refused.
func (c *Client) GetFile(ctx context.Context, path, ref string) (content []byte, sha string, err error) {
	if err := validPath(path); err != nil {
		return nil, "", err
	}
	var out struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		SHA      string `json:"sha"`
		Size     int    `json:"size"`
	}
	p := c.repoPath(append([]string{"contents"}, strings.Split(path, "/")...)...)
	if ref != "" {
		p += "?ref=" + url.QueryEscape(ref)
	}
	if err := c.do(ctx, http.MethodGet, p, nil, &out); err != nil {
		if StatusCode(err) == http.StatusNotFound {
			return nil, "", ErrFileNotFound
		}
		return nil, "", err
	}
	if out.Type != "file" || out.Encoding != "base64" || out.Size > 1<<20 {
		return nil, "", fmt.Errorf("github: %s is not a readable file", path)
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, "", fmt.Errorf("github: decode %s: %w", path, err)
	}
	return b, out.SHA, nil
}
