package service

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func codexIdentityTestAccount(mode string) *Account {
	return &Account{ID: 12, Platform: PlatformOpenAICodex, Type: AccountTypeGateway, Status: StatusActive, Schedulable: true,
		Extra:   map[string]any{codexFingerprintSeedExtraKey: "8f6bb3da-5d25-40e6-b227-dfa44b237be9", codexFingerprintModeExtraKey: mode},
		Gateway: &CodexGatewayState{BindingID: "gateway-owner", SyncState: CodexSyncReady, Snapshot: &codexgateway.AccountStatus{ID: "gateway-owner", CanAcceptRequests: true}}}
}

func TestCodexGatewayIdentityModesPreserveExplicitAccountConvergence(t *testing.T) {
	headers := http.Header{"X-Codex-Installation-Id": {"device-client"}, "Session-Id": {"session-client"}, "Thread-Id": {"thread-client"}, "X-Codex-Turn-Id": {"turn-client"}}
	for _, mode := range []string{"off", "device", "session", "full"} {
		t.Run(mode, func(t *testing.T) {
			a := codexIdentityTestAccount(mode)
			one, err := resolveCodexGatewayIdentity(a, 1, headers, []byte(`{"prompt_cache_key":"session-client"}`))
			require.NoError(t, err)
			two, err := resolveCodexGatewayIdentity(a, 2, headers, []byte(`{"prompt_cache_key":"session-client"}`))
			require.NoError(t, err)
			if mode == "off" {
				require.NotEqual(t, one.InstallationID, two.InstallationID)
			} else {
				require.Equal(t, one.InstallationID, two.InstallationID)
			}
			if mode == "session" || mode == "full" {
				require.Equal(t, one.SessionID, two.SessionID)
				require.Equal(t, one.ThreadID, two.ThreadID)
				require.NotEqual(t, one.TurnID, two.TurnID)
				parsed, err := uuid.Parse(one.TurnID)
				require.NoError(t, err)
				require.Equal(t, uuid.Version(7), parsed.Version())
			} else {
				require.NotEqual(t, one.SessionID, two.SessionID)
				require.NotEqual(t, one.ThreadID, two.ThreadID)
			}
			require.Equal(t, one.SessionID, one.PromptCacheKey)
			if mode == "full" {
				require.Equal(t, one.SessionID, one.ThreadID)
			}
		})
	}
}

func TestCodexGatewayIdentityFallbackAndMetadataBoundaries(t *testing.T) {
	a := codexIdentityTestAccount("off")
	one, err := resolveCodexGatewayIdentity(a, 1, nil, []byte(`{"input":[{"session_id":"opaque"}],"tools":[{"session_id":"opaque"}],"client_metadata":{"ws_request_header_traceparent":"00-example"}}`))
	require.NoError(t, err)
	two, err := resolveCodexGatewayIdentity(a, 1, nil, []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, one.InstallationID, two.InstallationID)
	require.NotEqual(t, one.SessionID, two.SessionID)
	require.NotEqual(t, "opaque", one.SessionID)
	require.Equal(t, "00-example", one.Traceparent)
	a.Extra["openai_device_id"] = "configured-device"
	a.Extra[codexFingerprintModeExtraKey] = "device"
	three, err := resolveCodexGatewayIdentity(a, 99, nil, []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, "configured-device", three.InstallationID)
}

func TestCodexGatewayWSUsesHandshakeTicketAndTrustedContext(t *testing.T) {
	identity := codexgateway.Identity{Version: 1, InstallationID: "i", SessionID: "s", ThreadID: "t", TurnID: "turn"}
	input := []byte(`{"type":"response.create","codex4server_identity":{"installation_id":"forged"},"client_metadata":{"x-codex-turn-state":"stale","future":"preserved"},"input":[{"encrypted_content":"opaque-value"}]}`)
	output, err := codexGatewayWSMessage(input, identity, "selected")
	require.NoError(t, err)
	var value struct {
		Identity codexgateway.Identity `json:"codex4server_identity"`
		Metadata map[string]string     `json:"client_metadata"`
		Input    []map[string]string   `json:"input"`
	}
	require.NoError(t, json.Unmarshal(output, &value))
	require.Equal(t, identity, value.Identity)
	require.Equal(t, "selected", value.Metadata["x-codex-turn-state"])
	require.Equal(t, "preserved", value.Metadata["future"])
	require.Equal(t, "opaque-value", value.Input[0]["encrypted_content"])
}

func TestCodexGatewaySchedulingDoesNotOverwriteBusinessRestrictions(t *testing.T) {
	a := codexIdentityTestAccount("off")
	require.True(t, a.IsSchedulable())
	a.Status = StatusDisabled
	require.False(t, a.IsSchedulable())
	a.Status = StatusActive
	a.Gateway.SyncState = CodexSyncUnknown
	require.False(t, a.IsSchedulable())
	a.Gateway.SyncState = CodexSyncReady
	a.Gateway.Snapshot.CanAcceptRequests = false
	require.False(t, a.IsSchedulable())
	a.Gateway = nil
	require.False(t, a.IsSchedulable())
	require.Error(t, ValidateCodexBusinessCredentials(map[string]any{"access_token": "secret"}))
	require.Error(t, ValidateCodexBusinessCredentials(map[string]any{"header_overrides": map[string]any{"AUTHORIZATION": "secret"}}))
	require.Error(t, ValidateCodexBusinessCredentials(map[string]any{"header_overrides": map[string]any{"X-Auth-Token": "secret"}}))
}

func TestCodexGatewayWSRemovesStaleTicketWithoutHandshakeTicket(t *testing.T) {
	body, err := codexGatewayWSMessage([]byte(`{"type":"response.create","client_metadata":{"x-codex-turn-state":"stale"}}`), codexgateway.Identity{Version: 1}, "")
	require.NoError(t, err)
	require.NotContains(t, string(body), "stale")
}

type codexLineageTestCache struct {
	GatewayCache
	values map[string]string
}

func (c *codexLineageTestCache) GetCodexIdentity(_ context.Context, key string) (string, error) {
	return c.values[key], nil
}
func (c *codexLineageTestCache) PutCodexIdentities(_ context.Context, values map[string]string, _ time.Duration) error {
	for k, v := range values {
		c.values[k] = v
	}
	return nil
}

func TestCodexGatewayConvergedLineageSurvivesInstanceChange(t *testing.T) {
	cache := &codexLineageTestCache{values: map[string]string{}}
	one := &OpenAIGatewayService{cache: cache}
	two := &OpenAIGatewayService{cache: cache}
	account := codexIdentityTestAccount("session")
	ctx := context.Background()
	first, err := one.resolveGatewayIdentity(ctx, account, 7, nil, []byte(`{"session_id":"conversation","thread_id":"thread","turn_id":"first"}`))
	require.NoError(t, err)
	second, err := two.resolveGatewayIdentity(ctx, account, 7, nil, []byte(`{"session_id":"conversation","thread_id":"thread","turn_id":"second","parent_turn_id":"first","root_turn_id":"first","parent_thread_id":"thread"}`))
	require.NoError(t, err)
	require.Equal(t, first.TurnID, second.ParentTurnID)
	require.Equal(t, first.TurnID, second.RootTurnID)
	require.Equal(t, first.ThreadID, second.ParentThreadID)
	require.NotEqual(t, first.TurnID, second.TurnID)
	_, err = two.resolveGatewayIdentity(ctx, account, 8, nil, []byte(`{"session_id":"conversation","thread_id":"thread","turn_id":"third","parent_turn_id":"first"}`))
	require.Error(t, err, "converged upstream session does not share another user's reference map")
}

func TestCodexGatewayStartupFailureNeverEntersLegacyForwarding(t *testing.T) {
	gateway := &OpenAIGatewayService{}
	account := codexIdentityTestAccount("off")
	cases := map[string]func(*gin.Context) error{
		"responses": func(c *gin.Context) error {
			_, err := gateway.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.5"}`))
			return err
		},
		"chat": func(c *gin.Context) error {
			_, err := gateway.ForwardAsChatCompletions(context.Background(), c, account, []byte(`{}`), "", "")
			return err
		},
		"messages": func(c *gin.Context) error {
			_, err := gateway.ForwardAsAnthropic(context.Background(), c, account, []byte(`{}`), "", "")
			return err
		},
		"images": func(c *gin.Context) error {
			_, err := gateway.ForwardImages(context.Background(), c, account, []byte(`{}`), nil, "")
			return err
		},
		"search": func(c *gin.Context) error {
			_, err := gateway.ForwardAlphaSearch(context.Background(), c, account, []byte(`{}`))
			return err
		},
	}
	for name, forward := range cases {
		t.Run(name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			require.Error(t, forward(c))
			require.Equal(t, 503, writer.Code)
			require.True(t, CodexGatewayFailureWritten(c))
			require.True(t, json.Valid(writer.Body.Bytes()))
		})
	}
}
