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
			// Legacy per-account ticket material must never reach scheduler metadata.
			"codex_turn_ticket:gpt-5.6-sol": map[string]any{
				"generation_id": "generation", "verification_method": "modeltrace_v1",
				"fingerprint_commit": "commit", "state": "private-turn-state", "cookie": "private-cookie",
			},
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
	require.NotContains(t, snapshot[0].Extra, "codex_ticket_ready_models", "shared tickets are not projected per account")
	require.Equal(t, false, snapshot[0].Extra["codex_allow_without_ticket"])
	require.Equal(t, false, snapshot[0].Extra["codex_ticket_harvest_enabled"])
	require.True(t, service.OpenAICodexAllowsWithoutTicket(snapshot[0], false))
	metadata, err := cache.rdb.Get(ctx, schedulerAccountMetaKey("991")).Result()
	require.NoError(t, err)
	require.NotContains(t, metadata, "private-turn-state")
	require.NotContains(t, metadata, "private-cookie")
	require.NotContains(t, metadata, "codex_turn_ticket:")
	require.NotContains(t, metadata, "codex_ticket_ready_models")

	for _, extra := range []map[string]any{
		{"codex_allow_without_ticket": false},
		{"codex_allow_without_ticket": false, "codex_ticket_harvest_models": map[string]bool{}},
	} {
		legacy := *snapshot[0]
		legacy.Extra = extra
		raw, err := json.Marshal(legacy)
		require.NoError(t, err)
		require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("991"), raw, 0).Err())
		_, hit, err = cache.GetSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.False(t, hit, "metadata without participation must cause DB fallback and republish")
	}

	// Metadata written before the global-ticket change still carries the retired
	// readiness and per-model keys; they are harmless once participation is present.
	legacy := *snapshot[0]
	legacy.Extra = map[string]any{"codex_allow_without_ticket": false, "codex_ticket_harvest_enabled": true,
		"codex_ticket_harvest_models": map[string]bool{}, "codex_ticket_ready_models": map[string]bool{}}
	raw, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("991"), raw, 0).Err())
	_, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit, "the ready-models and per-model keys are no longer required")

	legacy.Extra = map[string]any{"codex_allow_without_ticket": false, "codex_ticket_harvest_enabled": true}
	raw, err = json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, cache.rdb.Set(ctx, schedulerAccountMetaKey("991"), raw, 0).Err())
	_, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit, "account-level participation alone is a complete projection")

	delete(account.Extra, "codex_turn_ticket:gpt-5.6-sol")
	require.NoError(t, cache.SetAccount(ctx, &account))
	snapshot, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.NotContains(t, snapshot[0].Extra, "codex_ticket_ready_models")
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
	require.NotContains(t, snapshot[0].Extra, "codex_ticket_harvest_models")
	require.False(t, service.OpenAICodexAllowsWithoutTicket(snapshot[0], false))
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
		require.Equal(t, !enabled, service.OpenAICodexAllowsWithoutTicket(snapshot[0], false))
	}
}

// The retired per-model switches are no longer projected and never exempt an account.
func TestSchedulerCacheDropsRetiredCodexModelParticipation(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{ID: 993, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Extra: map[string]any{"codex_allow_without_ticket": false, "codex_ticket_harvest_models": map[string]bool{"gpt-6-sol": false, "gpt-6-astra": false}},
	}
	bucket := service.SchedulerBucket{GroupID: 1, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.NotContains(t, snapshot[0].Extra, "codex_ticket_harvest_models")
	require.Equal(t, true, snapshot[0].Extra["codex_ticket_harvest_enabled"])
	require.False(t, service.OpenAICodexAllowsWithoutTicket(snapshot[0], false))
}
