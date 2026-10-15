package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"

	sharedauth "github.com/alpine-dev/alpine/packages/shared/auth"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
	"github.com/alpine-dev/alpine/services/api/internal/apigen"
	"github.com/alpine-dev/alpine/services/api/internal/auth"
	"github.com/alpine-dev/alpine/services/api/internal/onboarding"
	"github.com/alpine-dev/alpine/services/integrations/github"
)

// sessionUser returns the signed-in session principal (operator pages work without an org).
func (s *Server) sessionUser(w http.ResponseWriter, r *http.Request) (*sharedauth.Principal, bool) {
	p := sharedauth.PrincipalFrom(r.Context())
	if p == nil || p.UserID == uuid.Nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "sign in required")
		return nil, false
	}
	return p, true
}

func (s *Server) onboardingReady(w http.ResponseWriter) bool {
	if s.Onboarding == nil {
		writeErr(w, http.StatusServiceUnavailable, "not_configured", "GitHub onboarding is not configured on this server")
		return false
	}
	return true
}

func (s *Server) onboardingFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, onboarding.ErrForbidden):
		writeErr(w, http.StatusForbidden, "forbidden", "only a platform operator can do this")
	case errors.Is(err, onboarding.ErrBadInput):
		writeErr(w, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, onboarding.ErrAppNotConfigured):
		writeErr(w, http.StatusConflict, "github_app_not_configured", "no GitHub App is registered yet; an operator creates it at /setup/github-app")
	case errors.Is(err, ticketrepo.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, onboarding.ErrGitHub):
		s.log().Warn("github call failed", "err", err)
		writeErr(w, http.StatusBadGateway, "github_error", "GitHub request failed; try again")
	default:
		s.fail(w, err)
	}
}

func ptr[T any](v T) *T { return &v }

// GetGitHubAppSetup implements apigen.ServerInterface.
func (s *Server) GetGitHubAppSetup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionUser(w, r)
	if !ok || !s.onboardingReady(w) {
		return
	}
	st, err := s.Onboarding.Status(r.Context(), p.UserID)
	if err != nil {
		s.onboardingFail(w, err)
		return
	}
	out := apigen.GitHubAppSetup{CanConfigure: st.CanConfigure, WebhookUrl: st.WebhookURL, WebhookViaProxy: st.ViaProxy,
		Permissions: github.AlpinePermissions(), Events: github.AlpineEvents()}
	if c := st.Credentials; c != nil {
		src := apigen.GitHubAppSetupSource(c.Source)
		out.Configured, out.Source, out.AppId, out.Slug, out.Name = true, &src, &c.AppID, &c.Slug, &c.Name
		out.HtmlUrl, out.OwnerLogin = &c.HTMLURL, &c.OwnerLogin
		if !c.CreatedAt.IsZero() {
			out.CreatedAt = &c.CreatedAt
		}
	}
	writeJSON(w, 200, out)
}

// StartGitHubAppManifest implements apigen.ServerInterface.
func (s *Server) StartGitHubAppManifest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionUser(w, r)
	if !ok || !s.onboardingReady(w) {
		return
	}
	var body apigen.GitHubAppManifestRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid_input", "invalid JSON")
		return
	}
	org, name := "", ""
	if body.Organization != nil {
		org = *body.Organization
	}
	if body.Name != nil {
		name = *body.Name
	}
	postURL, manifest, err := s.Onboarding.StartManifest(r.Context(), w, p.UserID, org, name)
	if err != nil {
		s.onboardingFail(w, err)
		return
	}
	writeJSON(w, 200, apigen.GitHubAppManifestStart{PostUrl: postURL, Manifest: manifest})
}

// GitHubAppManifestCallback implements apigen.ServerInterface.
func (s *Server) GitHubAppManifestCallback(w http.ResponseWriter, r *http.Request, params apigen.GitHubAppManifestCallbackParams) {
	if !s.onboardingReady(w) {
		return
	}
	p := sharedauth.PrincipalFrom(r.Context())
	var user, org uuid.UUID
	if p != nil {
		user, org = p.UserID, p.OrgID
	}
	to := s.Onboarding.FinishManifest(r.Context(), w, r, user, org, strParam(params.Code), strParam(params.State))
	http.Redirect(w, r, to, http.StatusFound)
}

func strParam[T ~string](v *T) string {
	if v == nil {
		return ""
	}
	return string(*v)
}

// GetGitHubIntegration implements apigen.ServerInterface.
func (s *Server) GetGitHubIntegration(w http.ResponseWriter, r *http.Request) {
	id, ok := s.caller(w, r, sharedauth.PermReposRead)
	if !ok || !s.onboardingReady(w) {
		return
	}
	in, err := s.Onboarding.Integration(r.Context(), id.OrgID)
	if err != nil {
		s.onboardingFail(w, err)
		return
	}
	out := apigen.GitHubIntegration{AppConfigured: in.App != nil, Installations: []apigen.GitHubInstallation{}}
	if in.App != nil {
		out.AppSlug, out.AppHtmlUrl = &in.App.Slug, &in.App.HTMLURL
	}
	for _, i := range in.Installations {
		gi := apigen.GitHubInstallation{InstallationId: i.InstallationID, AccountLogin: i.AccountLogin,
			AccountType: apigen.GitHubInstallationAccountType(i.AccountType), Suspended: i.SuspendedAt != nil, CreatedAt: i.CreatedAt,
			EnabledRepos: ptr(i.EnabledRepos), ManageUrl: ptr(i.ManageURL)}
		if i.RepositorySelection != nil {
			gi.RepositorySelection = ptr(apigen.GitHubInstallationRepositorySelection(*i.RepositorySelection))
		}
		out.Installations = append(out.Installations, gi)
	}
	writeJSON(w, 200, out)
}

// StartGitHubInstall implements apigen.ServerInterface.
func (s *Server) StartGitHubInstall(w http.ResponseWriter, r *http.Request) {
	id, ok := s.caller(w, r, sharedauth.PermReposManage)
	if !ok || !s.onboardingReady(w) {
		return
	}
	u, err := s.Onboarding.InstallURL(r.Context(), w, id.UserID, id.OrgID)
	if err != nil {
		s.onboardingFail(w, err)
		return
	}
	writeJSON(w, 200, apigen.RedirectURL{Url: u})
}

// GitHubInstallCallback implements apigen.ServerInterface.
func (s *Server) GitHubInstallCallback(w http.ResponseWriter, r *http.Request, params apigen.GitHubInstallCallbackParams) {
	if !s.onboardingReady(w) {
		return
	}
	a := s.Authenticator
	if a == nil {
		a = auth.Principals{}
	}
	id, err := auth.FromRequest(a, r)
	if err != nil || !id.Can(sharedauth.PermReposManage) {
		http.Redirect(w, r, s.Onboarding.AppRedirect("/onboarding", "github", "signed_out"), http.StatusFound)
		return
	}
	var inst int64
	if params.InstallationId != nil {
		inst = *params.InstallationId
	}
	to := s.Onboarding.FinishInstall(r.Context(), w, r, id.UserID, id.OrgID, inst, strParam(params.Code), strParam(params.State), strParam(params.SetupAction))
	http.Redirect(w, r, to, http.StatusFound)
}

// DisconnectGitHubInstallation implements apigen.ServerInterface.
func (s *Server) DisconnectGitHubInstallation(w http.ResponseWriter, r *http.Request, installationID apigen.InstallationId) {
	id, ok := s.caller(w, r, sharedauth.PermReposManage)
	if !ok || !s.onboardingReady(w) {
		return
	}
	if err := s.Onboarding.Disconnect(r.Context(), id.OrgID, id.UserID, installationID); err != nil {
		s.onboardingFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func repoOption(o onboarding.RepoOption) apigen.GitHubRepoOption {
	out := apigen.GitHubRepoOption{ProviderRepoId: o.ProviderRepoID, FullName: o.FullName, Private: o.Private, DefaultBranch: o.DefaultBranch,
		Enabled: o.Enabled, RepoId: o.RepoID, ConfigStatus: apigen.RepoConfigStatus(o.ConfigStatus), ConfigSha: o.ConfigSHA}
	if o.ConfigError != "" {
		out.ConfigError = &o.ConfigError
	}
	if o.StarterYAML != "" {
		out.StarterYaml = &o.StarterYAML
	}
	return out
}

// ListGitHubInstallationRepos implements apigen.ServerInterface.
func (s *Server) ListGitHubInstallationRepos(w http.ResponseWriter, r *http.Request, installationID apigen.InstallationId) {
	id, ok := s.caller(w, r, sharedauth.PermReposRead)
	if !ok || !s.onboardingReady(w) {
		return
	}
	opts, err := s.Onboarding.ListRepos(r.Context(), id.OrgID, installationID)
	if err != nil {
		s.onboardingFail(w, err)
		return
	}
	out := make([]apigen.GitHubRepoOption, 0, len(opts))
	for _, o := range opts {
		out = append(out, repoOption(o))
	}
	writeJSON(w, 200, list(out))
}

// SetGitHubRepoEnabled implements apigen.ServerInterface.
func (s *Server) SetGitHubRepoEnabled(w http.ResponseWriter, r *http.Request, installationID apigen.InstallationId, providerRepoID int64) {
	id, ok := s.caller(w, r, sharedauth.PermReposManage)
	if !ok || !s.onboardingReady(w) {
		return
	}
	var body apigen.SetGitHubRepoEnabledJSONBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_input", "body must be {\"enabled\": bool}")
		return
	}
	o, err := s.Onboarding.SetEnabled(r.Context(), id.OrgID, id.UserID, installationID, providerRepoID, body.Enabled)
	if err != nil {
		s.onboardingFail(w, err)
		return
	}
	writeJSON(w, 200, repoOption(o))
}

// OpenStarterConfigPullRequest implements apigen.ServerInterface.
func (s *Server) OpenStarterConfigPullRequest(w http.ResponseWriter, r *http.Request, repoID apigen.RepoId) {
	id, ok := s.caller(w, r, sharedauth.PermFactoryConfigManage)
	if !ok || !s.onboardingReady(w) {
		return
	}
	var body apigen.OpenStarterConfigPullRequestJSONBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid_input", "invalid JSON")
		return
	}
	yaml := ""
	if body.Yaml != nil {
		yaml = *body.Yaml
	}
	pr, err := s.Onboarding.OpenStarterPR(r.Context(), id.OrgID, id.UserID, repoID, yaml)
	if err != nil {
		s.onboardingFail(w, err)
		return
	}
	writeJSON(w, 201, apigen.StarterConfigPullRequest{Number: pr.Number, Url: pr.URL, Branch: pr.Branch})
}
