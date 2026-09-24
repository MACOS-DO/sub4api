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

func TestCodexTicketModelParticipationRequiresExplicitFalse(t *testing.T) {
	const model = "gpt-6-astra"
	for _, test := range []struct {
		name     string
		models   any
		excluded bool
	}{
		{name: "missing"},
		{name: "empty", models: map[string]any{}},
		{name: "typed false", models: map[string]bool{model: false}, excluded: true},
		{name: "json false", models: map[string]any{model: false}, excluded: true},
		{name: "enabled", models: map[string]bool{model: true}},
		{name: "other model", models: map[string]bool{"gpt-5.5": false}},
		{name: "string false", models: map[string]any{model: "false"}},
		{name: "null", models: map[string]any{model: nil}},
		{name: "invalid map", models: "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := ticketTestAccount(71)
			account.Extra = map[string]any{"codex_allow_without_ticket": false, codexTicketModelsEnabledKey: test.models}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
			require.Equal(t, !test.excluded, CodexTicketHarvestEnabled(account, model))
			require.Equal(t, !test.excluded, svc.openAICodexTicketBlocksAccount(account, model))
			require.Equal(t, test.excluded, OpenAICodexAllowsWithoutTicket(account, "  "+model+"  ", false))
			require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-5.6-sol"), "one excluded model cannot exempt another")
			for _, status := range OpenAICodexTicketStatuses(account, svc.openAICodexTicketConfig(), time.Now()) {
				if status.Model == model {
					require.Equal(t, !test.excluded, status.Blocked)
				}
			}
		})
	}
	require.False(t, OpenAICodexAllowsWithoutTicket(nil, "", false), "reading global policy has no model participation override")
	require.True(t, OpenAICodexAllowsWithoutTicket(nil, "", true))
}

func TestCodexTicketModelParticipationReenableRestoresPolicy(t *testing.T) {
	account := ticketTestAccount(71)
	account.Extra = map[string]any{"codex_allow_without_ticket": false}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	svc.accountRepo = &codexParticipationRepo{account: account}
	for _, enabled := range []bool{false, true, false} {
		require.NoError(t, svc.SetCodexTicketParticipation(context.Background(), account.ID, true,
			map[string]bool{"gpt-6-astra": enabled, "gpt-5.6-sol": true}))
		require.Equal(t, enabled, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
		require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-5.6-sol"))
		require.Equal(t, false, account.Extra["codex_allow_without_ticket"])
	}
}

func TestCodexTicketModelParticipationHTTPAndWebSocketRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, savedTicket := range []bool{false, true} {
		account := ticketTestAccount(71)
		account.Extra = map[string]any{"codex_allow_without_ticket": false,
			codexTicketModelsEnabledKey: map[string]bool{"gpt-6-astra": false, "gpt-5.6-sol": true}}
		if savedTicket {
			account.Extra[openAICodexTicketExtraKey("gpt-6-astra")] = verifiedTicket(account, "gpt-6-astra", "saved-state", "__oailb=saved")
		}
		svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
		for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
			for _, transport := range []string{"http", "passthrough", "websocket"} {
				t.Run(fmt.Sprintf("%s/%s/ticket=%t", transport, model, savedTicket), func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"model":%q,"input":[]}`, model))
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
					raw, marshalErr := json.Marshal(account.Extra[openAICodexTicketExtraKey(model)])
					require.NoError(t, marshalErr)
					lookup := &codexTicketLookupStub{raw: raw}
					svc.openaiCodexTicketLifecycle = lookup
					var headers http.Header
					var err error
					if transport == "websocket" {
						var ticket *openAICodexTicket
						headers, _, ticket, err = svc.buildOpenAIWSHeadersWithTicket(context.Background(), c, account, "token", OpenAIWSProtocolDecision{}, true, "", "", "session", model, "")
						if savedTicket && model == "gpt-6-astra" {
							require.NotNil(t, ticket)
						}
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
					if model == "gpt-5.6-sol" {
						require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
						return
					}
					require.NoError(t, err)
					if savedTicket {
						require.Equal(t, "saved-state", headers.Get(openAICodexTurnStateHeader))
						require.Equal(t, "__oailb=saved", headers.Get("Cookie"))
					} else {
						require.Empty(t, headers.Get(openAICodexTurnStateHeader))
						require.Empty(t, headers.Get("Cookie"))
					}
				})
			}
		}
	}
}

func TestCodexTicketModelParticipationUsesActualOutboundModel(t *testing.T) {
	for _, test := range []struct {
		name, requested, forward, mapped, compactMapped, fallback, outbound string
		compact, blocked                                                    bool
	}{
		{name: "mapped to excluded", requested: "gpt-5.6-sol", mapped: "gpt-6-astra", outbound: "gpt-6-astra"},
		{name: "channel mapping to excluded", requested: "gpt-5.6-sol", forward: "gpt-6-astra", outbound: "gpt-6-astra"},
		{name: "mapped to participating", requested: "gpt-6-astra", mapped: "gpt-5.6-sol", outbound: "gpt-5.6-sol", blocked: true},
		{name: "compact mapping to excluded", requested: "gpt-5.6-sol", compactMapped: "gpt-6-astra", outbound: "gpt-6-astra", compact: true},
		{name: "compact mapping to participating", requested: "gpt-6-astra", compactMapped: "gpt-5.6-sol", outbound: "gpt-5.6-sol", compact: true, blocked: true},
		{name: "compact fallback to excluded", requested: "gpt-5.6-sol", fallback: "gpt-6-astra", outbound: "gpt-6-astra", compact: true},
	} {
		for _, advanced := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/advanced=%t", test.name, advanced), func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				account := ticketTestAccount(71)
				account.Schedulable, account.Concurrency = true, 1
				account.Extra = map[string]any{"codex_allow_without_ticket": false,
					codexTicketModelsEnabledKey: map[string]bool{"gpt-6-astra": false, "gpt-5.6-sol": true}}
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
