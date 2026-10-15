package onboarding

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
	"github.com/alpine-dev/alpine/services/integrations/github"
)

// InstallURL starts "Connect GitHub" for org: a state tied to (user, org) and the App's
// installation page.
func (s *Service) InstallURL(ctx context.Context, w http.ResponseWriter, user, org uuid.UUID) (string, error) {
	c, err := s.creds(ctx)
	if err != nil {
		return "", err
	}
	if user == uuid.Nil || org == uuid.Nil {
		return "", ErrForbidden
	}
	state, err := s.newState(ctx, w, flow{Kind: flowInstall, UserID: user, OrgID: org})
	if err != nil {
		return "", err
	}
	return s.webURL() + "/apps/" + url.PathEscape(c.Slug) + "/installations/new?state=" + url.QueryEscape(state), nil
}

// FinishInstall handles the App's setup URL / OAuth callback and returns where to redirect.
//
// GitHub returns installation_id (and, because the manifest sets request_oauth_on_install, an
// OAuth code). The code is only trusted when it comes back with our state; otherwise the browser
// is sent through GitHub's authorize page once more with the state, so the code is bound to this
// flow. The link requires: state from this browser + same session user and org, the user's token
// can access the installation, and the installation belongs to this App.
func (s *Service) FinishInstall(ctx context.Context, w http.ResponseWriter, r *http.Request, user, org uuid.UUID, installationID int64,
	code, state, setupAction string) string {
	fail := func(reason string, err error) string {
		s.log().Warn("github install callback failed", "reason", reason, "installation", installationID, "err", err)
		return s.appRedirect("/onboarding", url.Values{"step": {"github"}, "error": {reason}})
	}
	c, err := s.creds(ctx)
	if err != nil {
		return fail("app_not_configured", err)
	}
	if state == "" || code == "" {
		// GitHub came back without our state (or without a code): re-run the OAuth authorize step
		// bound to the pending flow. The flow is re-stored under a new token.
		return s.reauthorize(ctx, w, r, user, org, installationID, c.ClientID, fail)
	}
	f, _, err := s.takeState(ctx, w, r, flowInstall, state, user)
	if err != nil {
		return fail(errCode(err), err)
	}
	if f.OrgID != org {
		return fail("org_changed", nil)
	}
	if installationID == 0 {
		installationID = f.InstallationID
	}
	if installationID <= 0 {
		return fail("installation_missing", nil)
	}
	token, err := github.ExchangeUserCode(ctx, s.HTTP, s.webURL(), c.ClientID, c.ClientSecret, code)
	if err != nil {
		return fail("oauth_failed", err)
	}
	ok, err := github.UserCanAccessInstallation(ctx, s.HTTP, s.GitHubAPIURL, token, installationID)
	if err != nil {
		return fail("verify_failed", err)
	}
	if !ok {
		return fail("installation_not_accessible", nil)
	}
	app, _, err := s.githubApp(ctx)
	if err != nil {
		return fail("app_not_configured", err)
	}
	inst, err := app.GetInstallation(ctx, installationID)
	if err != nil {
		return fail("installation_not_found", err)
	}
	if inst.AppID != 0 && inst.AppID != c.AppID {
		return fail("installation_other_app", nil)
	}
	linked, err := s.Store.LinkInstallation(ctx, org, ticketrepo.LinkInstallationInput{InstallationID: installationID,
		AccountLogin: inst.Account.Login, AccountType: inst.Account.Type, RepositorySelection: inst.RepositorySelection,
		Permissions: inst.Permissions, LinkedBy: user})
	if errors.Is(err, ticketrepo.ErrInstallationTaken) {
		return fail("installation_taken", err)
	}
	if err != nil {
		return fail("link_failed", err)
	}
	_ = s.Store.Audit(ctx, org, "user:"+user.String(), "github.installation_linked", "scm_installation", strconv.FormatInt(installationID, 10),
		map[string]any{"account": inst.Account.Login, "setup_action": setupAction})
	s.log().Info("github installation linked", "org", org, "installation", installationID, "account", linked.AccountLogin)
	return s.appRedirect("/onboarding", url.Values{"step": {"repos"}, "installation": {strconv.FormatInt(installationID, 10)}})
}

func (s *Service) reauthorize(ctx context.Context, w http.ResponseWriter, r *http.Request, user, org uuid.UUID, installationID int64,
	clientID string, fail func(string, error) string) string {
	cookie, err := r.Cookie(StateCookie)
	if err != nil || cookie.Value == "" {
		return fail("state_missing", nil)
	}
	b, err := s.States.Take(ctx, stateKey(cookie.Value))
	if err != nil {
		return fail("state_expired", err)
	}
	var f flow
	if err := jsonUnmarshal(b, &f); err != nil || f.Kind != flowInstall {
		return fail("state_mismatch", err)
	}
	if f.UserID != user || f.OrgID != org {
		return fail("state_user_mismatch", nil)
	}
	if f.InstallationID != 0 && installationID != 0 && f.InstallationID != installationID {
		return fail("installation_mismatch", nil)
	}
	if installationID != 0 {
		f.InstallationID = installationID
	}
	if f.InstallationID == 0 {
		return fail("installation_missing", nil)
	}
	f.Reauthorized++
	if f.Reauthorized > 2 {
		return fail("oauth_loop", nil)
	}
	state, err := s.newState(ctx, w, f)
	if err != nil {
		return fail("state_store_failed", err)
	}
	q := url.Values{"client_id": {clientID}, "state": {state}, "redirect_uri": {s.setupURL()}}
	return s.webURL() + "/login/oauth/authorize?" + q.Encode()
}

// ---- integration view -----------------------------------------------------------------------

// Integration is the Settings > Integrations model.
type Integration struct {
	App           *githubAppSummary
	Installations []InstallationView
}

type githubAppSummary struct {
	Slug, HTMLURL string
}

// InstallationView is one linked installation with its enabled repo count.
type InstallationView struct {
	ticketrepo.Installation
	EnabledRepos int
	ManageURL    string
}

// Integration returns the org's GitHub connection.
func (s *Service) Integration(ctx context.Context, org uuid.UUID) (Integration, error) {
	var out Integration
	if c, err := s.creds(ctx); err == nil {
		out.App = &githubAppSummary{Slug: c.Slug, HTMLURL: c.HTMLURL}
	} else if !errors.Is(err, ErrAppNotConfigured) {
		return out, err
	}
	insts, err := s.Store.ListInstallations(ctx, org)
	if err != nil {
		return out, err
	}
	for _, i := range insts {
		v := InstallationView{Installation: i}
		if states, err := s.Store.RepoStates(ctx, org, i.InstallationID); err == nil {
			for _, st := range states {
				if st.Enabled {
					v.EnabledRepos++
				}
			}
		}
		if i.AccountType == "Organization" {
			v.ManageURL = fmt.Sprintf("%s/organizations/%s/settings/installations/%d", s.webURL(), url.PathEscape(i.AccountLogin), i.InstallationID)
		} else {
			v.ManageURL = fmt.Sprintf("%s/settings/installations/%d", s.webURL(), i.InstallationID)
		}
		out.Installations = append(out.Installations, v)
	}
	return out, nil
}

// Disconnect unlinks an installation locally (GitHub keeps it installed until the user removes it).
func (s *Service) Disconnect(ctx context.Context, org, user uuid.UUID, installationID int64) error {
	if _, err := s.Store.GetInstallation(ctx, org, installationID); err != nil {
		return err
	}
	if _, err := s.Store.MarkInstallationUninstalled(ctx, org, installationID); err != nil {
		return err
	}
	return s.Store.Audit(ctx, org, "user:"+user.String(), "github.installation_disconnected", "scm_installation",
		strconv.FormatInt(installationID, 10), nil)
}

func ownerRepo(full string) (string, string, error) {
	o, r, ok := strings.Cut(full, "/")
	if !ok || o == "" || r == "" || strings.Contains(r, "/") {
		return "", "", fmt.Errorf("onboarding: bad repo name %q", full)
	}
	return o, r, nil
}
