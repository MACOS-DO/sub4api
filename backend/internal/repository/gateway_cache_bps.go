package repository

import (
	"context"
	"errors"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/redis/go-redis/v9"
)

var _ service.OpenAIBPSStateStore = (*gatewayCache)(nil)

func (c *gatewayCache) BPSGet(ctx context.Context, key string) ([]byte, error) {
	data, err := c.rdb.GetEx(ctx, key, service.OpenAIBPSStateTTL).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	return data, err
}
func (c *gatewayCache) BPSPut(ctx context.Context, key string, data []byte) error {
	return c.rdb.Set(ctx, key, data, service.OpenAIBPSStateTTL).Err()
}

var bpsBindScript = redis.NewScript(`
local current=redis.call('GET',KEYS[1])
if not current then
 redis.call('SET',KEYS[1],ARGV[1],'PX',ARGV[2])
 return ARGV[1]
end
redis.call('PEXPIRE',KEYS[1],ARGV[2])
return current
`)

func (c *gatewayCache) BPSBind(ctx context.Context, key string, data []byte) ([]byte, error) {
	v, e := bpsBindScript.Run(ctx, c.rdb, []string{key}, data, service.OpenAIBPSStateTTL.Milliseconds()).Text()
	return []byte(v), e
}
