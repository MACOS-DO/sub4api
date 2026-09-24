package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTicketModelBoundary(t *testing.T) {
	for _, test := range []struct {
		model    string
		eligible bool
	}{
		{"gpt-4.1", false}, {"gpt-5.4", false}, {"gpt-5.5", false},
		{"gpt-5.5-pro", false}, {"gpt-5.6-sol", true}, {"gpt-6-astra", true},
		{"claude-6", false},
	} {
		require.Equal(t, test.eligible, codexTicketEligibleModel(test.model), test.model)
	}
	models, err := ModelTraceTicketModels()
	require.NoError(t, err)
	require.NotEmpty(t, models)
	for _, model := range models {
		require.True(t, codexTicketEligibleModel(model), model)
	}
}

func TestCodexTicketHarvestRateLimitBoundary(t *testing.T) {
	now := time.Now().UTC()
	reset := now.Add(10 * time.Minute)
	account := &Account{RateLimitResetAt: &reset}
	require.True(t, openAICodexTicketHarvestLimited(account, "gpt-6-astra", now))
	require.True(t, openAICodexTicketHarvestLimited(account, "gpt-6-astra", reset.Add(-time.Second)))
	require.False(t, openAICodexTicketHarvestLimited(account, "gpt-6-astra", reset))
	quota := openAICodexTicketQuota(account, now)
	require.Equal(t, reset, *quota.resumeAt)

	account.RateLimitResetAt = nil
	account.Extra = map[string]any{"model_rate_limits": map[string]any{
		"gpt-6-astra": map[string]any{"rate_limit_reset_at": reset.Format(time.RFC3339)},
	}}
	require.True(t, openAICodexTicketHarvestLimited(account, "gpt-6-astra", now))
	require.False(t, openAICodexTicketHarvestLimited(account, "gpt-6-sol", now))
	account.Extra["allow_overages"] = true
	require.False(t, openAICodexTicketHarvestLimited(account, "gpt-6-astra", now))
	account.Extra["model_rate_limits"].(map[string]any)["AICredits"] = map[string]any{"rate_limit_reset_at": reset.Format(time.RFC3339)}
	require.True(t, openAICodexTicketHarvestLimited(account, "gpt-6-astra", now))
}
