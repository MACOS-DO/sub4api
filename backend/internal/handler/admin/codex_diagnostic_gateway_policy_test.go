package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexDiagnosticMissingTicketsAlwaysReachGateway(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "harvesting off", true: "harvesting on"}[enabled], func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAICodexTicket.Enabled = enabled
			account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive,
				Credentials: map[string]any{"chatgpt_account_id": "diagnostic-test-account"},
				Extra:       map[string]any{"codex_ticket_harvest_enabled": false, "codex_ticket_harvest_models": map[string]any{"gpt-6-astra": false}},
			}
			calls := 0
			var requestContext context.Context
			router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				requestContext = r.Context()
				require.Equal(t, "/v1/responses", r.URL.Path)
				require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
				deadline, ok := r.Context().Deadline()
				require.True(t, ok)
				require.InDelta(t, 120, time.Until(deadline).Seconds(), 2)
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Equal(t, "gpt-6-astra", gjson.GetBytes(body, "model").String())
				require.Equal(t, "gateway challenge", gjson.GetBytes(body, "input.4.content.0.text").String())
				// A ticket appearing during the request must not replace the model
				// result with a diagnostic-only ticket generation error.
				account.Extra["codex_turn_ticket:gpt-6-astra"] = map[string]any{"generation_id": "new-generation"}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]string{"status": "completed", "output_text": strings.Repeat("1 ", 332)}))
			})
			h, c, writer := newCodexDiagnosticHandlerForAccount(t, []string{"gpt-6-astra"}, router, cfg, account, nil)
			// No ticket lifecycle or harvesting repositories are installed; the
			// diagnostic must not access them before or after the gateway call.
			h.diagnoseCodexModels(c, func() (service.ModelTraceChallenge, error) {
				return service.ModelTraceChallenge{Prompt: "gateway challenge", ExpectedCount: 332}, nil
			})
			require.Equal(t, 1, calls)
			require.Equal(t, http.StatusOK, writer.Code)
			item := gjson.GetBytes(writer.Body.Bytes(), "data.items.0")
			require.Contains(t, []string{"normal", "degraded"}, item.Get("status").String())
			require.EqualValues(t, 332, item.Get("parsed_number_count").Int())
			require.False(t, item.Get("harvest").Exists())
			require.ErrorIs(t, requestContext.Err(), context.Canceled)
		})
	}
}

func TestCodexDiagnosticUsesGatewayFailureInsteadOfLocalPreflight(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		until := time.Now().Add(time.Hour)
		account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, RateLimitResetAt: &until, Credentials: map[string]any{"chatgpt_account_id": "diagnostic-test-account"}}
		calls := 0
		h, c, writer := newCodexDiagnosticHandlerForAccount(t, []string{"gpt-6-astra"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(code)
			_, err := io.WriteString(w, `{"error":{"code":"GATEWAY_POLICY_DENIED","message":"private detail"}}`)
			require.NoError(t, err)
		}), &config.Config{}, account, nil)
		h.DiagnoseCodexModels(c)
		require.Equal(t, 1, calls)
		item := gjson.GetBytes(writer.Body.Bytes(), "data.items.0")
		require.Equal(t, "failed", item.Get("status").String())
		require.Equal(t, "gateway_request_failed", item.Get("reason").String())
		require.Equal(t, "GATEWAY_POLICY_DENIED", item.Get("gateway_error_code").String())
		require.EqualValues(t, code, item.Get("http_status").Int())
		require.False(t, item.Get("harvest").Exists())
		require.NotContains(t, writer.Body.String(), "private detail")
	}
}

func TestCodexDiagnosticCancellationStopsFollowingModels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	h, c, writer := newCodexChallengeDiagnosticHandler(t, []string{"gpt-6-astra", "gpt-5.4"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		cancel()
		_, err := io.WriteString(w, `{"status":"incomplete"}`)
		require.NoError(t, err)
	}))
	c.Request = c.Request.WithContext(ctx)
	h.DiagnoseCodexModels(c)
	require.Equal(t, 1, calls)
	require.True(t, gjson.GetBytes(writer.Body.Bytes(), "data.canceled").Bool())
	require.Len(t, gjson.GetBytes(writer.Body.Bytes(), "data.items").Array(), 1)
}
