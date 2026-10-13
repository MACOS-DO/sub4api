//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexSharedTicketCacheRedis(t *testing.T) {
	ctx := context.Background()
	cache := NewCodexSharedTicketCache(integrationRedis)
	lock := "lock:" + uuid.NewString()
	require.NoError(t, integrationRedis.Del(ctx, codexSharedTicketKey).Err())
	t.Cleanup(func() {
		_ = integrationRedis.Del(ctx, codexSharedTicketKey, codexTicketLockKeyPrefix+lock).Err()
	})

	_, hit, err := cache.GetSharedTicket(ctx)
	require.NoError(t, err)
	require.False(t, hit)

	state := &service.CodexSharedTicketState{Ticket: json.RawMessage(`{"generation_id":"g","cookie":"__oailb=x"}`), InvalidatedAccountIDs: []int64{3}, Version: 2}
	require.NoError(t, cache.SetSharedTicket(ctx, state, time.Minute))
	got, hit, err := cache.GetSharedTicket(ctx)
	require.NoError(t, err)
	require.True(t, hit)
	require.JSONEq(t, string(state.Ticket), string(got.Ticket))
	require.Equal(t, []int64{3}, got.InvalidatedAccountIDs)
	require.Equal(t, int64(2), got.Version)
	ttl, err := integrationRedis.PTTL(ctx, codexSharedTicketKey).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Duration(0))

	// A reader that loaded an older version cannot overwrite a newer write.
	require.NoError(t, cache.SetSharedTicket(ctx, &service.CodexSharedTicketState{Version: 1}, time.Minute))
	got, _, err = cache.GetSharedTicket(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), got.Version)
	require.JSONEq(t, string(state.Ticket), string(got.Ticket))

	// An empty state is cached too, so a missing ticket does not hit the database on every read.
	require.NoError(t, cache.SetSharedTicket(ctx, &service.CodexSharedTicketState{Version: 3}, time.Minute))
	got, hit, err = cache.GetSharedTicket(ctx)
	require.NoError(t, err)
	require.True(t, hit)
	require.Empty(t, got.Ticket)
	require.NoError(t, cache.DeleteSharedTicket(ctx))
	_, hit, err = cache.GetSharedTicket(ctx)
	require.NoError(t, err)
	require.False(t, hit)

	locked, err := cache.TryLock(ctx, lock, "owner-a", time.Minute)
	require.NoError(t, err)
	require.True(t, locked)
	locked, err = cache.TryLock(ctx, lock, "owner-b", time.Minute)
	require.NoError(t, err)
	require.False(t, locked)
	extended, err := cache.ExtendLock(ctx, lock, "owner-b", time.Hour)
	require.NoError(t, err)
	require.False(t, extended, "only the owner extends the lock")
	extended, err = cache.ExtendLock(ctx, lock, "owner-a", time.Hour)
	require.NoError(t, err)
	require.True(t, extended)
	ttl, err = integrationRedis.PTTL(ctx, codexTicketLockKeyPrefix+lock).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Minute)
	require.NoError(t, cache.Unlock(ctx, lock, "owner-b"))
	locked, err = cache.TryLock(ctx, lock, "owner-b", time.Minute)
	require.NoError(t, err)
	require.False(t, locked, "only the owner releases the lock")
	require.NoError(t, cache.Unlock(ctx, lock, "owner-a"))
	locked, err = cache.TryLock(ctx, lock, "owner-b", time.Minute)
	require.NoError(t, err)
	require.True(t, locked)
}
