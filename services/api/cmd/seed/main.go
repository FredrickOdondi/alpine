// Command seed inserts the local dev tenant so the webhook demo, AUTH_DEV_LOGIN and AUTH_DEV_STUB
// work out of the box: org "acme", user dev@alpine.local (verified email + email identity, owner),
// a GitHub App installation + repo, a Sentry installation + project route, and the repo's
// .alpine/factory.yaml. Idempotent. Uses DATABASE_ADMIN_URL (migration owner, bypasses RLS).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/alpine-dev/alpine/packages/shared/config"
	"github.com/alpine-dev/alpine/packages/shared/factoryconfig"
)

// Fixed dev ids.
const (
	DevOrg  = "00000000-0000-4000-8000-000000000001"
	DevUser = "00000000-0000-4000-8000-000000000002"
)

func main() {
	ctx := context.Background()
	url := config.String("DATABASE_ADMIN_URL", "postgres://alpine:alpine@localhost:5432/alpine?sslmode=disable")
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		fail(err)
	}
	defer conn.Close(ctx)
	email := config.String("AUTH_DEV_LOGIN_EMAIL", "dev@alpine.local")
	installation := config.Int("SEED_GITHUB_INSTALLATION_ID", 42)
	repoID := config.Int("SEED_GITHUB_REPO_ID", 777)
	repoName := config.String("SEED_GITHUB_REPO", "acme/widgets")
	sentryInstallation := config.String("SEED_SENTRY_INSTALLATION", "dev-sentry-installation")
	sentryProject := config.String("SEED_SENTRY_PROJECT", "web-frontend")
	factoryPath := config.String("SEED_FACTORY_YAML", "../../.alpine/factory.yaml")

	type stmt struct {
		sql  string
		args []any
	}
	stmts := []stmt{
		{`INSERT INTO orgs (id, slug, name) VALUES ($1, 'acme', 'Acme Dev') ON CONFLICT (id) DO NOTHING`, []any{DevOrg}},
		{`INSERT INTO users (id, github_id, github_login, name, email, email_verified) VALUES ($1, 1, 'dev', 'Dev User', $2, true)
			ON CONFLICT (id) DO UPDATE SET email = EXCLUDED.email, email_verified = true`, []any{DevUser, email}},
		{`INSERT INTO user_identities (user_id, provider, subject, email, email_verified) VALUES ($1, 'email', $2, $2, true)
			ON CONFLICT DO NOTHING`, []any{DevUser, email}},
		{`INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner') ON CONFLICT DO NOTHING`, []any{DevOrg, DevUser}},
		{`INSERT INTO scm_installations (org_id, provider, installation_id, account_login) VALUES ($1, 'github', $2, 'acme')
			ON CONFLICT (provider, installation_id) DO NOTHING`, []any{DevOrg, installation}},
		{`INSERT INTO repos (org_id, installation_id, provider_repo_id, full_name, default_branch)
			SELECT $1, i.id, $3, $4, 'main' FROM scm_installations i WHERE i.provider = 'github' AND i.installation_id = $2
			ON CONFLICT DO NOTHING`, []any{DevOrg, installation, repoID, repoName}},
		{`INSERT INTO sentry_installations (org_id, installation_uuid, sentry_org_slug) VALUES ($1, $2, 'acme')
			ON CONFLICT (installation_uuid) DO NOTHING`, []any{DevOrg, sentryInstallation}},
		{`INSERT INTO sentry_project_repos (org_id, sentry_installation_id, sentry_project, repo_id)
			SELECT $1, s.id, $3, r.id FROM sentry_installations s, repos r
			WHERE s.installation_uuid = $2 AND r.org_id = $1 AND r.full_name = $4
			ON CONFLICT (org_id, sentry_project) DO NOTHING`, []any{DevOrg, sentryInstallation, sentryProject, repoName}},
	}
	if raw, err := os.ReadFile(factoryPath); err == nil {
		cfg, perr := factoryconfig.Parse(raw)
		if perr != nil {
			fail(fmt.Errorf("%s: %w", factoryPath, perr))
		}
		parsed, _ := json.Marshal(cfg)
		stmts = append(stmts,
			stmt{`INSERT INTO factory_configs (org_id, repo_id, version, source, commit_sha, parsed, raw_yaml)
				SELECT $1, r.id, 1, 'repo', 'seed', $3, $4 FROM repos r WHERE r.org_id = $1 AND r.full_name = $2
				ON CONFLICT (repo_id, version) DO UPDATE SET parsed = EXCLUDED.parsed, raw_yaml = EXCLUDED.raw_yaml`,
				[]any{DevOrg, repoName, parsed, string(raw)}},
			stmt{`UPDATE repos SET config_sha = 'seed' WHERE org_id = $1 AND full_name = $2`, []any{DevOrg, repoName}})
	} else {
		fmt.Fprintf(os.Stderr, "seed: skipping factory config (%v)\n", err)
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s.sql, s.args...); err != nil {
			fail(fmt.Errorf("%w\n%s", err, s.sql))
		}
	}
	fmt.Printf("seeded org %s (acme), user %s <%s> (owner), installation %d, repo %s (%d), sentry %s/%s\n",
		DevOrg, DevUser, email, installation, repoName, repoID, sentryInstallation, sentryProject)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "seed:", err)
	os.Exit(1)
}
