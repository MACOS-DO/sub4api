package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tickets live only in the global shared ticket and participation is account-level, so
// no account write may bring back retired ticket material or per-model switches, for
// example from an admin page loaded before the migration.
func TestAdminAccountWritesDropRetiredCodexTicketExtra(t *testing.T) {
	retired := func() map[string]any {
		return map[string]any{
			"codex_turn_ticket:gpt-6-astra": map[string]any{"state": "spoofed", "cookie": "__oailb=spoofed"},
			codexTicketRetiredModelsKey:     map[string]any{openAICodexTicketHarvestModel: false},
			codexTicketAccountEnabledKey:    true,
			"custom":                        true,
		}
	}
	assertStripped := func(t *testing.T, extra map[string]any) {
		t.Helper()
		require.NotContains(t, extra, "codex_turn_ticket:gpt-6-astra")
		require.NotContains(t, extra, codexTicketRetiredModelsKey)
		require.Equal(t, true, extra[codexTicketAccountEnabledKey], "the account-level switch is kept")
		require.Equal(t, true, extra["custom"])
	}

	t.Run("create", func(t *testing.T) {
		account, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, retired())
		require.NoError(t, err)
		assertStripped(t, account.Extra)
	})
	t.Run("update", func(t *testing.T) {
		repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{41: ticketTestAccount(41)}}
		svc := &adminServiceImpl{accountRepo: repo}
		updated, err := svc.UpdateAccount(context.Background(), 41, &UpdateAccountInput{Extra: retired()})
		require.NoError(t, err)
		assertStripped(t, updated.Extra)
	})
	t.Run("update extra", func(t *testing.T) {
		account := ticketTestAccount(41)
		account.Extra = map[string]any{}
		repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{41: account}}
		svc := &adminServiceImpl{accountRepo: repo}
		require.NoError(t, svc.UpdateAccountExtra(context.Background(), 41, retired()))
		assertStripped(t, repo.accounts[41].Extra)
	})
}
