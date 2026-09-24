//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCacheCodexTicketProjectionAndLegacyRebuild(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{
		ID: 991, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Extra: map[string]any{
			"codex_allow_without_ticket": false,
			"codex_turn_ticket:gpt-5.6-sol": map[string]any{
				"generation_id": "generation", "verification_method": "modeltrace_v1",
				"fingerprint_commit": "commit", "state": "private-turn-state", "cookie": "private-cookie",
			},
			"codex_turn_ticket:gpt-6-astra": map[string]any{"state": "unverified"},
		},
	}
	bucket := service.SchedulerBucket{GroupID: 1, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.True(t, snapshot[0].SchedulerTicketProjection)
	require.Equal(t, map[string]any{"gpt-5.6-sol": true}, snapshot[0].Extra[service.CodexTicketReadyModelsExtraKey])
	require.Equal(t, false, snapshot[0].Extra["codex_allow_without_ticket"])
	metadata, err := cache.rdb.Get(ctx, schedulerAccountMetaKey("991")).Result()
	require.NoError(t, err)
	require.NotContains(t, metadata, "private-turn-state")
	require.NotContains(t, metadata, "private-cookie")
	require.NotContains(t, metadata, "codex_turn_ticket:")
	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, full)
	require.Contains(t, full.Extra, "codex_turn_ticket:gpt-5.6-sol")

	legacy := *snapshot[0]
	legacy.Extra = map[string]any{"codex_allow_without_ticket": false}
	raw, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("991"), raw, 0).Err())
	_, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit, "old metadata must cause DB fallback and republish")

	delete(account.Extra, "codex_turn_ticket:gpt-5.6-sol")
	require.NoError(t, cache.SetAccount(ctx, &account))
	snapshot, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, map[string]any{}, snapshot[0].Extra[service.CodexTicketReadyModelsExtraKey])
}
