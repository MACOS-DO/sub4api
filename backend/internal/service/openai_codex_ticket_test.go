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

// verifiedTicket builds a harvested shared ticket whose source is account. State
// and cookie are used verbatim: a structurally valid ticket needs both a turn-state
// and an __oailb cookie.
func verifiedTicket(account *Account, model, state, cookie string) *openAICodexTicket {
	return &openAICodexTicket{AccountID: account.ID, Model: model, State: state, Length: len(state), Cookie: cookie,
		GenerationID: uuid.NewString(), VerificationMethod: "modeltrace_v1",
		FingerprintCommit: modelTraceFallbackCommit, CapturedAt: time.Now().Add(-24 * time.Hour)}
}

// seedSharedCodexTicket places the global ticket in the local cache, which is the
// only source consulted when no lifecycle repository is configured.
func seedSharedCodexTicket(svc *OpenAIGatewayService, ticket *openAICodexTicket, invalidatedAccountIDs ...int64) {
	view := &codexSharedTicketView{ticket: ticket}
	if len(invalidatedAccountIDs) > 0 {
		view.invalidated = make(map[int64]struct{}, len(invalidatedAccountIDs))
		for _, id := range invalidatedAccountIDs {
			view.invalidated[id] = struct{}{}
		}
	}
	svc.cacheSharedCodexTicketView(view)
}

func cachedSharedCodexTicket(svc *OpenAIGatewayService) (*openAICodexTicket, bool) {
	entry := svc.openaiCodexTicket.Load()
	if entry == nil {
		return nil, false
	}
	return entry.view.current(), true
}

func TestCodexTicketMissingEitherCredentialIsNotReusable(t *testing.T) {
	account := ticketTestAccount(41)
	for _, credentials := range [][2]string{{"", ""}, {"  ", "__oailb=lb"}, {"state-only", ""}, {"state", "session=no-lb"}, {"", "__oailb=cookie-only"}} {
		svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
		ticket := verifiedTicket(account, "gpt-6-sol", credentials[0], credentials[1])
		require.False(t, ticket.structurallyValid(0), credentials)
		seedSharedCodexTicket(svc, ticket)
		headers := http.Header{}
		headers.Set(openAICodexTurnStateHeader, "client-state")
		require.ErrorIs(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", headers), ErrOpenAICodexTicketUnavailable)
		require.Empty(t, headers.Get("Cookie"))
		require.Equal(t, "client-state", headers.Get(openAICodexTurnStateHeader))
	}
	require.True(t, verifiedTicket(account, "gpt-6-sol", "state", "route=a; __oailb=lb").structurallyValid(0))
}

func TestCodexTicketStructuralValidityRequiresHarvestMetadata(t *testing.T) {
	account := ticketTestAccount(41)
	require.True(t, verifiedTicket(account, "gpt-6-sol", "lb-state", "__oailb=lb").structurallyValid(0))
	for name, mutate := range map[string]func(*openAICodexTicket){
		"generation":   func(ticket *openAICodexTicket) { ticket.GenerationID = "" },
		"verification": func(ticket *openAICodexTicket) { ticket.VerificationMethod = "legacy" },
		"commit":       func(ticket *openAICodexTicket) { ticket.FingerprintCommit = "" },
		"state":        func(ticket *openAICodexTicket) { ticket.State = " " },
		"oailb":        func(ticket *openAICodexTicket) { ticket.Cookie = "session=1" },
	} {
		ticket := verifiedTicket(account, "gpt-6-sol", "lb-state", "__oailb=lb")
		mutate(ticket)
		require.False(t, ticket.structurallyValid(0), name)
	}
	var missing *openAICodexTicket
	require.False(t, missing.structurallyValid(0))
}

func TestCodexTicketInjectionSetsTurnStateAndAppendsCookie(t *testing.T) {
	account := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	seedSharedCodexTicket(svc, verifiedTicket(account, "gpt-6-sol", "ticket-state", "__oailb=old; session=old"))
	headers := http.Header{}
	headers.Set(openAICodexTurnStateHeader, "client-old")
	headers.Set("Cookie", "existing=1")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", headers))
	require.Equal(t, []string{"ticket-state"}, headers.Values(openAICodexTurnStateHeader), "the shared ticket replaces the client turn-state")
	require.Equal(t, "existing=1; __oailb=old; session=old", headers.Get("Cookie"))

	headers = http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", headers))
	require.Equal(t, "ticket-state", headers.Get(openAICodexTurnStateHeader))
	require.Equal(t, "__oailb=old; session=old", headers.Get("Cookie"))
}

func TestCodexTicketStatusReflectsSharedTicketAndParticipation(t *testing.T) {
	source := ticketTestAccount(41)
	viewer := ticketTestAccount(42)
	ticket := verifiedTicket(source, "gpt-6-sol", fakeCodexTicketState(292), "__oailb=old")
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	seedSharedCodexTicket(svc, ticket)
	shared := svc.CodexSharedTicketSnapshot(context.Background())
	require.Same(t, ticket, shared.view.current())
	status := OpenAICodexTicketStatusOf(viewer, svc.openAICodexTicketConfig(), shared, time.Now())
	require.NotNil(t, status)
	require.Equal(t, openAICodexTicketHarvestModel, status.Model)
	require.True(t, status.Ready)
	require.False(t, status.Blocked)
	require.True(t, status.CookiePresent)
	require.True(t, status.TurnStatePresent)
	require.Equal(t, 292, status.Length)
	require.Equal(t, source.ID, status.SourceAccountID)
	require.Equal(t, ticket.FingerprintCommit, status.FingerprintCommit)
	require.NotNil(t, status.CapturedAt)

	viewer.Extra = map[string]any{codexTicketAccountEnabledKey: false}
	status = OpenAICodexTicketStatusOf(viewer, svc.openAICodexTicketConfig(), shared, time.Now())
	require.False(t, status.Ready, "a non-participating account does not use the shared ticket")
	require.False(t, status.Blocked)
	require.Zero(t, status.SourceAccountID)
	require.Zero(t, status.Length)
	require.False(t, status.TurnStatePresent)

	require.Nil(t, OpenAICodexTicketStatusOf(&Account{ID: 9, Platform: PlatformAnthropic}, svc.openAICodexTicketConfig(), shared, time.Now()))
}

func TestCodexTicketVerifiedOldCaptureDoesNotExpireByTTL(t *testing.T) {
	account := ticketTestAccount(41)
	ticket := verifiedTicket(account, "gpt-6-astra", "old-state", "__oailb=old")
	ticket.CapturedAt = time.Now().Add(-7 * 24 * time.Hour)
	ticket.ExpiresAt = time.Now().Add(-6 * 24 * time.Hour)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, TargetLength: 292}, nil)
	seedSharedCodexTicket(svc, ticket)
	headers := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, ticket.Model, headers))
	require.Equal(t, ticket.State, headers.Get(openAICodexTurnStateHeader))
	require.Equal(t, ticket.Cookie, headers.Get("Cookie"))
}

func TestCodexTicketGlobalTicketServesEveryGatedModel(t *testing.T) {
	account := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	seedSharedCodexTicket(svc, verifiedTicket(account, "gpt-6-sol", "global-state", "__oailb=global"))
	for _, model := range []string{"gpt-6-sol", "gpt-6-astra", "gpt-5.6-sol"} {
		headers := http.Header{}
		require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, model, headers), model)
		require.Equal(t, "global-state", headers.Get(openAICodexTurnStateHeader), model)
		require.Equal(t, "__oailb=global", headers.Get("Cookie"), model)
		require.False(t, svc.openAICodexTicketBlocksAccount(account, model), model)
	}
	ungated := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-5.5", ungated))
	require.Empty(t, ungated.Get("Cookie"))
	require.Empty(t, ungated.Get(openAICodexTurnStateHeader))
}

func TestCodexTicketSharedAcrossAccounts(t *testing.T) {
	source, other := ticketTestAccount(41), ticketTestAccount(42)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	ticket := verifiedTicket(source, "gpt-6-sol", "shared-state", "__oailb=shared; route=a")
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), source, ticket))
	for _, account := range []*Account{source, other} {
		headers := http.Header{}
		injected, err := svc.applyOpenAICodexTicketWithGeneration(context.Background(), account, "gpt-6-astra", headers)
		require.NoError(t, err)
		require.NotNil(t, injected)
		require.Equal(t, ticket.GenerationID, injected.GenerationID)
		require.Equal(t, source.ID, injected.AccountID, "the ticket records its source account")
		require.Equal(t, "__oailb=shared; route=a", headers.Get("Cookie"))
		require.Equal(t, "shared-state", headers.Get(openAICodexTurnStateHeader))
		require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	}
	third := ticketTestAccount(43)
	require.False(t, svc.openAICodexTicketBlocksAccount(third, "gpt-6-astra"))
	require.False(t, svc.openAICodexTicketBlocksAccount(third, "gpt-5.6-sol"))
}

func TestCodexTicketLaterHarvestOverwritesSharedTicket(t *testing.T) {
	first, second := ticketTestAccount(41), ticketTestAccount(42)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), first, verifiedTicket(first, "gpt-6-sol", "first-state", "__oailb=first")))
	latest := verifiedTicket(second, "gpt-6-sol", "second-state", "__oailb=second")
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), second, latest))
	cached, ok := cachedSharedCodexTicket(svc)
	require.True(t, ok)
	require.Same(t, latest, cached)
	headers := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), first, "gpt-6-astra", headers))
	require.Equal(t, "second-state", headers.Get(openAICodexTurnStateHeader))
	require.Equal(t, "__oailb=second", headers.Get("Cookie"))
}

func TestCodexTicketNonParticipatingAccountGetsNoCookie(t *testing.T) {
	source := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	seedSharedCodexTicket(svc, verifiedTicket(source, "gpt-6-sol", "shared-state", "__oailb=shared"))
	account := ticketTestAccount(42)
	account.Extra = map[string]any{codexTicketAccountEnabledKey: false}
	headers := http.Header{}
	headers.Set(openAICodexTurnStateHeader, "client-state")
	injected, err := svc.applyOpenAICodexTicketWithGeneration(context.Background(), account, "gpt-6-astra", headers)
	require.NoError(t, err)
	require.Nil(t, injected)
	require.Empty(t, headers.Get("Cookie"))
	require.Equal(t, "client-state", headers.Get(openAICodexTurnStateHeader))
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
}

// The retired per-model switches must not keep an account out: otherwise an account
// that once opted gpt-6-sol out could never take part again.
func TestCodexTicketRetiredPerModelSwitchesAreIgnored(t *testing.T) {
	source := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	seedSharedCodexTicket(svc, verifiedTicket(source, "gpt-6-sol", "shared-state", "__oailb=shared"))
	account := ticketTestAccount(42)
	account.Extra = map[string]any{codexTicketRetiredModelsKey: map[string]any{"gpt-6-sol": false, "gpt-6-astra": false}}
	require.True(t, CodexTicketHarvestEnabled(account))
	headers := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", headers))
	require.Equal(t, "shared-state", headers.Get(openAICodexTurnStateHeader))
}

func TestCodexTicketAutomaticDueFollowsSharedTicket(t *testing.T) {
	accounts := []*Account{ticketTestAccount(41), ticketTestAccount(42), ticketTestAccount(43)}
	const model = "gpt-6-sol"
	now := time.Now()

	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	for _, account := range accounts {
		require.True(t, svc.codexTicketAutomaticDue(account, now), "missing shared ticket is due for every participating account")
	}

	ticket := verifiedTicket(accounts[0], model, "fresh-state", "__oailb=fresh")
	ticket.CapturedAt = now
	seedSharedCodexTicket(svc, ticket)
	for _, account := range accounts {
		require.False(t, svc.codexTicketAutomaticDue(account, now), "a usable shared ticket is not due without refresh")
	}

	refreshing := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestRefreshSeconds: 300}, nil)
	seedSharedCodexTicket(refreshing, ticket)
	for _, account := range accounts {
		require.False(t, refreshing.codexTicketAutomaticDue(account, now.Add(299*time.Second)))
		require.True(t, refreshing.codexTicketAutomaticDue(account, now.Add(301*time.Second)))
	}

	invalid := verifiedTicket(accounts[0], model, "", "")
	seedSharedCodexTicket(svc, invalid)
	for _, account := range accounts {
		require.True(t, svc.codexTicketAutomaticDue(account, now))
	}
}

func TestCodexTicketAutomaticDueHonorsPerAccountBackoff(t *testing.T) {
	first, second := ticketTestAccount(41), ticketTestAccount(42)
	now := time.Now()
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.openaiCodexTicketNextAttempt.Store(codexTicketRetryKey(first.ID), now.Add(time.Minute))
	require.False(t, svc.codexTicketAutomaticDue(first, now))
	require.True(t, svc.codexTicketAutomaticDue(second, now))
	require.True(t, svc.codexTicketAutomaticDue(first, now.Add(2*time.Minute)))

	ticket := verifiedTicket(second, "gpt-6-sol", "fresh-state", "__oailb=fresh")
	require.NoError(t, svc.storeOpenAICodexTicket(context.Background(), second, ticket))
	svc.scheduleCodexTicketAfterSuccess(ticket)
	_, ok := svc.openaiCodexTicketNextAttempt.Load(codexTicketRetryKey(first.ID))
	require.False(t, ok, "a successful harvest clears every account's backoff")
}
