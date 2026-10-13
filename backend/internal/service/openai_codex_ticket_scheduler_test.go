package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketBlocksAccountIsModelSpecific(t *testing.T) {
	account := ticketTestAccount(91)
	ticket := verifiedTicket(ticketTestAccount(90), "gpt-5.6-sol", "private-state", "__oailb=private")
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-5.6-sol"))
	seedSharedCodexTicket(svc, ticket)
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-5.6-sol"))
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"), "the global ticket serves every gated model")
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-5.4"))
	cached, ok := cachedSharedCodexTicket(svc)
	require.True(t, ok, "the scheduler check must not delete the shared ticket")
	require.Same(t, ticket, cached)
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, ticket.Model, make(map[string][]string)))
	// Legacy per-account readiness metadata no longer influences scheduling.
	svc.openaiCodexTicket.Store(nil)
	account.Extra = map[string]any{"codex_ticket_ready_models": map[string]bool{"gpt-6-astra": true}}
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
}

func TestCodexTicketAccountOverrideIsThreeState(t *testing.T) {
	account := ticketTestAccount(92)
	require.False(t, OpenAICodexAllowsWithoutTicket(account, false))
	require.True(t, OpenAICodexAllowsWithoutTicket(account, true))
	account.Extra = map[string]any{"codex_allow_without_ticket": true}
	require.True(t, OpenAICodexAllowsWithoutTicket(account, false))
	account.Extra["codex_allow_without_ticket"] = false
	require.False(t, OpenAICodexAllowsWithoutTicket(account, true))
	status := OpenAICodexTicketStatusOf(account, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, CodexSharedTickets{}, time.Now())
	require.NotNil(t, status)
	require.True(t, status.Blocked)
	status = OpenAICodexTicketStatusOf(account, config.OpenAICodexTicketConfig{Enabled: false, FailClosed: true}, CodexSharedTickets{}, time.Now())
	require.False(t, status.Blocked)
}

func TestCodexTicketGlobalAllowSettingCachesAndInvalidates(t *testing.T) {
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	settings := NewSettingService(repo, &config.Config{})
	ctx := context.Background()
	require.False(t, settings.GetOpenAICodexTicketAllowWithoutTicket(ctx, false))
	settings.InvalidateOpenAICodexTicketAllowCache()
	require.True(t, settings.GetOpenAICodexTicketAllowWithoutTicket(ctx, true))
	repo.values[SettingKeyOpenAICodexTicketAllowWithoutTicket] = "false"
	settings.InvalidateOpenAICodexTicketAllowCache()
	require.False(t, settings.GetOpenAICodexTicketAllowWithoutTicket(ctx, true))
	repo.values[SettingKeyOpenAICodexTicketAllowWithoutTicket] = "true"
	require.False(t, settings.GetOpenAICodexTicketAllowWithoutTicket(ctx, false))
	settings.InvalidateOpenAICodexTicketAllowCache()
	require.True(t, settings.GetOpenAICodexTicketAllowWithoutTicket(ctx, false))
}

type codexSnapshotRefreshStub struct {
	AccountRepository
	accountID int64
}

func (stub *codexSnapshotRefreshStub) RefreshSchedulerAccount(_ context.Context, accountID int64) {
	stub.accountID = accountID
}

func TestCodexTicketInvalidationDoesNotTouchAccountSnapshots(t *testing.T) {
	account := ticketTestAccount(93)
	account.Extra = map[string]any{"custom": true}
	ticket := verifiedTicket(account, "gpt-5.6-sol", "old-state", "__oailb=old")
	capture := &codexInvalidationCapture{current: ticket}
	refresher := &codexSnapshotRefreshStub{}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.accountRepo = refresher
	svc.openaiCodexTicketLifecycle = capture
	svc.openaiCodexTicketWake = make(chan struct{}, 1)
	request, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	request = request.WithContext(context.WithValue(request.Context(), codexTicketRequestContextKey{}, ticket))
	request.Header.Set(openAICodexTurnStateHeader, ticket.State)
	request.Header.Set("Cookie", ticket.Cookie)
	header := http.Header{}
	header.Set(openAICodexTurnStateHeader, "new-state")
	header.Set("Set-Cookie", "__oailb=new; Path=/")
	svc.observeCodexTicketResponse(request, &http.Response{StatusCode: http.StatusOK, Header: header}, account)
	require.Len(t, capture.events, 1)
	require.Zero(t, refresher.accountID, "the shared ticket lives outside account snapshots")
	require.Equal(t, map[string]any{"custom": true}, account.Extra, "shared account snapshots must not be mutated")
	require.Len(t, svc.openaiCodexTicketWake, 1, "the harvester is notified instead")
}
