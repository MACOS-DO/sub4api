package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketSchedulerProjectionIsModelSpecific(t *testing.T) {
	account := ticketTestAccount(91)
	ticket := verifiedTicket(account, "gpt-5.6-sol", "private-turn-state", "private=cookie")
	account.Extra = map[string]any{openAICodexTicketExtraKey(ticket.Model): ticket}
	ready := OpenAICodexTicketReadyModels(account)
	require.Equal(t, map[string]bool{"gpt-5.6-sol": true}, ready)

	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	projected := ticketTestAccount(account.ID)
	projected.Extra = map[string]any{CodexTicketReadyModelsExtraKey: ready}
	projected.SchedulerTicketProjection = true
	svc.openaiCodexTickets.Store(openAICodexTicketKey(account.ID, ticket.Model), ticket)
	require.False(t, svc.openAICodexTicketBlocksAccount(projected, "gpt-5.6-sol"))
	require.True(t, svc.openAICodexTicketBlocksAccount(projected, "gpt-6-astra"))
	require.False(t, svc.openAICodexTicketBlocksAccount(projected, "gpt-5.4"))
	_, cached := svc.openaiCodexTickets.Load(openAICodexTicketKey(account.ID, ticket.Model))
	require.True(t, cached, "metadata prefilter must not delete the real ticket")
	require.False(t, svc.openAICodexTicketBlocksAccount(account, ticket.Model))
	account.Extra[CodexTicketReadyModelsExtraKey] = map[string]bool{"gpt-6-astra": true}
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"), "full account must ignore a projected readiness key")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, ticket.Model, make(map[string][]string)))

	delete(account.Extra, openAICodexTicketExtraKey(ticket.Model))
	require.Empty(t, OpenAICodexTicketReadyModels(account))
}

func TestCodexTicketAccountOverrideIsThreeState(t *testing.T) {
	account := ticketTestAccount(92)
	require.False(t, OpenAICodexAllowsWithoutTicket(account, "gpt-6-astra", false))
	require.True(t, OpenAICodexAllowsWithoutTicket(account, "gpt-6-astra", true))
	account.Extra = map[string]any{"codex_allow_without_ticket": true}
	require.True(t, OpenAICodexAllowsWithoutTicket(account, "gpt-6-astra", false))
	account.Extra["codex_allow_without_ticket"] = false
	require.False(t, OpenAICodexAllowsWithoutTicket(account, "gpt-6-astra", true))
	statuses := OpenAICodexTicketStatuses(account, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, time.Now())
	require.NotEmpty(t, statuses)
	require.True(t, statuses[0].Blocked)
	statuses = OpenAICodexTicketStatuses(account, config.OpenAICodexTicketConfig{Enabled: false, FailClosed: true}, time.Now())
	require.False(t, statuses[0].Blocked)
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

func TestCodexTicketInvalidationRefreshesSchedulerAccount(t *testing.T) {
	account := ticketTestAccount(93)
	ticket := verifiedTicket(account, "gpt-5.6-sol", "old-state", "__oailb=old")
	account.Extra = map[string]any{openAICodexTicketExtraKey(ticket.Model): ticket}
	capture := &codexInvalidationCapture{current: ticket}
	refresher := &codexSnapshotRefreshStub{}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.accountRepo = refresher
	svc.openaiCodexTicketLifecycle = capture
	request, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	request = request.WithContext(context.WithValue(request.Context(), codexTicketRequestContextKey{}, ticket))
	request.Header.Set(openAICodexTurnStateHeader, ticket.State)
	request.Header.Set("Cookie", ticket.Cookie)
	header := http.Header{}
	header.Set(openAICodexTurnStateHeader, "new-state")
	header.Set("Set-Cookie", "__oailb=new; Path=/")
	svc.observeCodexTicketResponse(request, &http.Response{StatusCode: http.StatusOK, Header: header}, account)
	require.Equal(t, account.ID, refresher.accountID)
	require.Contains(t, account.Extra, openAICodexTicketExtraKey(ticket.Model), "shared account snapshots must not be mutated")
}
