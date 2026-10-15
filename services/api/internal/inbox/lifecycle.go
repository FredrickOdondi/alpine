package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
	"strings"
)

// lifecycle applies GitHub App installation webhooks. These never create tickets:
//
//	installation.deleted                      -> installation uninstalled, its repos disabled
//	installation.suspend / unsuspend          -> suspended_at set / cleared (no tickets while suspended)
//	installation.new_permissions_accepted     -> permissions snapshot refreshed
//	installation_repositories.removed         -> those repos disabled
//	installation_repositories.added           -> selection refreshed; new repos stay disabled until
//	                                             someone enables them in the repo picker
//	installation.created                      -> nothing: linking happens only through the verified
//	                                             setup callback (an installation event cannot prove
//	                                             which workspace it belongs to)
func (p *Processor) lifecycle(ctx context.Context, ev ticketrepo.InboxEvent) (string, error) {
	if p.Installations == nil {
		return "ignored:lifecycle_not_configured", nil
	}
	var body struct {
		Action       string `json:"action"`
		Installation struct {
			ID                  int64             `json:"id"`
			RepositorySelection string            `json:"repository_selection"`
			Permissions         map[string]string `json:"permissions"`
		} `json:"installation"`
		RepositorySelection string `json:"repository_selection"`
		RepositoriesRemoved []struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		} `json:"repositories_removed"`
		RepositoriesAdded []struct {
			ID int64 `json:"id"`
		} `json:"repositories_added"`
	}
	if err := json.Unmarshal(ev.Payload, &body); err != nil || body.Installation.ID <= 0 {
		return "rejected:bad_installation_payload", nil
	}
	org, id := ev.OrgID, body.Installation.ID
	switch ev.EventType + "." + body.Action {
	case "installation.deleted":
		changed, err := p.Installations.MarkInstallationUninstalled(ctx, org, id)
		if err != nil {
			return "", err
		}
		if !changed {
			return "installation:already_uninstalled", nil
		}
		return "installation:uninstalled", nil
	case "installation.suspend", "installation.unsuspend":
		if err := p.Installations.SetInstallationSuspended(ctx, org, id, body.Action == "suspend"); err != nil {
			return "", err
		}
		return "installation:" + body.Action, nil
	case "installation.new_permissions_accepted", "installation.created":
		if err := p.Installations.UpdateInstallationAccess(ctx, org, id, body.Installation.RepositorySelection, body.Installation.Permissions); err != nil {
			return "", err
		}
		return "installation:access_updated", nil
	case "installation_repositories.removed":
		ids := make([]int64, 0, len(body.RepositoriesRemoved))
		for _, r := range body.RepositoriesRemoved {
			ids = append(ids, r.ID)
		}
		n, err := p.Installations.DisableReposByProviderID(ctx, org, id, ids)
		if err != nil {
			return "", err
		}
		if err := p.Installations.UpdateInstallationAccess(ctx, org, id, body.RepositorySelection, nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("installation:repos_removed:%d_disabled", n), nil
	case "installation_repositories.added":
		if err := p.Installations.UpdateInstallationAccess(ctx, org, id, body.RepositorySelection, nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("installation:repos_added:%d", len(body.RepositoriesAdded)), nil
	default:
		return "ignored:" + strings.TrimSuffix(ev.EventType+"."+body.Action, "."), nil
	}
}
