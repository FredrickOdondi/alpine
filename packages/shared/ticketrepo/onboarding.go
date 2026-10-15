package ticketrepo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alpine-dev/alpine/packages/shared/intake"
)

// ErrInstallationTaken is returned when an installation id is already linked to another org.
var ErrInstallationTaken = errors.New("ticketrepo: installation is linked to another workspace")

// Installation is a GitHub App installation linked to an org.
type Installation struct {
	ID                  uuid.UUID
	OrgID               uuid.UUID
	InstallationID      int64
	AccountLogin        string
	AccountType         string
	RepositorySelection *string
	Permissions         map[string]string
	SuspendedAt         *time.Time
	UninstalledAt       *time.Time
	LinkedBy            *uuid.UUID
	CreatedAt           time.Time
}

// LinkInstallationInput links a verified installation to an org.
type LinkInstallationInput struct {
	InstallationID      int64
	AccountLogin        string
	AccountType         string // "User" | "Organization"
	RepositorySelection string // "all" | "selected"
	Permissions         map[string]string
	LinkedBy            uuid.UUID
}

// LinkInstallation links (or re-links) an installation to org. It refuses an installation that
// any other org owns (checked across tenants with a SECURITY DEFINER lookup), and clears
// uninstalled_at/suspended_at when the same org links it again.
func (r *Store) LinkInstallation(ctx context.Context, org uuid.UUID, in LinkInstallationInput) (Installation, error) {
	var owner uuid.UUID
	err := r.DB.WithoutTenant(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT org_id FROM alpine_installation_owner('github', $1)`, in.InstallationID).Scan(&owner)
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Installation{}, err
	}
	if owner != uuid.Nil && owner != org {
		return Installation{}, ErrInstallationTaken
	}
	accountType := in.AccountType
	if accountType != "User" {
		accountType = "Organization"
	}
	var sel *string
	if in.RepositorySelection == "all" || in.RepositorySelection == "selected" {
		sel = &in.RepositorySelection
	}
	perms, _ := json.Marshal(orEmpty(in.Permissions))
	var by *uuid.UUID
	if in.LinkedBy != uuid.Nil {
		by = &in.LinkedBy
	}
	var out Installation
	err = r.tenant(ctx, org, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO scm_installations (org_id, provider, installation_id, account_login, account_type,
				permissions, repository_selection, linked_by)
			VALUES ($1, 'github', $2, $3, $4, $5, $6, $7)
			ON CONFLICT (provider, installation_id) DO UPDATE SET account_login = EXCLUDED.account_login,
				account_type = EXCLUDED.account_type, permissions = EXCLUDED.permissions,
				repository_selection = EXCLUDED.repository_selection, linked_by = EXCLUDED.linked_by,
				uninstalled_at = NULL, suspended_at = NULL
			WHERE scm_installations.org_id = EXCLUDED.org_id
			RETURNING `+installationCols, org, in.InstallationID, in.AccountLogin, accountType, perms, sel, by)
		var err error
		out, err = scanInstallation(row)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) { // conflict row belonged to another org (raced the check)
		return Installation{}, ErrInstallationTaken
	}
	return out, err
}

const installationCols = `id, org_id, installation_id, account_login, account_type, repository_selection, permissions,
	suspended_at, uninstalled_at, linked_by, created_at`

func scanInstallation(row pgx.Row) (Installation, error) {
	var i Installation
	var perms []byte
	err := row.Scan(&i.ID, &i.OrgID, &i.InstallationID, &i.AccountLogin, &i.AccountType, &i.RepositorySelection, &perms,
		&i.SuspendedAt, &i.UninstalledAt, &i.LinkedBy, &i.CreatedAt)
	if err == nil && len(perms) > 0 {
		_ = json.Unmarshal(perms, &i.Permissions)
	}
	return i, err
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// ListInstallations lists the org's GitHub installations that are still installed.
func (r *Store) ListInstallations(ctx context.Context, org uuid.UUID) ([]Installation, error) {
	var out []Installation
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+installationCols+` FROM scm_installations
			WHERE provider = 'github' AND uninstalled_at IS NULL ORDER BY created_at`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Installation, error) { return scanInstallation(row) })
		return err
	})
	return out, err
}

// GetInstallation returns the org's installation by GitHub installation id.
func (r *Store) GetInstallation(ctx context.Context, org uuid.UUID, installationID int64) (Installation, error) {
	var out Installation
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		var err error
		out, err = scanInstallation(tx.QueryRow(ctx, `SELECT `+installationCols+` FROM scm_installations
			WHERE provider = 'github' AND installation_id = $1 AND uninstalled_at IS NULL`, installationID))
		return err
	})
	return out, notFound(err)
}

// MarkInstallationUninstalled records an uninstall (webhook installation.deleted or a manual
// disconnect) and disables its repos. Tickets and history are kept.
func (r *Store) MarkInstallationUninstalled(ctx context.Context, org uuid.UUID, installationID int64) (bool, error) {
	var n int64
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE scm_installations SET uninstalled_at = now()
			WHERE provider = 'github' AND installation_id = $1 AND uninstalled_at IS NULL`, installationID)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		_, err = tx.Exec(ctx, `UPDATE repos SET enabled = false WHERE installation_id IN
			(SELECT id FROM scm_installations WHERE provider = 'github' AND installation_id = $1)`, installationID)
		return err
	})
	return n > 0, err
}

// SetInstallationSuspended records installation.suspend / unsuspend.
func (r *Store) SetInstallationSuspended(ctx context.Context, org uuid.UUID, installationID int64, suspended bool) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE scm_installations SET suspended_at = CASE WHEN $2 THEN now() ELSE NULL END
			WHERE provider = 'github' AND installation_id = $1`, installationID, suspended)
		return err
	})
}

// UpdateInstallationAccess refreshes the granted permissions / repository selection
// (installation.new_permissions_accepted, installation_repositories).
func (r *Store) UpdateInstallationAccess(ctx context.Context, org uuid.UUID, installationID int64, selection string, perms map[string]string) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		var sel *string
		if selection == "all" || selection == "selected" {
			sel = &selection
		}
		var pj []byte
		if perms != nil {
			pj, _ = json.Marshal(perms)
		}
		_, err := tx.Exec(ctx, `UPDATE scm_installations SET repository_selection = COALESCE($2, repository_selection),
			permissions = COALESCE($3::jsonb, permissions) WHERE provider = 'github' AND installation_id = $1`, installationID, sel, pj)
		return err
	})
}

// DisableReposByProviderID disables repos removed from an installation; returns how many.
func (r *Store) DisableReposByProviderID(ctx context.Context, org uuid.UUID, installationID int64, providerRepoIDs []int64) (int64, error) {
	var n int64
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE repos SET enabled = false WHERE enabled AND provider_repo_id = ANY($2)
			AND installation_id IN (SELECT id FROM scm_installations WHERE provider = 'github' AND installation_id = $1)`,
			installationID, providerRepoIDs)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// RepoState is the enablement state of one repo row.
type RepoState struct {
	ID             uuid.UUID
	ProviderRepoID int64
	FullName       string
	DefaultBranch  string
	Enabled        bool
	ConfigSHA      *string
}

// RepoStates returns the repo rows of one installation keyed by provider repo id.
func (r *Store) RepoStates(ctx context.Context, org uuid.UUID, installationID int64) (map[int64]RepoState, error) {
	out := map[int64]RepoState{}
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id, r.provider_repo_id, r.full_name, r.default_branch, r.enabled, r.config_sha
			FROM repos r JOIN scm_installations i ON i.id = r.installation_id
			WHERE i.provider = 'github' AND i.installation_id = $1 AND r.deleted_at IS NULL`, installationID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s RepoState
			if err := rows.Scan(&s.ID, &s.ProviderRepoID, &s.FullName, &s.DefaultBranch, &s.Enabled, &s.ConfigSHA); err != nil {
				return err
			}
			out[s.ProviderRepoID] = s
		}
		return rows.Err()
	})
	return out, err
}

// SetRepoEnabled creates or updates the repo row of an installation repo and sets enabled.
func (r *Store) SetRepoEnabled(ctx context.Context, org uuid.UUID, ref intake.RepoRef, enabled bool) (RepoState, error) {
	var s RepoState
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		branch := ref.DefaultBranch
		if branch == "" {
			branch = "main"
		}
		return tx.QueryRow(ctx, `INSERT INTO repos (org_id, installation_id, provider_repo_id, full_name, default_branch, enabled)
			SELECT $1, i.id, $3, $4, $5, $6 FROM scm_installations i
			WHERE i.provider = 'github' AND i.installation_id = $2 AND i.uninstalled_at IS NULL
			ON CONFLICT (installation_id, provider_repo_id) WHERE deleted_at IS NULL DO UPDATE SET full_name = EXCLUDED.full_name,
				default_branch = EXCLUDED.default_branch, enabled = EXCLUDED.enabled
			RETURNING id, provider_repo_id, full_name, default_branch, enabled, config_sha`,
			org, ref.InstallationID, ref.RepoID, ref.FullName, branch, enabled).
			Scan(&s.ID, &s.ProviderRepoID, &s.FullName, &s.DefaultBranch, &s.Enabled, &s.ConfigSHA)
	})
	return s, notFound(err)
}

// SaveFactoryConfig stores a new factory config version read from the repo and points the repo
// at it (config_sha).
func (r *Store) SaveFactoryConfig(ctx context.Context, org, repoID uuid.UUID, commitSHA, raw string, parsed any) error {
	pj, err := json.Marshal(parsed)
	if err != nil {
		return err
	}
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		var sameAsLatest bool
		_ = tx.QueryRow(ctx, `SELECT commit_sha = $2 AND raw_yaml = $3 FROM factory_configs WHERE repo_id = $1
			ORDER BY version DESC LIMIT 1`, repoID, commitSHA, raw).Scan(&sameAsLatest)
		if !sameAsLatest {
			if _, err := tx.Exec(ctx, `INSERT INTO factory_configs (org_id, repo_id, version, source, commit_sha, parsed, raw_yaml)
				SELECT $1, $2, COALESCE(MAX(version), 0) + 1, 'repo', $3, $4, $5 FROM factory_configs WHERE repo_id = $2`,
				org, repoID, commitSHA, pj, raw); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE repos SET config_sha = $2 WHERE id = $1`, repoID, commitSHA)
		return err
	})
}

// EnabledRepoForIntake returns the repo an intake event belongs to when it is enabled and its
// installation is active. reason is "" when ok, else "repo_not_enabled" / "installation_inactive"
// / "unknown_installation".
func (r *Store) EnabledRepoForIntake(ctx context.Context, org uuid.UUID, ref intake.RepoRef) (id uuid.UUID, reason string, err error) {
	err = r.tenant(ctx, org, func(tx pgx.Tx) error {
		var instUUID uuid.UUID
		var active bool
		err := tx.QueryRow(ctx, `SELECT id, suspended_at IS NULL AND uninstalled_at IS NULL FROM scm_installations
			WHERE provider = 'github' AND installation_id = $1`, ref.InstallationID).Scan(&instUUID, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			reason = "unknown_installation"
			return nil
		}
		if err != nil {
			return err
		}
		if !active {
			reason = "installation_inactive"
			return nil
		}
		var enabled bool
		err = tx.QueryRow(ctx, `SELECT id, enabled FROM repos WHERE installation_id = $1 AND provider_repo_id = $2
			AND deleted_at IS NULL`, instUUID, ref.RepoID).Scan(&id, &enabled)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !enabled) {
			id, reason = uuid.Nil, "repo_not_enabled"
			return nil
		}
		if err != nil {
			return err
		}
		// Keep the name current across renames (the provider id is the stable key).
		if ref.FullName != "" {
			_, err = tx.Exec(ctx, `UPDATE repos SET full_name = $2 WHERE id = $1 AND full_name <> $2`, id, ref.FullName)
		}
		return err
	})
	return id, reason, err
}
