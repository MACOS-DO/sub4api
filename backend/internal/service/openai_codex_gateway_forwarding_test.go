package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Only the service contract is mocked; both WS hops use real local sockets.
func codexGatewayDataFixture(t *testing.T, handler http.HandlerFunc) (*OpenAIGatewayService, *Account) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/capabilities" {
			_ = json.NewEncoder(w).Encode(codexgateway.Capabilities{APIVersion: 4, StorageMode: "postgres", SharedPGLayoutSupported: true, PersistentAccountOperationsSupported: true, MaxRequestBytes: 1 << 20})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	key := filepath.Join(t.TempDir(), "service-key")
	require.NoError(t, os.WriteFile(key, []byte("synthetic-forwarding-service-key"), 0600))
	client, err := codexgateway.New(codexgateway.Config{Enabled: true, BaseURL: server.URL, ServiceKeyFile: key})
	require.NoError(t, err)
	require.NoError(t, client.Check(context.Background()))
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.Codex4Server.BaseURL = server.URL
	cfg.Gateway.MaxBodySize = 1 << 20
	svc := newPassthroughLifecycleService(cfg, newStagedPassthroughConn())
	a := codexIdentityTestAccount("off")
	svc.accountRepo = &codexRecoveryRepository{account: a}
	svc.codexGateway = &CodexGatewayService{client: client, bindings: &codexRecoveryRepository{}}
	return svc, a
}

func TestCodexGatewayWebSocketMapsEveryTurnAndSettlesOnce(t *testing.T) {
	for _, withChannel := range []bool{false, true} {
		t.Run(fmt.Sprintf("channel=%t", withChannel), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			captured := make(chan []byte, 4)
			handshakes := make(chan http.Header, 1)
			svc, a := codexGatewayDataFixture(t, func(w http.ResponseWriter, r *http.Request) {
				handshakes <- r.Header.Clone()
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				for turn := 1; ; turn++ {
					_, body, err := conn.Read(ctx)
					if err != nil {
						return
					}
					captured <- body
					model := gjson.GetBytes(body, "model").String()
					event := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_%d","model":%q,"status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":3}}}`, turn, model)
					if conn.Write(ctx, coderws.MessageText, []byte(event)) != nil {
						return
					}
				}
			})
			mappingKey := "public-*"
			if withChannel {
				mappingKey = "channel-*"
			}
			models := svc.openAICodexTicketConfig().Models
			require.NotEmpty(t, models)
			upstreamModel := models[0]
			svc.cfg.Gateway.OpenAICodexTicket.Enabled = true
			svc.cfg.Gateway.OpenAICodexTicket.FailClosed = true
			a.Extra[openAICodexTicketExtraKey(upstreamModel)] = verifiedTicket(a, upstreamModel, "mapped-model-ticket", "route=mapped")
			a.Credentials = map[string]any{"model_mapping": map[string]any{mappingKey: upstreamModel, upstreamModel: "wrong-if-mapped-twice"}}
			settled := make(chan *OpenAIForwardResult, 4)
			admitted := make(chan int, 4)
			server, _ := startPassthroughLifecycleServerWithHooks(t, ctx, svc, a, func(*gin.Context) *OpenAIWSIngressHooks {
				return &OpenAIWSIngressHooks{
					MapRequestModel: func(_ int, model string) (string, error) {
						if withChannel {
							return "channel-model", nil
						}
						return model, nil
					},
					BeforeTurn: func(turn int) error { admitted <- turn; return nil },
					AfterTurn: func(_ int, result *OpenAIForwardResult, err error) {
						if err == nil {
							settled <- result
						}
					},
				}
			})
			defer server.Close()
			client := dialPassthroughLifecycleClientWithPayload(t, server, `{"type":"response.create","model":"public-model","input":[]}`)
			defer client.CloseNow()
			select {
			case headers := <-handshakes:
				require.Equal(t, "mapped-model-ticket", headers.Get(openAIWSTurnStateHeader))
			case <-ctx.Done():
				t.Fatal("Gateway handshake was not opened")
			}
			for turn := 1; turn <= 4; turn++ {
				if turn > 1 {
					modelField := ""
					if turn == 3 {
						modelField = `,"model":"public-next"`
					}
					payload := fmt.Sprintf(`{"type":"response.create","previous_response_id":"resp_%d","input":[]%s}`, turn-1, modelField)
					require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(payload)))
				}
				select {
				case body := <-captured:
					require.Equal(t, upstreamModel, gjson.GetBytes(body, "model").String())
					require.Equal(t, "mapped-model-ticket", gjson.GetBytes(body, "client_metadata.x-codex-turn-state").String())
					if turn > 1 {
						require.Equal(t, fmt.Sprintf("resp_%d", turn-1), gjson.GetBytes(body, "previous_response_id").String())
					}
				case <-ctx.Done():
					t.Fatal("mapped frame did not reach Gateway")
				}
				_, _, err := client.Read(ctx)
				require.NoError(t, err)
				select {
				case result := <-settled:
					requestedModel := "public-model"
					if turn >= 3 {
						requestedModel = "public-next"
					}
					require.Equal(t, requestedModel, result.Model)
					require.Equal(t, upstreamModel, result.UpstreamModel)
					require.Equal(t, 7, result.Usage.InputTokens)
					require.Equal(t, 3, result.Usage.OutputTokens)
				case <-ctx.Done():
					t.Fatal("turn was not settled")
				}
			}
			for _, turn := range []int{2, 3, 4} {
				require.Equal(t, turn, <-admitted, "every later turn must pass admission")
			}
			require.Empty(t, admitted)
			require.Empty(t, settled, "each terminal event must settle only once")
			require.Empty(t, handshakes, "all turns must use the original connection")
		})
	}
}

func TestCodexGatewayModelSwitchRequiresMatchingHandshakeTicket(t *testing.T) {
	svc, a := codexGatewayDataFixture(t, func(http.ResponseWriter, *http.Request) {})
	svc.cfg.Gateway.OpenAICodexTicket.Enabled = true
	svc.cfg.Gateway.OpenAICodexTicket.FailClosed = true
	models := svc.openAICodexTicketConfig().Models
	require.GreaterOrEqual(t, len(models), 2)
	first := verifiedTicket(a, models[0], "first-ticket", "route=first")
	second := verifiedTicket(a, models[1], "second-ticket", "route=second")
	a.Extra[openAICodexTicketExtraKey(models[0])] = first
	a.Extra[openAICodexTicketExtraKey(models[1])] = second
	c := &codexGatewayFrameConn{service: svc, ticketAccount: a, handshakeModel: models[0], turnState: first.State, headers: http.Header{"Cookie": {first.Cookie}}}
	require.NoError(t, c.validateModelTicket(context.Background(), models[0]))
	err := c.validateModelTicket(context.Background(), models[1])
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	second.State, second.Cookie = first.State, first.Cookie
	require.NoError(t, c.validateModelTicket(context.Background(), models[1]))
	// A refreshed generation for the original model does not alter this socket.
	a.Extra[openAICodexTicketExtraKey(models[0])] = verifiedTicket(a, models[0], "new-generation", "route=new")
	require.NoError(t, c.validateModelTicket(context.Background(), models[0]))
	require.Equal(t, "first-ticket", c.turnState)
}

func TestCodexGatewayAccountProbeUsesMappedModel(t *testing.T) {
	captured := make(chan string, 1)
	svc, a := codexGatewayDataFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		captured <- body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	})
	a.Credentials = map[string]any{"model_mapping": map[string]any{"test-alias": "gpt-5.5"}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	tests := &AccountTestService{openaiGatewayService: svc}
	_ = tests.testCodexGatewayAccount(c, a, "test-alias", "OK", "default")
	require.Equal(t, "gpt-5.5", <-captured)
	require.Contains(t, recorder.Body.String(), "test-alias")
}
