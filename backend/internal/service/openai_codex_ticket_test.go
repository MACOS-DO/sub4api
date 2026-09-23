package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func fakeCodexTicketState(length int) string {
	if length < len(openAICodexTicketStatePrefix) {
		return strings.Repeat("A", length)
	}
	return openAICodexTicketStatePrefix + strings.Repeat("B", length-len(openAICodexTicketStatePrefix))
}

func ticketTestAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "tok", "chatgpt_account_id": "acc-1"}}
}

func ticketTestService(t *testing.T, cfg config.OpenAICodexTicketConfig, upstream HTTPUpstream) *OpenAIGatewayService {
	t.Helper()
	return &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICodexTicket: cfg}}, httpUpstream: upstream}
}

func verifiedTicket(account *Account, model, state, cookie string) *openAICodexTicket {
	return &openAICodexTicket{AccountID: account.ID, Model: model, State: state, Cookie: cookie,
		Length: len(state), GenerationID: uuid.NewString(), VerificationMethod: "modeltrace_v1",
		FingerprintCommit: modelTraceFallbackCommit, CapturedAt: time.Now().Add(-24 * time.Hour)}
}

func TestCodexTicketLegacyLengthOnlyIsNotReusable(t *testing.T) {
	account := ticketTestAccount(41)
	account.Extra = map[string]any{openAICodexTicketExtraKey("gpt-6-astra"): &openAICodexTicket{
		AccountID: account.ID, Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, CapturedAt: time.Now(),
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	headers := http.Header{}
	require.ErrorIs(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", headers), ErrOpenAICodexTicketUnavailable)
	require.Empty(t, headers.Get(openAICodexTurnStateHeader))
}

func TestCodexTicketCookieOnlyIsReusable(t *testing.T) {
	account := ticketTestAccount(41)
	account.Extra = map[string]any{openAICodexTicketExtraKey("gpt-6-astra"): verifiedTicket(account, "gpt-6-astra", "", "session=old")}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	headers := http.Header{}
	headers.Set(openAICodexTurnStateHeader, "client-old")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", headers))
	require.Empty(t, headers.Get(openAICodexTurnStateHeader))
	require.Equal(t, "session=old", headers.Get("Cookie"))
	statuses := OpenAICodexTicketStatuses(account, svc.openAICodexTicketConfig(), time.Now())
	require.Len(t, statuses, 8)
	for _, status := range statuses {
		if status.Model == "gpt-6-astra" {
			require.True(t, status.Ready)
			require.Zero(t, status.Length)
		}
	}
}

func TestCodexTicketVerifiedOldCaptureDoesNotExpireByTTL(t *testing.T) {
	account := ticketTestAccount(41)
	ticket := verifiedTicket(account, "gpt-6-astra", "state-value", "")
	ticket.CapturedAt = time.Now().Add(-7 * 24 * time.Hour)
	ticket.ExpiresAt = time.Now().Add(-6 * 24 * time.Hour)
	account.Extra = map[string]any{openAICodexTicketExtraKey(ticket.Model): ticket}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, TargetLength: 292}, nil)
	headers := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, ticket.Model, headers))
	require.Equal(t, ticket.State, headers.Get(openAICodexTurnStateHeader))
}

func TestCodexTicketIsolationByRawModel(t *testing.T) {
	account := ticketTestAccount(41)
	account.Extra = map[string]any{openAICodexTicketExtraKey("gpt-6-astra"): verifiedTicket(account, "gpt-6-astra", "state-one", "")}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	require.ErrorIs(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-5.5", http.Header{}), ErrOpenAICodexTicketUnavailable)
	other := ticketTestAccount(42)
	require.ErrorIs(t, svc.applyOpenAICodexTicket(context.Background(), other, "gpt-6-astra", http.Header{}), ErrOpenAICodexTicketUnavailable)
}
