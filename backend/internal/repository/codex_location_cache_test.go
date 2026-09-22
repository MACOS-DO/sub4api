package repository

import (
	"context"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestCodexLocationCache(t *testing.T) (service.CodexLocationCache, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewCodexLocationCache(client), server
}

func TestCodexLocationCacheProxyRecordsUseRedis(t *testing.T) {
	cache, server := newTestCodexLocationCache(t)
	record := &service.CodexLocationCacheRecord{Timezone: "America/New_York", ProxyID: 100, Success: true}
	require.NoError(t, cache.SetAccountLocation(context.Background(), 1, record, time.Hour))
	require.True(t, server.Exists(codexLocationCacheKey(1)))

	got, err := cache.GetAccountLocation(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "America/New_York", got.Timezone)

	require.NoError(t, cache.DeleteAccountLocation(context.Background(), 1))
	got, err = cache.GetAccountLocation(context.Background(), 1)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestCodexLocationCacheDirectRecordsAreProcessLocal(t *testing.T) {
	cache, server := newTestCodexLocationCache(t)
	record := &service.CodexLocationCacheRecord{Timezone: "Asia/Tokyo", ProxyID: 0, Success: true}
	require.NoError(t, cache.SetAccountLocation(context.Background(), 2, record, time.Hour))
	// 无代理记录不能写共享 Redis：不同副本的直连出口可能不同。
	require.False(t, server.Exists(codexLocationCacheKey(2)))

	got, err := cache.GetAccountLocation(context.Background(), 2)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "Asia/Tokyo", got.Timezone)

	require.NoError(t, cache.DeleteAccountLocation(context.Background(), 2))
	got, err = cache.GetAccountLocation(context.Background(), 2)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestCodexLocationCacheDirectRecordExpires(t *testing.T) {
	cache, _ := newTestCodexLocationCache(t)
	record := &service.CodexLocationCacheRecord{Timezone: "Asia/Tokyo", ProxyID: 0, Success: true}
	require.NoError(t, cache.SetAccountLocation(context.Background(), 3, record, time.Millisecond))
	time.Sleep(5 * time.Millisecond)
	got, err := cache.GetAccountLocation(context.Background(), 3)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestCodexLocationCacheProxyWriteEvictsDirectEntry(t *testing.T) {
	cache, server := newTestCodexLocationCache(t)
	require.NoError(t, cache.SetAccountLocation(context.Background(), 1, &service.CodexLocationCacheRecord{
		Timezone: "Asia/Tokyo",
		ProxyID:  0,
		Success:  true,
	}, time.Hour))
	// 账号改绑代理之前，直连记录可命中的确有效。
	direct, err := cache.GetAccountLocation(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, direct)
	require.Equal(t, int64(0), direct.ProxyID)

	require.NoError(t, cache.SetAccountLocation(context.Background(), 1, &service.CodexLocationCacheRecord{
		Timezone:  "America/New_York",
		ProxyID:   100,
		ProxyHash: "abc",
		Success:   true,
	}, time.Hour))
	// 直连条目不得继续遮蔽新的代理记录，否则每次读取都会指纹不匹配而重复探测。
	got, err := cache.GetAccountLocation(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, int64(100), got.ProxyID)
	require.Equal(t, "America/New_York", got.Timezone)
	require.True(t, server.Exists(codexLocationCacheKey(1)))
}

func TestCodexLocationCacheDeleteBatch(t *testing.T) {
	cache, server := newTestCodexLocationCache(t)
	for _, accountID := range []int64{1, 2, 3} {
		require.NoError(t, cache.SetAccountLocation(context.Background(), accountID, &service.CodexLocationCacheRecord{
			Timezone: "Asia/Tokyo",
			ProxyID:  10,
			Success:  true,
		}, time.Hour))
	}
	require.NoError(t, cache.DeleteAccountLocations(context.Background(), []int64{1, 2}))
	require.False(t, server.Exists(codexLocationCacheKey(1)))
	require.False(t, server.Exists(codexLocationCacheKey(2)))
	require.True(t, server.Exists(codexLocationCacheKey(3)))
}
