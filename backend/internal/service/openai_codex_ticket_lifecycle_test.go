package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexTicketFuncUpstream struct {
	HTTPUpstream
	do func(*http.Request) (*http.Response, error)
}

func (u *codexTicketFuncUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.do(req)
}
func codexTicketResponse() *http.Response {
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, fakeCodexTicketState(292))
	return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader("data: {}\n\n"))}
}

func ticketProbeChallenge(t *testing.T) ModelTraceChallenge {
	t.Helper()
	challenge, err := newModelTraceChallenge(bytes.NewReader([]byte{40, 0, 0, 0, 0}))
	require.NoError(t, err)
	return challenge
}

func TestCodexTicketProbeBypassesPluginDuringWiring(t *testing.T) {
	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100, unavailable: "plugin must not handle synthetic probes"})
	var calls atomic.Int64
	upstream := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if HTTPUpstreamProfileFromContext(req.Context()) != HTTPUpstreamProfileOpenAIHarvest || !req.Close {
			return nil, errors.New("missing no-reuse transport profile")
		}
		return codexTicketResponse(), nil
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, upstream)
	svc.SetPluginManager(manager)
	account := ticketTestAccount(41)
	// This binding rejects ordinary traffic; harvesting still uses the dedicated transport.
	request, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	_, err := svc.doOpenAIUpstream(request, "", account)
	require.Error(t, err)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 200; i++ {
			svc.SetPluginManager(manager)
		}
	}()
	close(start)
	for i := 0; i < 20; i++ {
		_, state, _, status, err := svc.fireOpenAICodexTicketProbe(context.Background(), account, "test-token", "gpt-6-astra", "http://proxy.example.com:8080", ticketProbeChallenge(t), time.Second)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Len(t, state, 292)
	}
	wg.Wait()
	require.Equal(t, int64(20), calls.Load())
}

func TestCodexTicketProbeParsesNormalSSEAndCookies(t *testing.T) {
	account := ticketTestAccount(41)
	upstream := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
		header := http.Header{}
		header.Set(openAICodexTurnStateHeader, "new-state")
		header.Add("Set-Cookie", "session=first; HttpOnly")
		header.Add("Set-Cookie", "route=second; Secure")
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"1 2 3\"}\n\ndata: [DONE]\n\n"))}, nil
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, upstream)
	output, state, cookie, status, err := svc.fireOpenAICodexTicketProbe(context.Background(), account, "token", "gpt-6-astra", "http://proxy.example.com:8080", ticketProbeChallenge(t), time.Second)
	require.NoError(t, err)
	require.Equal(t, "1 2 3", output)
	require.Equal(t, "new-state", state)
	require.Contains(t, cookie, "session=first")
	require.Contains(t, cookie, "route=second")
	require.Equal(t, http.StatusOK, status)
}

func TestCodexTicketProbeDoesNotCarryExistingTicket(t *testing.T) {
	account := ticketTestAccount(41)
	account.Extra = map[string]any{openAICodexTicketExtraKey("gpt-6-astra"): &openAICodexTicket{
		AccountID: account.ID, Model: "gpt-6-astra", State: fakeCodexTicketState(292),
		Cookie: "session=old", GenerationID: "old-generation", CapturedAt: time.Now(),
	}}
	upstream := &codexTicketFuncUpstream{do: func(request *http.Request) (*http.Response, error) {
		require.Empty(t, request.Header.Get(openAICodexTurnStateHeader))
		require.Empty(t, request.Header.Get("Cookie"))
		return codexTicketResponse(), nil
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, upstream)
	_, _, _, status, err := svc.fireOpenAICodexTicketProbe(context.Background(), account, "token", "gpt-6-astra", "http://proxy.example.com:8080", ticketProbeChallenge(t), time.Second)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
}

func TestCodexTicketPolicyExemptsCredentialShadows(t *testing.T) {
	parentID := int64(41)
	parent := ticketTestAccount(parentID)
	shadow := ticketTestAccount(42)
	shadow.ParentAccountID = &parentID
	shadow.Status = StatusActive
	cfg := config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, HarvestProxyURL: "http://proxy.example.com:8080"}
	upstream := &httpUpstreamRecorder{}
	svc := ticketTestService(t, cfg, upstream)
	svc.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*shadow}}
	require.True(t, svc.openAICodexTicketBlocksAccount(parent, "gpt-6-astra"))
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		shadow.Type = accountType
		require.False(t, svc.openAICodexTicketBlocksAccount(shadow, "gpt-6-astra"))
		headers := http.Header{}
		headers.Set(openAICodexTurnStateHeader, "client-state")
		require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), shadow, "gpt-6-astra", headers))
		require.Equal(t, "client-state", headers.Get(openAICodexTurnStateHeader))
		require.Empty(t, OpenAICodexTicketStatuses(shadow, cfg, time.Now()))
		svc.probeOnceOpenAICodexTicket(context.Background(), shadow, "gpt-6-astra")
	}
	svc.refreshOpenAICodexTickets(context.Background())
	require.Empty(t, upstream.requests)
}

type codexTicketRefreshRepo struct {
	AccountRepository
	accounts []Account
}

func (repo *codexTicketRefreshRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	return repo.accounts, nil
}
