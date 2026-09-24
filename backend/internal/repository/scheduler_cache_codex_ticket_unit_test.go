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
			"codex_allow_without_ticket":   false,
			"codex_ticket_harvest_enabled": false,
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
	require.Equal(t, false, snapshot[0].Extra["codex_ticket_harvest_enabled"])
	require.True(t, service.OpenAICodexAllowsWithoutTicket(snapshot[0], "gpt-6-astra", false))
	metadata, err := cache.rdb.Get(ctx, schedulerAccountMetaKey("991")).Result()
	require.NoError(t, err)
	require.NotContains(t, metadata, "private-turn-state")
	require.NotContains(t, metadata, "private-cookie")
	require.NotContains(t, metadata, "codex_turn_ticket:")
	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, full)
	require.Contains(t, full.Extra, "codex_turn_ticket:gpt-5.6-sol")

	for _, extra := range []map[string]any{
		{"codex_allow_without_ticket": false},
		{"codex_allow_without_ticket": false, service.CodexTicketReadyModelsExtraKey: map[string]bool{"gpt-5.6-sol": true}},
		{"codex_allow_without_ticket": false, "codex_ticket_harvest_enabled": true, service.CodexTicketReadyModelsExtraKey: map[string]bool{}},
	} {
		legacy := *snapshot[0]
		legacy.Extra = extra
		raw, err := json.Marshal(legacy)
		require.NoError(t, err)
		require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("991"), raw, 0).Err())
		_, hit, err = cache.GetSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.False(t, hit, "old metadata must cause DB fallback and republish")
	}

	delete(account.Extra, "codex_turn_ticket:gpt-5.6-sol")
	require.NoError(t, cache.SetAccount(ctx, &account))
	snapshot, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, map[string]any{}, snapshot[0].Extra[service.CodexTicketReadyModelsExtraKey])
}

func TestSchedulerCacheCodexParticipationDefaultAndUpdates(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{
		ID: 992, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Extra: map[string]any{"codex_allow_without_ticket": false},
	}
	bucket := service.SchedulerBucket{GroupID: 1, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.Equal(t, true, snapshot[0].Extra["codex_ticket_harvest_enabled"])
	require.Equal(t, map[string]any{}, snapshot[0].Extra["codex_ticket_harvest_models"])
	require.False(t, service.OpenAICodexAllowsWithoutTicket(snapshot[0], "gpt-6-astra", false))
	require.NotContains(t, account.Extra, "codex_ticket_harvest_enabled", "projection must not mutate source settings")

	for _, enabled := range []bool{false, true} {
		account.Extra["codex_ticket_harvest_enabled"] = enabled
		require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{"codex_ticket_harvest_enabled": enabled}))
		require.NoError(t, cache.SetAccount(ctx, &account))
		snapshot, hit, err = cache.GetSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.True(t, hit)
		require.Len(t, snapshot, 1)
		require.Equal(t, enabled, snapshot[0].Extra["codex_ticket_harvest_enabled"])
		require.Equal(t, !enabled, service.OpenAICodexAllowsWithoutTicket(snapshot[0], "gpt-6-astra", false))
	}
}

func TestSchedulerCacheCodexModelParticipationRoundTrip(t *testing.T) {
	for _, models := range []any{
		map[string]bool{"gpt-6-astra": false, "gpt-5.6-sol": true},
		map[string]any{"gpt-6-astra": false, "gpt-5.6-sol": true, "legacy": "false"},
	} {
		ctx := context.Background()
		cache := newSchedulerCacheUnit(t)
		account := service.Account{ID: 993, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Extra: map[string]any{"codex_allow_without_ticket": false, "codex_ticket_harvest_models": models},
		}
		bucket := service.SchedulerBucket{GroupID: 1, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
		token, err := cache.CaptureBucketWriteToken(ctx, bucket)
		require.NoError(t, err)
		require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
		snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.True(t, hit)
		require.Len(t, snapshot, 1)
		require.Equal(t, map[string]any{"gpt-6-astra": false, "gpt-5.6-sol": true}, snapshot[0].Extra["codex_ticket_harvest_models"])
		require.True(t, service.OpenAICodexAllowsWithoutTicket(snapshot[0], "gpt-6-astra", false))
		require.False(t, service.OpenAICodexAllowsWithoutTicket(snapshot[0], "gpt-5.6-sol", false))
		require.False(t, service.OpenAICodexAllowsWithoutTicket(snapshot[0], "legacy", false))

		account.Extra["codex_ticket_harvest_models"] = map[string]bool{"gpt-6-astra": true, "gpt-5.6-sol": false}
		require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{"codex_ticket_harvest_models": account.Extra["codex_ticket_harvest_models"]}))
		require.NoError(t, cache.SetAccount(ctx, &account))
		snapshot, hit, err = cache.GetSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.True(t, hit)
		require.False(t, service.OpenAICodexAllowsWithoutTicket(snapshot[0], "gpt-6-astra", false))
		require.True(t, service.OpenAICodexAllowsWithoutTicket(snapshot[0], "gpt-5.6-sol", false))
		require.Equal(t, false, snapshot[0].Extra["codex_allow_without_ticket"])
	}
}
