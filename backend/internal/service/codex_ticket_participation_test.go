package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexParticipationRepo struct {
	AccountRepository
	account *Account
	scans   chan struct{}
}

func (r *codexParticipationRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}
func (r *codexParticipationRepo) UpdateExtra(_ context.Context, _ int64, values map[string]any) error {
	for key, value := range values {
		r.account.Extra[key] = value
	}
	return nil
}
func (r *codexParticipationRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	if r.scans != nil {
		select {
		case r.scans <- struct{}{}:
		default:
		}
	}
	return []Account{*r.account}, nil
}

func TestCodexTicketParticipationCanBeRestoredWhenRuntimeDisabled(t *testing.T) {
	account := ticketTestAccount(71)
	account.Status = "disabled"
	account.Extra = map[string]any{codexTicketAccountEnabledKey: false, codexTicketRetiredModelsKey: map[string]any{"gpt-6-sol": false}, "codex_allow_without_ticket": false}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: false}, nil)
	svc.accountRepo = &codexParticipationRepo{account: account}
	require.NoError(t, svc.SetCodexTicketParticipation(context.Background(), account.ID, false))
	require.False(t, CodexTicketHarvestEnabled(account))
	require.NoError(t, svc.SetCodexTicketParticipation(context.Background(), account.ID, true))
	require.True(t, CodexTicketHarvestEnabled(account), "the retired per-model switch cannot keep the account out")
	require.Equal(t, false, account.Extra["codex_allow_without_ticket"])
}

func TestCodexTicketHarvesterNotificationRescansWithoutBypassingParticipation(t *testing.T) {
	account := ticketTestAccount(71)
	account.Extra = map[string]any{codexTicketAccountEnabledKey: false}
	repo := &codexParticipationRepo{account: account, scans: make(chan struct{}, 10)}
	upstream := &httpUpstreamRecorder{}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProbeIntervalSeconds: 3600}, upstream)
	svc.accountRepo = repo
	svc.StartOpenAICodexTicketHarvester()
	defer svc.StopOpenAICodexTicketHarvester()
	select {
	case <-repo.scans:
	case <-time.After(time.Second):
		t.Fatal("initial scan missing")
	}
	svc.notifyOpenAICodexTicketHarvester()
	select {
	case <-repo.scans:
	case <-time.After(time.Second):
		t.Fatal("notification did not wake the scheduler")
	}
	svc.StopOpenAICodexTicketHarvester()
	require.Empty(t, upstream.requests)
}

func TestCodexTicketHarvestStillHonorsRuntimeGates(t *testing.T) {
	for _, gate := range []string{"global", "account", "inactive", "rate limit", "harvest model limited"} {
		t.Run(gate, func(t *testing.T) {
			account := ticketTestAccount(71)
			account.Extra = map[string]any{}
			cfg := config.OpenAICodexTicketConfig{Enabled: true}
			switch gate {
			case "global":
				cfg.Enabled = false
			case "account":
				account.Extra[codexTicketAccountEnabledKey] = false
			case "harvest model limited":
				until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
				account.Extra["model_rate_limits"] = map[string]any{openAICodexTicketHarvestModel: map[string]any{"rate_limit_reset_at": until}}
			case "inactive":
				account.Status = "disabled"
			case "rate limit":
				until := time.Now().Add(time.Hour)
				account.RateLimitResetAt = &until
			}
			upstream := &httpUpstreamRecorder{}
			svc := ticketTestService(t, cfg, upstream)
			svc.accountRepo = &codexParticipationRepo{account: account}
			svc.refreshOpenAICodexTickets(context.Background())
			_, err := svc.ManualCodexTicketHarvest(context.Background(), account.ID)
			require.Error(t, err)
			require.Empty(t, upstream.requests)
		})
	}
}

func TestCodexTicketParticipationBypassesMissingTicketGateUntilReenabled(t *testing.T) {
	account := ticketTestAccount(71)
	account.Extra = map[string]any{"codex_allow_without_ticket": false}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	svc.accountRepo = &codexParticipationRepo{account: account}
	ctx := context.Background()

	for _, enabled := range []bool{false, true, false} {
		require.NoError(t, svc.SetCodexTicketParticipation(ctx, account.ID, enabled))
		for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra"} {
			require.Equal(t, enabled, svc.openAICodexTicketBlocksAccount(account, model), model)
			_, err := svc.applyOpenAICodexTicketWithGeneration(ctx, account, model, http.Header{})
			if enabled {
				require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
			} else {
				require.NoError(t, err)
			}
		}
		status := OpenAICodexTicketStatusOf(account, svc.openAICodexTicketConfig(), CodexSharedTickets{}, time.Now())
		require.Equal(t, enabled, status.Blocked)
		require.Equal(t, false, account.Extra["codex_allow_without_ticket"], "the saved policy must not change")
	}
}
