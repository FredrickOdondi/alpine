package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisIdempotency stores completed responses in Redis under an org-namespaced key with a TTL
// long enough to cover Temporal activity retries.
type RedisIdempotency struct {
	Redis *redis.Client
	TTL   time.Duration // default 7 days
}

func (s *RedisIdempotency) key(orgID, key string) string {
	sum := sha256.Sum256([]byte(key))
	return "alpine:llm:idem:" + orgID + ":" + hex.EncodeToString(sum[:])
}

// Get implements IdempotencyStore.
func (s *RedisIdempotency) Get(ctx context.Context, orgID, key string) (*Response, bool, error) {
	raw, err := s.Redis.Get(ctx, s.key(orgID, key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var r Response
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, false, err
	}
	return &r, true, nil
}

// Put implements IdempotencyStore.
func (s *RedisIdempotency) Put(ctx context.Context, orgID, key string, resp *Response) error {
	ttl := s.TTL
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return s.Redis.Set(ctx, s.key(orgID, key), raw, ttl).Err()
}
