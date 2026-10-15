// Package githubapp stores the platform GitHub App registration that an operator creates once
// through GitHub's App manifest flow (app id, slug, private key, webhook secret, OAuth client).
//
// Two stores: DBStore (github_app_registrations, secrets envelope-encrypted with the platform KEK)
// and FileStore (a 0600 JSON file, for local dev without a KEK). Load prefers explicit env
// credentials (GITHUB_APP_ID + GITHUB_APP_PRIVATE_KEY_FILE) so existing deployments keep working.
package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/keys"
)

// ErrNotConfigured means no GitHub App has been registered yet.
var ErrNotConfigured = errors.New("githubapp: no GitHub App registered")

// Credentials is a registered GitHub App. String() never prints secrets.
type Credentials struct {
	AppID         int64     `json:"app_id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	HTMLURL       string    `json:"html_url"`
	OwnerLogin    string    `json:"owner_login"`
	ClientID      string    `json:"client_id"`
	ClientSecret  string    `json:"client_secret"`
	WebhookSecret string    `json:"webhook_secret"`
	PrivateKeyPEM string    `json:"private_key_pem"`
	CreatedBy     uuid.UUID `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	// Source is "db", "file" or "env" (not persisted).
	Source string `json:"-"`
}

func (c Credentials) String() string {
	return fmt.Sprintf("githubapp.Credentials{app_id=%d slug=%s secrets=REDACTED}", c.AppID, c.Slug)
}

// GoString redacts secrets too.
func (c Credentials) GoString() string { return c.String() }

// BotLogin is the App's bot account ("<slug>[bot]").
func (c Credentials) BotLogin() string { return c.Slug + "[bot]" }

// Validate checks the fields every consumer needs.
func (c Credentials) Validate() error {
	switch {
	case c.AppID <= 0:
		return errors.New("githubapp: app id missing")
	case c.Slug == "":
		return errors.New("githubapp: slug missing")
	case c.PrivateKeyPEM == "":
		return errors.New("githubapp: private key missing")
	case c.WebhookSecret == "":
		return errors.New("githubapp: webhook secret missing")
	}
	return nil
}

// Store persists the registration.
type Store interface {
	Load(ctx context.Context) (Credentials, error)
	Save(ctx context.Context, c Credentials) error
}

type secrets struct {
	PrivateKeyPEM string `json:"private_key_pem"`
	WebhookSecret string `json:"webhook_secret"`
	ClientSecret  string `json:"client_secret"`
}

const (
	kekOrg     = "platform"
	kekPurpose = "github_app"
)

// DBStore keeps the registration in github_app_registrations with secrets envelope-encrypted.
type DBStore struct {
	DB  *db.DB
	KEK keys.KEK
}

// Save implements Store (upsert of the single row).
func (s DBStore) Save(ctx context.Context, c Credentials) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(secrets{PrivateKeyPEM: c.PrivateKeyPEM, WebhookSecret: c.WebhookSecret, ClientSecret: c.ClientSecret})
	env, err := keys.Seal(ctx, s.KEK, kekOrg, kekPurpose, string(b))
	if err != nil {
		return err
	}
	var by *uuid.UUID
	if c.CreatedBy != uuid.Nil {
		by = &c.CreatedBy
	}
	return s.DB.WithoutTenant(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO github_app_registrations
			(id, app_id, slug, name, html_url, owner_login, client_id, secrets_ciphertext, secrets_data_key, kek_id, created_by)
			VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (id) DO UPDATE SET app_id = EXCLUDED.app_id, slug = EXCLUDED.slug, name = EXCLUDED.name,
			  html_url = EXCLUDED.html_url, owner_login = EXCLUDED.owner_login, client_id = EXCLUDED.client_id,
			  secrets_ciphertext = EXCLUDED.secrets_ciphertext, secrets_data_key = EXCLUDED.secrets_data_key,
			  kek_id = EXCLUDED.kek_id, created_by = EXCLUDED.created_by, created_at = now()`,
			c.AppID, c.Slug, c.Name, c.HTMLURL, c.OwnerLogin, c.ClientID, env.Ciphertext, env.DataKeyCiphertext, env.KEKID, by)
		return err
	})
}

// Load implements Store.
func (s DBStore) Load(ctx context.Context) (Credentials, error) {
	var c Credentials
	var env keys.Envelope
	var by *uuid.UUID
	err := s.DB.WithoutTenant(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT app_id, slug, name, html_url, owner_login, client_id, secrets_ciphertext,
			secrets_data_key, kek_id, created_by, created_at FROM github_app_registrations WHERE id = 1`).
			Scan(&c.AppID, &c.Slug, &c.Name, &c.HTMLURL, &c.OwnerLogin, &c.ClientID, &env.Ciphertext, &env.DataKeyCiphertext,
				&env.KEKID, &by, &c.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Credentials{}, ErrNotConfigured
	}
	if err != nil {
		return Credentials{}, err
	}
	if by != nil {
		c.CreatedBy = *by
	}
	if env.KEKID != s.KEK.ID() {
		return Credentials{}, fmt.Errorf("githubapp: secrets sealed with KEK %q, have %q", env.KEKID, s.KEK.ID())
	}
	pt, err := keys.Open(ctx, s.KEK, kekOrg, kekPurpose, env)
	if err != nil {
		return Credentials{}, err
	}
	var sec secrets
	if err := json.Unmarshal([]byte(pt.Reveal()), &sec); err != nil {
		return Credentials{}, fmt.Errorf("githubapp: decode secrets: %w", err)
	}
	c.PrivateKeyPEM, c.WebhookSecret, c.ClientSecret = sec.PrivateKeyPEM, sec.WebhookSecret, sec.ClientSecret
	c.Source = "db"
	return c, nil
}

// FileStore keeps the registration in a local JSON file (mode 0600). Dev only: the secrets are
// plaintext on disk, so it is used only when no KEK is configured.
type FileStore struct {
	Path string
}

// Save implements Store.
func (s FileStore) Save(_ context.Context, c Credentials) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Load implements Store.
func (s FileStore) Load(_ context.Context) (Credentials, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, ErrNotConfigured
	}
	if err != nil {
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return Credentials{}, fmt.Errorf("githubapp: %s: %w", s.Path, err)
	}
	c.Source = "file"
	return c, c.Validate()
}

// EnvCredentials returns credentials from GITHUB_APP_ID / GITHUB_APP_BOT_LOGIN /
// GITHUB_APP_PRIVATE_KEY_FILE / GITHUB_WEBHOOK_SECRET when GITHUB_APP_ID is set (ok=false otherwise).
func EnvCredentials(getenv func(string) string) (Credentials, bool, error) {
	idStr := getenv("GITHUB_APP_ID")
	if idStr == "" {
		return Credentials{}, false, nil
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		return Credentials{}, true, errors.New("githubapp: GITHUB_APP_ID must be a positive integer")
	}
	pem, err := os.ReadFile(getenv("GITHUB_APP_PRIVATE_KEY_FILE"))
	if err != nil {
		return Credentials{}, true, fmt.Errorf("githubapp: GITHUB_APP_PRIVATE_KEY_FILE: %w", err)
	}
	slug := getenv("GITHUB_APP_SLUG")
	if bot := getenv("GITHUB_APP_BOT_LOGIN"); slug == "" && len(bot) > 5 {
		slug = bot[:len(bot)-len("[bot]")]
	}
	c := Credentials{AppID: id, Slug: slug, Name: slug, PrivateKeyPEM: string(pem), WebhookSecret: getenv("GITHUB_WEBHOOK_SECRET"),
		ClientID: getenv("GITHUB_APP_CLIENT_ID"), ClientSecret: getenv("GITHUB_APP_CLIENT_SECRET"), Source: "env"}
	if c.Slug != "" {
		c.HTMLURL = "https://github.com/apps/" + c.Slug
	}
	return c, true, c.Validate()
}

// Cached wraps a Store with an in-memory copy that Save refreshes (the API reads the webhook
// secret on every delivery). Env credentials, when present, win and cannot be overwritten.
type Cached struct {
	Store Store
	Env   *Credentials

	mu     sync.RWMutex
	cur    *Credentials
	loaded time.Time
	TTL    time.Duration // re-read interval (default 30s), so other processes' saves are seen
}

// Load returns the current credentials (ErrNotConfigured when none).
func (c *Cached) Load(ctx context.Context) (Credentials, error) {
	if c.Env != nil {
		return *c.Env, nil
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	c.mu.RLock()
	cur, fresh := c.cur, time.Since(c.loaded) < ttl
	c.mu.RUnlock()
	if cur != nil && fresh {
		return *cur, nil
	}
	got, err := c.Store.Load(ctx)
	if err != nil {
		if errors.Is(err, ErrNotConfigured) {
			return Credentials{}, err
		}
		if cur != nil { // keep serving the last good copy on transient errors
			return *cur, nil
		}
		return Credentials{}, err
	}
	c.mu.Lock()
	c.cur, c.loaded = &got, time.Now()
	c.mu.Unlock()
	return got, nil
}

// Save persists and refreshes the cache.
func (c *Cached) Save(ctx context.Context, cr Credentials) error {
	if c.Env != nil {
		return errors.New("githubapp: credentials come from GITHUB_APP_* env; unset them to register an App here")
	}
	if err := c.Store.Save(ctx, cr); err != nil {
		return err
	}
	c.mu.Lock()
	c.cur, c.loaded = &cr, time.Now()
	c.mu.Unlock()
	return nil
}

// Open builds the standard store from env-style settings: env credentials if GITHUB_APP_ID is set,
// else DBStore when a KEK is available, else FileStore at filePath.
func Open(getenv func(string) string, database *db.DB, kek keys.KEK, filePath string) (*Cached, error) {
	env, ok, err := EnvCredentials(getenv)
	if err != nil {
		return nil, err
	}
	if ok {
		return &Cached{Env: &env}, nil
	}
	if kek != nil && database != nil {
		return &Cached{Store: DBStore{DB: database, KEK: kek}}, nil
	}
	return &Cached{Store: FileStore{Path: filePath}}, nil
}
