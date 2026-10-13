package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

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
		if sample.Model == openAICodexTicketHarvestModel {
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
		result, err := svc.runCodexTicketAttemptWithChallengeGenerator(context.Background(), account, trigger, func() (ModelTraceChallenge, error) {
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
		require.Equal(t, openAICodexTicketHarvestModel, result.FingerprintPredictedModel)
		require.Equal(t, openAICodexTicketHarvestModel, history.attempts[index].Model, "every harvest probes gpt-6-sol")
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
			result, err := svc.runCodexTicketAttemptWithChallengeGenerator(context.Background(), account, "manual", func() (ModelTraceChallenge, error) {
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
	result, err := svc.runCodexTicketAttemptWithChallengeGenerator(context.Background(), account, "manual", func() (ModelTraceChallenge, error) {
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
	_, busy := svc.openaiCodexTicketInFlight.Load(codexTicketRetryKey(account.ID))
	require.False(t, busy)
	_, scheduled := svc.openaiCodexTicketNextAttempt.Load(codexTicketRetryKey(account.ID))
	require.True(t, scheduled)
}

func modelTraceReferenceOutput(t *testing.T, model string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/modeltrace_gpt_reference.json")
	require.NoError(t, err)
	var samples []struct{ Model, Text string }
	require.NoError(t, json.Unmarshal(raw, &samples))
	for _, sample := range samples {
		if sample.Model == model {
			return sample.Text
		}
	}
	t.Fatalf("no reference output for %s", model)
	return ""
}

func TestCodexTicketHarvestStoresSharedTicketForAllAccounts(t *testing.T) {
	output := modelTraceReferenceOutput(t, openAICodexTicketHarvestModel)
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		return challengeHarvestResponse(t, output), nil
	}}
	svc, source, _ := challengeHarvestService(t, upstream)
	svc.cfg.Gateway.OpenAICodexTicket.FailClosed = true
	capture := &codexInvalidationCapture{}
	svc.openaiCodexTicketLifecycle = capture
	borrower := ticketTestAccount(42)
	svc.openaiCodexTicketNextAttempt.Store(codexTicketRetryKey(borrower.ID), time.Now().Add(time.Hour))

	result, err := svc.runCodexTicketAttemptWithChallengeGenerator(context.Background(), source, "manual", func() (ModelTraceChallenge, error) {
		return newModelTraceChallenge(bytes.NewReader([]byte{0, 0, 0, 0, 0}))
	})
	require.NoError(t, err)
	require.Equal(t, "success", result.Outcome)
	require.True(t, *result.CookiePresent)
	require.True(t, *result.TurnStatePresent)
	require.Equal(t, 292, *result.TicketLength)
	require.Len(t, capture.stored, 1)
	stored := capture.stored[0]
	require.Equal(t, source.ID, stored.AccountID)
	require.Equal(t, fakeCodexTicketState(292), stored.State)
	require.Equal(t, 292, stored.Length)
	require.Equal(t, "__oailb=harvested", stored.Cookie)
	require.Equal(t, *result.TicketGenerationID, stored.GenerationID)
	require.Equal(t, stored, capture.current)
	_, retry := svc.openaiCodexTicketNextAttempt.Load(codexTicketRetryKey(borrower.ID))
	require.False(t, retry, "a shared success clears every account's backoff")
	require.False(t, svc.codexTicketAutomaticDue(borrower, time.Now()))

	headers := http.Header{}
	injected, err := svc.applyOpenAICodexTicketWithGeneration(context.Background(), borrower, "gpt-6-astra", headers)
	require.NoError(t, err)
	require.Equal(t, stored.GenerationID, injected.GenerationID)
	require.Equal(t, "__oailb=harvested", headers.Get("Cookie"))
	require.Equal(t, stored.State, headers.Get(openAICodexTurnStateHeader))
	require.Equal(t, openAICodexTicketHarvestModel, stored.Model)
	status := OpenAICodexTicketStatusOf(borrower, svc.openAICodexTicketConfig(), svc.CodexSharedTicketSnapshot(context.Background()), time.Now())
	require.True(t, status.Ready)
	require.Equal(t, source.ID, status.SourceAccountID)
	require.Equal(t, 292, status.Length)
	require.True(t, status.TurnStatePresent)
}

func TestCodexTicketHarvestRejectsOtherFingerprintModel(t *testing.T) {
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		return challengeHarvestResponse(t, modelTraceReferenceOutput(t, "gpt-6-astra")), nil
	}}
	svc, source, _ := challengeHarvestService(t, upstream)
	capture := &codexInvalidationCapture{}
	svc.openaiCodexTicketLifecycle = capture
	result, err := svc.runCodexTicketAttemptWithChallengeGenerator(context.Background(), source, "manual", func() (ModelTraceChallenge, error) {
		return newModelTraceChallenge(bytes.NewReader([]byte{0, 0, 0, 0, 0}))
	})
	require.NoError(t, err)
	require.Equal(t, "miss", result.Outcome)
	require.Equal(t, "fingerprint_mismatch", result.ReasonCode)
	require.Equal(t, "gpt-6-astra", result.FingerprintPredictedModel)
	require.Empty(t, capture.stored)
}

func TestCodexTicketHarvestSupersededInRoundIsSuccessWithoutStoring(t *testing.T) {
	output := modelTraceReferenceOutput(t, openAICodexTicketHarvestModel)
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		return challengeHarvestResponse(t, output), nil
	}}
	svc, source, history := challengeHarvestService(t, upstream)
	capture := &codexInvalidationCapture{}
	svc.openaiCodexTicketLifecycle = capture
	round := &codexTicketRound{stored: true} // another account of this round already got its ticket written

	result, err := svc.runCodexTicketAttemptWithChallengeGenerator(withCodexTicketRound(context.Background(), round), source, "automatic", func() (ModelTraceChallenge, error) {
		return newModelTraceChallenge(bytes.NewReader([]byte{0, 0, 0, 0, 0}))
	})
	require.NoError(t, err)
	require.Equal(t, "success", result.Outcome)
	require.Equal(t, CodexTicketReasonSuperseded, result.ReasonCode)
	require.Nil(t, result.TicketGenerationID)
	require.Empty(t, capture.stored, "a superseded ticket never replaces the adopted one")
	require.Len(t, history.attempts, 1)
	require.Equal(t, "success", history.attempts[0].Outcome)
	_, retry := svc.openaiCodexTicketNextAttempt.Load(codexTicketRetryKey(source.ID))
	require.False(t, retry, "a superseded success is not a failure")
}

func TestCodexTicketHarvestStoreFailureLeavesRoundOpen(t *testing.T) {
	output := modelTraceReferenceOutput(t, openAICodexTicketHarvestModel)
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		return challengeHarvestResponse(t, output), nil
	}}
	svc, source, _ := challengeHarvestService(t, upstream)
	svc.openaiCodexTicketLifecycle = &failingStoreTicketRepo{}
	round := &codexTicketRound{}
	result, err := svc.runCodexTicketAttemptWithChallengeGenerator(withCodexTicketRound(context.Background(), round), source, "automatic", func() (ModelTraceChallenge, error) {
		return newModelTraceChallenge(bytes.NewReader([]byte{0, 0, 0, 0, 0}))
	})
	require.NoError(t, err)
	require.Equal(t, "error", result.Outcome)
	require.Equal(t, "persistence", result.ReasonCode)
	require.False(t, round.adopted(), "another account of the round may still get its ticket written")
}

type failingStoreTicketRepo struct{ CodexTicketLifecycleRepository }

func (failingStoreTicketRepo) StoreTicket(context.Context, string, int64, time.Time, json.RawMessage) error {
	return errors.New("write failed")
}

func TestCodexTicketHarvestRequiresTurnStateAndOAILB(t *testing.T) {
	output := modelTraceReferenceOutput(t, openAICodexTicketHarvestModel)
	for name, test := range map[string]struct {
		keepState bool
		cookies   []string
		success   bool
		cookie    string
	}{
		"no credentials":            {},
		"turn-state only":           {keepState: true},
		"turn-state without oailb":  {keepState: true, cookies: []string{"session=x; Path=/"}, cookie: "session=x"},
		"oailb only":                {cookies: []string{"__oailb=lb; Path=/"}, cookie: "__oailb=lb"},
		"turn-state and oailb":      {keepState: true, cookies: []string{"__oailb=lb; Path=/"}, success: true, cookie: "__oailb=lb"},
		"turn-state, oailb, others": {keepState: true, cookies: []string{"route=a; Path=/", "__oailb=lb; Path=/"}, success: true, cookie: "route=a; __oailb=lb"},
	} {
		t.Run(name, func(t *testing.T) {
			upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
				response := challengeHarvestResponse(t, output)
				if !test.keepState {
					response.Header.Del(openAICodexTurnStateHeader)
				}
				response.Header.Del("Set-Cookie")
				for _, cookie := range test.cookies {
					response.Header.Add("Set-Cookie", cookie)
				}
				return response, nil
			}}
			svc, account, history := challengeHarvestService(t, upstream)
			capture := &codexInvalidationCapture{}
			svc.openaiCodexTicketLifecycle = capture
			result, err := svc.runCodexTicketAttemptWithChallengeGenerator(context.Background(), account, "manual", func() (ModelTraceChallenge, error) {
				return newModelTraceChallenge(bytes.NewReader([]byte{0, 0, 0, 0, 0}))
			})
			require.NoError(t, err)
			require.Equal(t, test.keepState, *result.TurnStatePresent)
			require.Equal(t, test.cookie != "", *result.CookiePresent)
			require.Len(t, history.attempts, 1)
			if !test.success {
				require.Equal(t, "miss", result.Outcome)
				require.Equal(t, "missing_credentials", result.ReasonCode)
				require.Nil(t, result.TicketGenerationID)
				require.Empty(t, capture.stored)
				_, scheduled := svc.openaiCodexTicketNextAttempt.Load(codexTicketRetryKey(account.ID))
				require.True(t, scheduled)
				return
			}
			require.Equal(t, "success", result.Outcome)
			require.Len(t, capture.stored, 1)
			stored := capture.stored[0]
			require.Equal(t, test.cookie, stored.Cookie)
			require.Equal(t, fakeCodexTicketState(292), stored.State)
			require.Equal(t, 292, stored.Length)
		})
	}
}
