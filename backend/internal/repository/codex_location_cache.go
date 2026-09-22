package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"

	"github.com/redis/go-redis/v9"
)

const codexLocationCacheKeyPrefix = "codex:loc:v1:account:"

type codexLocationCache struct {
	rdb *redis.Client

	mu sync.Mutex
	// direct 只保存无代理账号（ProxyID == 0）的出口地理：同一账号在不同副本上
	// 直连出口可能不同，不能写进共享 Redis。
	direct map[int64]codexLocationDirectEntry
}

type codexLocationDirectEntry struct {
	record    service.CodexLocationCacheRecord
	expiresAt time.Time
}

func NewCodexLocationCache(rdb *redis.Client) service.CodexLocationCache {
	return &codexLocationCache{
		rdb:    rdb,
		direct: make(map[int64]codexLocationDirectEntry),
	}
}

func codexLocationCacheKey(accountID int64) string {
	return fmt.Sprintf("%s%d", codexLocationCacheKeyPrefix, accountID)
}

func (c *codexLocationCache) GetAccountLocation(ctx context.Context, accountID int64) (*service.CodexLocationCacheRecord, error) {
	if rec := c.getDirect(accountID); rec != nil {
		return rec, nil
	}
	if c.rdb == nil {
		return nil, nil
	}
	raw, err := c.rdb.Get(ctx, codexLocationCacheKey(accountID)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record service.CodexLocationCacheRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *codexLocationCache) SetAccountLocation(ctx context.Context, accountID int64, record *service.CodexLocationCacheRecord, ttl time.Duration) error {
	if record == nil {
		return nil
	}
	if record.ProxyID == 0 {
		c.setDirect(accountID, *record, ttl)
		return nil
	}
	// 代理绑定记录写共享 Redis 前必须清除进程内直连条目：直连条目读取优先级更高，
	// 账号从无代理改绑代理后若残留，会因指纹不匹配而每次都重新探测并永久遮蔽新记录。
	c.deleteDirect(accountID)
	if c.rdb == nil {
		return nil
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, codexLocationCacheKey(accountID), payload, ttl).Err()
}

func (c *codexLocationCache) DeleteAccountLocation(ctx context.Context, accountID int64) error {
	c.deleteDirect(accountID)
	if c.rdb == nil {
		return nil
	}
	return c.rdb.Del(ctx, codexLocationCacheKey(accountID)).Err()
}

func (c *codexLocationCache) DeleteAccountLocations(ctx context.Context, accountIDs []int64) error {
	if len(accountIDs) == 0 {
		return nil
	}
	for _, accountID := range accountIDs {
		c.deleteDirect(accountID)
	}
	if c.rdb == nil {
		return nil
	}
	keys := make([]string, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		keys = append(keys, codexLocationCacheKey(accountID))
	}
	return c.rdb.Del(ctx, keys...).Err()
}

func (c *codexLocationCache) getDirect(accountID int64) *service.CodexLocationCacheRecord {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.direct[accountID]
	if !ok {
		return nil
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		delete(c.direct, accountID)
		return nil
	}
	record := entry.record
	return &record
}

func (c *codexLocationCache) setDirect(accountID int64, record service.CodexLocationCacheRecord, ttl time.Duration) {
	if c == nil {
		return
	}
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.direct[accountID] = codexLocationDirectEntry{record: record, expiresAt: expiresAt}
}

func (c *codexLocationCache) deleteDirect(accountID int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.direct, accountID)
}
