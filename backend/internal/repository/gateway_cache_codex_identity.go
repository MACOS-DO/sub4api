package repository

import (
	"context"
	"errors"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/redis/go-redis/v9"
)

var _ service.CodexGatewayIdentityCache = (*gatewayCache)(nil)

func (c *gatewayCache) GetCodexIdentity(ctx context.Context, key string) (string, error) {
	value, err := c.rdb.Get(ctx, "codex_gateway_identity:"+key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return value, err
}
func (c *gatewayCache) PutCodexIdentities(ctx context.Context, values map[string]string, ttl time.Duration) error {
	pipe := c.rdb.Pipeline()
	for key, value := range values {
		pipe.Set(ctx, "codex_gateway_identity:"+key, value, ttl)
	}
	_, err := pipe.Exec(ctx)
	return err
}
