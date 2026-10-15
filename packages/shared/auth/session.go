package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Session is a server-side session stored in Redis. The browser only holds an opaque random id in
// an HttpOnly cookie; Redis keys are HMAC(SESSION_SECRET, id), so a Redis dump yields no usable
// cookies.
type Session struct {
	Key         string    `json:"-"` // hashed id (Redis key suffix)
	UserID      uuid.UUID `json:"uid"`
	ActiveOrgID uuid.UUID `json:"org"`
	CSRFToken   string    `json:"csrf"`
	AuthMethod  string    `json:"m"`
	CreatedAt   time.Time `json:"c"`
	LastSeenAt  time.Time `json:"s"`
	ExpiresAt   time.Time `json:"e"` // absolute expiry
}

// ErrNoSession is returned for unknown, expired or revoked sessions.
var ErrNoSession = errors.New("auth: no session")

// SessionStore persists sessions in Redis with idle and absolute expiry and a per-user index for
// "log out everywhere".
type SessionStore struct {
	Redis       redis.UniversalClient
	Secret      []byte
	IdleTTL     time.Duration // default 7 days
	AbsoluteTTL time.Duration // default 30 days
	Prefix      string        // default "alpine:auth:"
	Now         func() time.Time
}

func (s *SessionStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *SessionStore) idle() time.Duration {
	if s.IdleTTL > 0 {
		return s.IdleTTL
	}
	return 7 * 24 * time.Hour
}

func (s *SessionStore) absolute() time.Duration {
	if s.AbsoluteTTL > 0 {
		return s.AbsoluteTTL
	}
	return 30 * 24 * time.Hour
}

func (s *SessionStore) prefix() string {
	if s.Prefix != "" {
		return s.Prefix
	}
	return "alpine:auth:"
}

func (s *SessionStore) sessKey(hashed string) string { return s.prefix() + "sess:" + hashed }
func (s *SessionStore) userKey(u uuid.UUID) string   { return s.prefix() + "usess:" + u.String() }
func (s *SessionStore) hash(rawID string) string     { return keyed(s.Secret, "session", rawID) }

// Create starts a new session and returns it with the raw id to put in the cookie.
func (s *SessionStore) Create(ctx context.Context, userID, orgID uuid.UUID, method string) (*Session, string, error) {
	raw := RandomToken(32)
	now := s.now()
	sess := &Session{
		Key: s.hash(raw), UserID: userID, ActiveOrgID: orgID, CSRFToken: RandomToken(32), AuthMethod: method,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(s.absolute()),
	}
	if err := s.write(ctx, sess); err != nil {
		return nil, "", err
	}
	if err := s.Redis.SAdd(ctx, s.userKey(userID), sess.Key).Err(); err != nil {
		return nil, "", fmt.Errorf("auth: index session: %w", err)
	}
	_ = s.Redis.Expire(ctx, s.userKey(userID), s.absolute()).Err()
	return sess, raw, nil
}

func (s *SessionStore) write(ctx context.Context, sess *Session) error {
	b, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	ttl := min(s.idle(), sess.ExpiresAt.Sub(s.now()))
	if ttl <= 0 {
		return ErrNoSession
	}
	if err := s.Redis.Set(ctx, s.sessKey(sess.Key), b, ttl).Err(); err != nil {
		return fmt.Errorf("auth: store session: %w", err)
	}
	return nil
}

// Get loads a session by raw cookie value, enforcing expiry and sliding the idle timeout (at most
// one write per minute).
func (s *SessionStore) Get(ctx context.Context, rawID string) (*Session, error) {
	if rawID == "" || len(rawID) > 128 {
		return nil, ErrNoSession
	}
	key := s.hash(rawID)
	b, err := s.Redis.Get(ctx, s.sessKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("auth: load session: %w", err)
	}
	var sess Session
	if err := json.Unmarshal(b, &sess); err != nil {
		return nil, ErrNoSession
	}
	sess.Key = key
	now := s.now()
	if !now.Before(sess.ExpiresAt) {
		_ = s.deleteKey(ctx, sess.UserID, key)
		return nil, ErrNoSession
	}
	if now.Sub(sess.LastSeenAt) > time.Minute {
		sess.LastSeenAt = now
		if err := s.write(ctx, &sess); err != nil && !errors.Is(err, ErrNoSession) {
			return nil, err
		}
	}
	return &sess, nil
}

// SetActiveOrg switches the session's active org.
func (s *SessionStore) SetActiveOrg(ctx context.Context, sess *Session, orgID uuid.UUID) error {
	sess.ActiveOrgID = orgID
	return s.write(ctx, sess)
}

// Delete revokes one session by raw id (no error if unknown).
func (s *SessionStore) Delete(ctx context.Context, rawID string) error {
	if rawID == "" {
		return nil
	}
	key := s.hash(rawID)
	b, err := s.Redis.Get(ctx, s.sessKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		return err
	}
	var sess Session
	_ = json.Unmarshal(b, &sess)
	return s.deleteKey(ctx, sess.UserID, key)
}

// DeleteByKey revokes a session by its hashed key.
func (s *SessionStore) DeleteByKey(ctx context.Context, userID uuid.UUID, key string) error {
	return s.deleteKey(ctx, userID, key)
}

func (s *SessionStore) deleteKey(ctx context.Context, userID uuid.UUID, key string) error {
	pipe := s.Redis.TxPipeline()
	pipe.Del(ctx, s.sessKey(key))
	if userID != uuid.Nil {
		pipe.SRem(ctx, s.userKey(userID), key)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// DeleteAllForUser revokes every session of a user ("log out everywhere"); returns how many.
func (s *SessionStore) DeleteAllForUser(ctx context.Context, userID uuid.UUID) (int, error) {
	keys, err := s.Redis.SMembers(ctx, s.userKey(userID)).Result()
	if err != nil {
		return 0, err
	}
	pipe := s.Redis.TxPipeline()
	for _, k := range keys {
		pipe.Del(ctx, s.sessKey(k))
	}
	pipe.Del(ctx, s.userKey(userID))
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return len(keys), nil
}

// CountForUser returns how many sessions the user index holds (may include expired ones).
func (s *SessionStore) CountForUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	return s.Redis.SCard(ctx, s.userKey(userID)).Result()
}
