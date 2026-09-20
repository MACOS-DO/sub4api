package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexTicketAttemptMemoryRepo struct {
	inserted  []CodexTicketAttempt
	insertErr error
}

func (r *codexTicketAttemptMemoryRepo) Insert(_ context.Context, attempt *CodexTicketAttempt) error {
	if r.insertErr != nil {
		return r.insertErr
	}
	r.inserted = append(r.inserted, *attempt)
	return nil
}
func (r *codexTicketAttemptMemoryRepo) List(context.Context, int64, string, bool, int, int) ([]CodexTicketAttempt, int64, error) {
	return nil, 0, nil
}
func (r *codexTicketAttemptMemoryRepo) Cleanup(context.Context) error { return nil }
func (r *codexTicketAttemptMemoryRepo) TryLock(context.Context, int64, string) (func(), bool, error) {
	return func() {}, true, nil
}

func (r *codexTicketQuotaRepo) GetByID(context.Context, int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.account
	return &account, nil
}

func TestManualCodexTicketHarvestBypassesRateLimitAndKeepsItReadOnly(t *testing.T) {
	account := ticketTestAccount(41)
	account.Status = StatusActive
	resetAt := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	account.RateLimitResetAt = &resetAt
	repo := &codexTicketQuotaRepo{account: *account}
	history := &codexTicketAttemptMemoryRepo{}
	response := codexTicketResponse()
	response.StatusCode = http.StatusTooManyRequests
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://proxy.example:8080", Models: []string{"gpt-6-astra"}}, &httpUpstreamRecorder{responses: []*http.Response{response}})
	svc.accountRepo, svc.openaiCodexTicketHistory = repo, history

	result, err := svc.ManualCodexTicketHarvest(context.Background(), account.ID, "gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, "success", result.Outcome)
	require.Equal(t, "manual", result.Trigger)
	require.True(t, result.HistoryRecorded)
	require.True(t, result.TicketStatus.Ready)
	require.Equal(t, resetAt, *repo.account.RateLimitResetAt)
	require.Len(t, history.inserted, 1)
	require.Equal(t, "manual", history.inserted[0].Trigger)
}

func TestManualCodexTicketFailurePreservesExistingTicketAndReportsHistoryFailure(t *testing.T) {
	account := ticketTestAccount(41)
	account.Status = StatusActive
	repo := &codexTicketQuotaRepo{account: *account}
	history := &codexTicketAttemptMemoryRepo{insertErr: errors.New("database unavailable")}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProxyURL: "http://proxy.example:8080", Models: []string{"gpt-6-astra"}}, &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}}}})
	svc.accountRepo, svc.openaiCodexTicketHistory = repo, history
	existing := &openAICodexTicket{AccountID: account.ID, Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	svc.storeOpenAICodexTicket(context.Background(), account, existing)

	result, err := svc.ManualCodexTicketHarvest(context.Background(), account.ID, "gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, "miss", result.Outcome)
	require.False(t, result.HistoryRecorded)
	require.Equal(t, existing.State, svc.lookupOpenAICodexTicket(account, "gpt-6-astra").State)
}

func TestCodexTicketAutomaticScheduleUsesRequestedRanges(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	now := time.Now().Truncate(time.Second)
	ticket := &openAICodexTicket{AccountID: 41, Model: "gpt-6-astra", CapturedAt: now}
	svc.scheduleCodexTicketAfterSuccess(ticket)
	next := func() time.Time {
		value, _ := svc.openaiCodexTicketNextAttempt.Load(openAICodexTicketKey(41, "gpt-6-astra"))
		return value.(time.Time)
	}()
	require.GreaterOrEqual(t, next.Sub(now), codexTicketValidFirstRetryMin)
	require.LessOrEqual(t, next.Sub(now), codexTicketValidFirstRetryMax)
	require.Equal(t, 10*time.Second, codexTicketMissingRetry)
	retry := codexTicketJitter("41\x00gpt-6-astra", now, codexTicketValidRetryMin, codexTicketValidRetryMax)
	require.GreaterOrEqual(t, retry, 20*time.Second)
	require.LessOrEqual(t, retry, 40*time.Second)
	svc.openaiCodexTicketNextAttempt.Store(openAICodexTicketKey(42, "gpt-5.6-sol"), now.Add(3*time.Second))
	require.Equal(t, 3*time.Second, svc.openAICodexTicketNextScanDelay(now))
}

func TestManualCodexTicketHarvestHonorsParticipationAndAvailability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*Account, *config.OpenAICodexTicketConfig)
		prepare   func(*OpenAIGatewayService, *Account)
		want      error
	}{
		{
			name: "global switch disabled",
			configure: func(_ *Account, cfg *config.OpenAICodexTicketConfig) {
				cfg.Enabled = false
			},
			want: ErrCodexTicketUnavailable,
		},
		{
			name: "account opted out",
			configure: func(account *Account, _ *config.OpenAICodexTicketConfig) {
				account.Extra = make(map[string]any)
				account.Extra[codexTicketAccountEnabledKey] = false
			},
			want: ErrCodexTicketUnavailable,
		},
		{
			name: "model opted out",
			configure: func(account *Account, _ *config.OpenAICodexTicketConfig) {
				account.Extra = make(map[string]any)
				account.Extra[codexTicketModelsEnabledKey] = map[string]any{"gpt-6-astra": false}
			},
			want: ErrCodexTicketUnavailable,
		},
		{
			name: "no managed proxy",
			configure: func(_ *Account, cfg *config.OpenAICodexTicketConfig) {
				cfg.HarvestProxyURL = ""
			},
			want: ErrCodexTicketNoProxy,
		},
		{
			name: "same model already running",
			prepare: func(svc *OpenAIGatewayService, account *Account) {
				svc.openaiCodexTicketInFlight.Store(openAICodexTicketKey(account.ID, "gpt-6-astra"), true)
			},
			want: ErrCodexTicketBusy,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := ticketTestAccount(41)
			account.Status = StatusActive
			cfg := config.OpenAICodexTicketConfig{
				Enabled: true, HarvestProxyURL: "http://proxy.example:8080", Models: []string{"gpt-6-astra"},
			}
			if tc.configure != nil {
				tc.configure(account, &cfg)
			}
			repo := &codexTicketQuotaRepo{account: *account}
			requests := 0
			svc := ticketTestService(t, cfg, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
				requests++
				return codexTicketResponse(), nil
			}})
			svc.accountRepo = repo
			if tc.prepare != nil {
				tc.prepare(svc, account)
			}
			_, err := svc.ManualCodexTicketHarvest(context.Background(), account.ID, "gpt-6-astra")
			require.ErrorIs(t, err, tc.want)
			require.Zero(t, requests)
		})
	}
}
