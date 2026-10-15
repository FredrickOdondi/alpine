// Package keys implements envelope encryption for tenant secrets (BYO OpenAI keys first, repo
// secrets later). Each secret is encrypted with a fresh 256-bit data key (AES-256-GCM, AAD bound
// to the org and purpose); the data key is encrypted by a key-encryption key (KEK): AWS KMS in
// production (per-tenant key ARN, architecture §8) or a local AES key in development.
package keys

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/llm"
)

// KEK encrypts/decrypts data keys. aad binds a ciphertext to its tenant.
type KEK interface {
	ID() string
	Encrypt(ctx context.Context, plaintext, aad []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext, aad []byte) ([]byte, error)
}

// LocalKEK is an AES-256-GCM KEK from a base64 32-byte key (ALPINE_LOCAL_KEK). Dev/test only.
type LocalKEK struct {
	id  string
	gcm cipher.AEAD
}

// NewLocalKEK parses a base64-encoded 32-byte key.
func NewLocalKEK(id, b64 string) (*LocalKEK, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("keys: decode KEK: %w", err)
	}
	if len(raw) != 32 {
		return nil, errors.New("keys: KEK must be 32 bytes (base64)")
	}
	gcm, err := newGCM(raw)
	if err != nil {
		return nil, err
	}
	return &LocalKEK{id: id, gcm: gcm}, nil
}

// ID implements KEK.
func (k *LocalKEK) ID() string { return k.id }

// Encrypt implements KEK.
func (k *LocalKEK) Encrypt(_ context.Context, plaintext, aad []byte) ([]byte, error) {
	return seal(k.gcm, plaintext, aad)
}

// Decrypt implements KEK.
func (k *LocalKEK) Decrypt(_ context.Context, ciphertext, aad []byte) ([]byte, error) {
	return open(k.gcm, ciphertext, aad)
}

// Envelope is what gets stored (model_keys row).
type Envelope struct {
	Ciphertext        []byte
	DataKeyCiphertext []byte
	KEKID             string
	Last4             string
}

func aad(orgID, purpose string) []byte { return []byte("alpine:" + purpose + ":" + orgID) }

// Seal encrypts plaintext for orgID/purpose (e.g. "model_key:openai").
func Seal(ctx context.Context, kek KEK, orgID, purpose string, plaintext string) (Envelope, error) {
	if plaintext == "" {
		return Envelope{}, errors.New("keys: empty secret")
	}
	dk := make([]byte, 32)
	if _, err := rand.Read(dk); err != nil {
		return Envelope{}, err
	}
	defer clear(dk)
	gcm, err := newGCM(dk)
	if err != nil {
		return Envelope{}, err
	}
	a := aad(orgID, purpose)
	ct, err := seal(gcm, []byte(plaintext), a)
	if err != nil {
		return Envelope{}, err
	}
	wrapped, err := kek.Encrypt(ctx, dk, a)
	if err != nil {
		return Envelope{}, fmt.Errorf("keys: wrap data key: %w", err)
	}
	last4 := plaintext
	if len(last4) > 4 {
		last4 = last4[len(last4)-4:]
	}
	for len(last4) < 4 {
		last4 = "*" + last4
	}
	return Envelope{Ciphertext: ct, DataKeyCiphertext: wrapped, KEKID: kek.ID(), Last4: last4}, nil
}

// Open decrypts an envelope; the result is a llm.Secret so it cannot be logged by accident.
func Open(ctx context.Context, kek KEK, orgID, purpose string, env Envelope) (llm.Secret, error) {
	a := aad(orgID, purpose)
	dk, err := kek.Decrypt(ctx, env.DataKeyCiphertext, a)
	if err != nil {
		return "", fmt.Errorf("keys: unwrap data key: %w", err)
	}
	defer clear(dk)
	gcm, err := newGCM(dk)
	if err != nil {
		return "", err
	}
	pt, err := open(gcm, env.Ciphertext, a)
	if err != nil {
		return "", fmt.Errorf("keys: decrypt: %w", err)
	}
	return llm.Secret(pt), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func seal(gcm cipher.AEAD, pt, aad []byte) ([]byte, error) {
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, pt, aad), nil
}

func open(gcm cipher.AEAD, ct, aad []byte) ([]byte, error) {
	if len(ct) < gcm.NonceSize() {
		return nil, errors.New("keys: ciphertext too short")
	}
	return gcm.Open(nil, ct[:gcm.NonceSize()], ct[gcm.NonceSize():], aad)
}

// Store loads the active envelope for an org's provider key.
type Store interface {
	ActiveModelKey(ctx context.Context, orgID, provider string) (*Envelope, error) // nil, nil if none
}

// Resolver implements llm.KeyResolver over a Store with a short per-org in-memory cache.
type Resolver struct {
	Store    Store
	KEK      KEK
	Provider string // "openai"
	// AllowPlatformKey lets orgs without a BYO key use the platform key configured in LiteLLM.
	AllowPlatformKey bool
	TTL              time.Duration
	Now              func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	key     llm.Secret
	byok    bool
	expires time.Time
}

var _ llm.KeyResolver = (*Resolver)(nil)

// Resolve implements llm.KeyResolver.
func (r *Resolver) Resolve(ctx context.Context, orgID string) (llm.Secret, bool, error) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	ttl := r.TTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	r.mu.Lock()
	if c, ok := r.cache[orgID]; ok && now().Before(c.expires) {
		r.mu.Unlock()
		return c.key, c.byok, nil
	}
	r.mu.Unlock()

	env, err := r.Store.ActiveModelKey(ctx, orgID, r.Provider)
	if err != nil {
		return "", false, fmt.Errorf("keys: load model key: %w", err)
	}
	var c cached
	if env != nil {
		k, err := Open(ctx, r.KEK, orgID, "model_key:"+r.Provider, *env)
		if err != nil {
			return "", false, err
		}
		c = cached{key: k, byok: true}
	} else if !r.AllowPlatformKey {
		return "", false, llm.ErrNoKey
	}
	c.expires = now().Add(ttl)
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]cached{}
	}
	r.cache[orgID] = c
	r.mu.Unlock()
	return c.key, c.byok, nil
}

// Invalidate drops an org's cached key (after rotation/revocation).
func (r *Resolver) Invalidate(orgID string) {
	r.mu.Lock()
	delete(r.cache, orgID)
	r.mu.Unlock()
}
