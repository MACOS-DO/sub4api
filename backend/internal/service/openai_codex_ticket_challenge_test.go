package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type challengeHarvestAccountRepo struct {
	AccountRepository
	account *Account
}

func (r *challengeHarvestAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func (r *challengeHarvestAccountRepo) UpdateExtra(_ context.Context, _ int64, extra map[string]any) error {
	if r.account.Extra == nil {
		r.account.Extra = make(map[string]any)
	}
	for key, value := range extra {
		r.account.Extra[key] = value
	}
	return nil
}

type challengeHarvestProxyRepo struct{ ProxyRepository }

func (r *challengeHarvestProxyRepo) ListActive(context.Context) ([]Proxy, error) {
	return []Proxy{{ID: 1, Name: "test", Protocol: "http", Host: "proxy.example.com", Port: 8080, Status: StatusActive}}, nil
}

type challengeHarvestHistory struct {
	CodexTicketAttemptRepository
	attempts []CodexTicketAttempt
}

func (r *challengeHarvestHistory) TryLock(context.Context, int64, string) (func(), bool, error) {
	return func() {}, true, nil
}

func (r *challengeHarvestHistory) Insert(_ context.Context, attempt *CodexTicketAttempt) error {
	r.attempts = append(r.attempts, *attempt)
	return nil
}

func challengeHarvestService(t *testing.T, upstream HTTPUpstream) (*OpenAIGatewayService, *Account, *challengeHarvestHistory) {
	t.Helper()
	account := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, upstream)
	svc.accountRepo = &challengeHarvestAccountRepo{account: account}
	svc.settingService = &SettingService{settingRepo: newRuntimeSettingRepoStub(), proxyRepo: &challengeHarvestProxyRepo{}}
	history := &challengeHarvestHistory{}
	svc.openaiCodexTicketHistory = history
	return svc, account, history
}

func challengeHarvestResponse(t *testing.T, text string) *http.Response {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": text})
	require.NoError(t, err)
	response := codexTicketResponse()
	response.Body = io.NopCloser(strings.NewReader("data: " + string(payload) + "\n\ndata: [DONE]\n\n"))
	return response
}

func TestCodexTicketChallengeRequestHistoryAndPredictionAgree(t *testing.T) {
	raw, err := os.ReadFile("testdata/modeltrace_gpt_reference.json")
	require.NoError(t, err)
	var samples []struct{ Model, Text string }
	require.NoError(t, json.Unmarshal(raw, &samples))
	var output string
	for _, sample := range samples {
		if sample.Model == "gpt-6-astra" {
			output = sample.Text
		}
	}
	require.NotEmpty(t, output)

	var prompts []string
	upstream := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assertCodexProbeIdentity(t, body, req.Header)
		prompts = append(prompts, gjson.GetBytes(body, "input.4.content.0.text").String())
		return challengeHarvestResponse(t, output), nil
	}}
	svc, account, history := challengeHarvestService(t, upstream)
	for index, trigger := range []string{"automatic", "manual", "diagnostic"} {
		count := []int{292, 310, 332}[index]
		var sent ModelTraceChallenge
		generated := 0
		result, err := svc.runCodexTicketAttemptWithChallengeGenerator(context.Background(), account, "gpt-6-astra", trigger, func() (ModelTraceChallenge, error) {
			generated++
			challenge, err := newModelTraceChallenge(bytes.NewReader([]byte{byte(count - 292), byte(index), 0, 0, 0}))
			sent = challenge
			return challenge, err
		})
		require.NoError(t, err)
		require.Equal(t, 1, generated)
		require.Equal(t, "success", result.Outcome)
		require.True(t, result.HistoryRecorded)
		require.Len(t, prompts, index+1)
		require.Equal(t, sent.Prompt, prompts[index])
		require.Contains(t, prompts[index], fmt.Sprintf(" %d 个 ", count))
		require.Equal(t, count, *result.ChallengeExpectedCount)
		require.Len(t, history.attempts, index+1)
		require.Equal(t, count, *history.attempts[index].ChallengeExpectedCount)
		require.Equal(t, trigger, history.attempts[index].Trigger)
		require.Equal(t, "gpt-6-astra", result.FingerprintPredictedModel)
	}
}

func TestCodexTicketChallengeUsesSelectedCountForMinimumOutput(t *testing.T) {
	// 170 numbers meet ceil(292 * .55), but not ceil(332 * .55).
	for _, count := range []int{292, 332} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			calls := 0
			upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
				calls++
				return challengeHarvestResponse(t, strings.Repeat("1 ", 170)), nil
			}}
			svc, account, history := challengeHarvestService(t, upstream)
			result, err := svc.runCodexTicketAttemptWithChallengeGenerator(context.Background(), account, "gpt-6-astra", "manual", func() (ModelTraceChallenge, error) {
				return newModelTraceChallenge(bytes.NewReader([]byte{byte(count - 292), 0, 0, 0, 0}))
			})
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, 170, *result.ParsedNumberCount)
			require.Equal(t, count, *history.attempts[0].ChallengeExpectedCount)
			if count == 332 {
				require.Equal(t, "miss", result.Outcome)
				require.Equal(t, "insufficient_numbers", result.ReasonCode)
			} else {
				require.NotEqual(t, "insufficient_numbers", result.ReasonCode)
				require.NotEmpty(t, result.FingerprintPredictedModel)
			}
		})
	}
}

func TestCodexTicketChallengeFailureRecordsErrorWithoutRequest(t *testing.T) {
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		t.Fatal("failed challenge must not send an upstream request")
		return nil, nil
	}}
	svc, account, history := challengeHarvestService(t, upstream)
	result, err := svc.runCodexTicketAttemptWithChallengeGenerator(context.Background(), account, "gpt-6-astra", "manual", func() (ModelTraceChallenge, error) {
		return newModelTraceChallenge(bytes.NewReader(nil))
	})
	require.NoError(t, err)
	require.Equal(t, "error", result.Outcome)
	require.Equal(t, "challenge_generation_failed", result.ReasonCode)
	require.Nil(t, result.ChallengeExpectedCount)
	require.Nil(t, result.HTTPStatus)
	require.True(t, result.HistoryRecorded)
	require.Len(t, history.attempts, 1)
	require.Equal(t, result.ReasonCode, history.attempts[0].ReasonCode)
	_, busy := svc.openaiCodexTicketInFlight.Load(openAICodexTicketKey(account.ID, "gpt-6-astra"))
	require.False(t, busy)
	_, scheduled := svc.openaiCodexTicketNextAttempt.Load(openAICodexTicketKey(account.ID, "gpt-6-astra"))
	require.True(t, scheduled)
}
