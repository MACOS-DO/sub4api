package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Participation is account-level only. The retired per-model switches are ignored
// whatever they hold, so an account that once opted a model out is not stuck.
func TestCodexTicketParticipationIgnoresRetiredModelSwitches(t *testing.T) {
	const model = "gpt-6-astra"
	for _, test := range []struct {
		name     string
		extra    map[string]any
		excluded bool
	}{
		{name: "missing"},
		{name: "retired model false", extra: map[string]any{codexTicketRetiredModelsKey: map[string]any{model: false, openAICodexTicketHarvestModel: false}}},
		{name: "retired typed false", extra: map[string]any{codexTicketRetiredModelsKey: map[string]bool{openAICodexTicketHarvestModel: false}}},
		{name: "account disabled", extra: map[string]any{codexTicketAccountEnabledKey: false}, excluded: true},
		{name: "account enabled", extra: map[string]any{codexTicketAccountEnabledKey: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := ticketTestAccount(71)
			account.Extra = map[string]any{"codex_allow_without_ticket": false}
			for key, value := range test.extra {
				account.Extra[key] = value
			}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
			require.Equal(t, !test.excluded, CodexTicketHarvestEnabled(account))
			require.Equal(t, test.excluded, OpenAICodexAllowsWithoutTicket(account, false))
			for _, gated := range []string{model, "gpt-5.6-sol", openAICodexTicketHarvestModel} {
				require.Equal(t, !test.excluded, svc.openAICodexTicketBlocksAccount(account, gated), gated)
			}
			status := OpenAICodexTicketStatusOf(account, svc.openAICodexTicketConfig(), CodexSharedTickets{}, time.Now())
			require.Equal(t, !test.excluded, status.Blocked)
		})
	}
	require.False(t, OpenAICodexAllowsWithoutTicket(nil, false), "reading global policy has no participation override")
	require.True(t, OpenAICodexAllowsWithoutTicket(nil, true))
}

func TestCodexTicketParticipationReenableRestoresPolicy(t *testing.T) {
	account := ticketTestAccount(71)
	account.Extra = map[string]any{"codex_allow_without_ticket": false, codexTicketRetiredModelsKey: map[string]any{"gpt-6-astra": false}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	svc.accountRepo = &codexParticipationRepo{account: account}
	for _, enabled := range []bool{false, true, false, true} {
		require.NoError(t, svc.SetCodexTicketParticipation(context.Background(), account.ID, enabled))
		require.Equal(t, enabled, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
		require.Equal(t, enabled, svc.openAICodexTicketBlocksAccount(account, "gpt-5.6-sol"))
		require.Equal(t, false, account.Extra["codex_allow_without_ticket"])
	}
}

func TestCodexTicketParticipationHTTPAndWebSocketRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, savedTicket := range []bool{false, true} {
		account := ticketTestAccount(71)
		account.Extra = map[string]any{"codex_allow_without_ticket": false,
			codexTicketRetiredModelsKey: map[string]bool{"gpt-6-astra": false}}
		svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
		for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
			for _, transport := range []string{"http", "passthrough", "websocket"} {
				t.Run(fmt.Sprintf("%s/%s/ticket=%t", transport, model, savedTicket), func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"model":%q,"input":[]}`, model))
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
					// The global ticket was harvested by another account with gpt-6-sol.
					var shared *openAICodexTicket
					if savedTicket {
						shared = verifiedTicket(ticketTestAccount(72), openAICodexTicketHarvestModel, "saved-state", "__oailb=saved")
					}
					raw, marshalErr := json.Marshal(shared)
					require.NoError(t, marshalErr)
					lookup := &codexTicketLookupStub{raw: raw}
					svc.openaiCodexTicketLifecycle = lookup
					svc.openaiCodexTicket.Store(nil) // read this subtest's stub, not the previous subtest's cache
					var headers http.Header
					var err error
					if transport == "websocket" {
						var ticket *openAICodexTicket
						headers, _, ticket, err = svc.buildOpenAIWSHeadersWithTicket(context.Background(), c, account, "token", OpenAIWSProtocolDecision{}, true, "", "", "session", model, "")
						require.Equal(t, savedTicket, ticket != nil)
					} else {
						var req *http.Request
						if transport == "http" {
							req, err = svc.buildUpstreamRequest(context.Background(), c, account, body, "token", true, "session", true)
						} else {
							req, err = svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "token")
						}
						if req != nil {
							headers = req.Header
						}
					}
					require.Equal(t, 1, lookup.calls, "all transports must read the stored ticket")
					if !savedTicket {
						require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
						return
					}
					require.NoError(t, err)
					require.Equal(t, "__oailb=saved", headers.Get("Cookie"))
					require.Equal(t, "saved-state", headers.Get(openAICodexTurnStateHeader))
				})
			}
		}
	}
}

func TestCodexTicketGateUsesActualOutboundModel(t *testing.T) {
	for _, test := range []struct {
		name, requested, forward, mapped, compactMapped, fallback, outbound string
		compact, blocked                                                    bool
	}{
		{name: "mapped to ungated", requested: "gpt-5.6-sol", mapped: "gpt-5.5", outbound: "gpt-5.5"},
		{name: "channel mapping to ungated", requested: "gpt-5.6-sol", forward: "gpt-5.5", outbound: "gpt-5.5"},
		{name: "mapped to gated", requested: "gpt-5.5", mapped: "gpt-5.6-sol", outbound: "gpt-5.6-sol", blocked: true},
		{name: "compact mapping to ungated", requested: "gpt-5.6-sol", compactMapped: "gpt-5.5", outbound: "gpt-5.5", compact: true},
		{name: "compact mapping to gated", requested: "gpt-5.5", compactMapped: "gpt-5.6-sol", outbound: "gpt-5.6-sol", compact: true, blocked: true},
		{name: "compact fallback to ungated", requested: "gpt-5.6-sol", fallback: "gpt-5.5", outbound: "gpt-5.5", compact: true},
	} {
		for _, advanced := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/advanced=%t", test.name, advanced), func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				account := ticketTestAccount(71)
				account.Schedulable, account.Concurrency = true, 1
				account.Extra = map[string]any{"codex_allow_without_ticket": false}
				if test.mapped != "" {
					account.Credentials["model_mapping"] = map[string]any{test.requested: test.mapped}
				}
				if test.compactMapped != "" {
					account.Credentials["compact_model_mapping"] = map[string]any{test.requested: test.compactMapped}
				}
				svc := newOpenAICompactionSchedulerTestService([]Account{*account}, advanced)
				svc.cfg.Gateway.OpenAICodexTicket = config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}
				svc.cfg.Gateway.OpenAICompactModel = test.fallback
				forwardModel := test.requested
				ctx := context.Background()
				if test.forward != "" {
					forwardModel = test.forward
					ctx = WithOpenAIForwardModel(ctx, forwardModel, test.compact)
				}
				require.Equal(t, test.outbound, svc.openAICodexTicketOutboundModel(account, forwardModel, test.compact))
				selection, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "", test.requested, nil, OpenAIUpstreamTransportAny, test.compact)
				if test.blocked {
					require.Error(t, err)
					require.Nil(t, selection)
				} else {
					require.NoError(t, err)
					require.Equal(t, account.ID, selection.Account.ID)
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
				}

				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK,
					Header: http.Header{"Content-Type": []string{"application/json"}},
					Body:   io.NopCloser(strings.NewReader(`{"id":"resp_test","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))}}
				svc.httpUpstream = upstream
				body := []byte(fmt.Sprintf(`{"model":%q,"stream":false,"instructions":"test","input":"hello"}`, forwardModel))
				route := "/v1/responses"
				if test.compact {
					route += "/compact"
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, route, bytes.NewReader(body))
				result, err := svc.Forward(ctx, c, account, body)
				if test.blocked {
					require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
					require.Empty(t, upstream.requests)
				} else {
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, test.outbound, gjson.GetBytes(upstream.lastBody, "model").String())
				}
			})
		}
	}
}
