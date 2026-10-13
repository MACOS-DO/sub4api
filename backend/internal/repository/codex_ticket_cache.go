package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"

	"github.com/redis/go-redis/v9"
)

const (
	// v3 holds the single global ticket as a hash {v: version, d: state JSON}; the
	// older per-model keys are left to expire.
	codexSharedTicketKey     = "codex:ticket:shared:v3"
	codexTicketLockKeyPrefix = "codex:ticket:"
)

// codexSharedTicketSetScript writes the state unless the cached version is newer,
// so a reader that loaded the database before a write cannot overwrite it later.
var codexSharedTicketSetScript = redis.NewScript(`
local current = redis.call("HGET", KEYS[1], "v")
if current and tonumber(current) > tonumber(ARGV[1]) then
  return 0
end
redis.call("HSET", KEYS[1], "v", ARGV[1], "d", ARGV[2])
redis.call("PEXPIRE", KEYS[1], ARGV[3])
return 1
`)

// codexTicketLockExtendScript extends a lock only while the caller still owns it.
var codexTicketLockExtendScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)

type codexSharedTicketCache struct {
	rdb *redis.Client
}

// NewCodexSharedTicketCache returns the Redis cache for the global shared Codex ticket.
func NewCodexSharedTicketCache(rdb *redis.Client) service.CodexSharedTicketCache {
	return &codexSharedTicketCache{rdb: rdb}
}

func (c *codexSharedTicketCache) GetSharedTicket(ctx context.Context) (*service.CodexSharedTicketState, bool, error) {
	data, err := c.rdb.HGet(ctx, codexSharedTicketKey, "d").Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var state service.CodexSharedTicketState
	if err := json.Unmarshal(data, &state); err != nil {
		// A corrupt entry is a miss; the loader overwrites it from the database.
		return nil, false, nil
	}
	return &state, true, nil
}

func (c *codexSharedTicketCache) SetSharedTicket(ctx context.Context, state *service.CodexSharedTicketState, ttl time.Duration) error {
	if state == nil {
		state = &service.CodexSharedTicketState{}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return codexSharedTicketSetScript.Run(ctx, c.rdb, []string{codexSharedTicketKey}, state.Version, data, ttl.Milliseconds()).Err()
}

func (c *codexSharedTicketCache) DeleteSharedTicket(ctx context.Context) error {
	return c.rdb.Del(ctx, codexSharedTicketKey).Err()
}

func (c *codexSharedTicketCache) TryLock(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, codexTicketLockKeyPrefix+name, owner, ttl).Result()
}

func (c *codexSharedTicketCache) ExtendLock(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	extended, err := codexTicketLockExtendScript.Run(ctx, c.rdb, []string{codexTicketLockKeyPrefix + name}, owner, ttl.Milliseconds()).Int()
	return extended == 1, err
}

// Unlock releases the lock only while the caller still owns it
// (same compare-and-delete script as the leader lock).
func (c *codexSharedTicketCache) Unlock(ctx context.Context, name, owner string) error {
	return leaderLockReleaseScript.Run(ctx, c.rdb, []string{codexTicketLockKeyPrefix + name}, owner).Err()
}
