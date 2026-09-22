package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// codexLocationAlignWSPayload 是带 content_item_kinds 标记的首帧：对齐必须发生在
// WS compatibility normalization 删除该标记之前，否则关闭启发式后无法精确改写。
const codexLocationAlignWSPayload = `{"type":"response.create","model":"gpt-5.5","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/repo</cwd>\n  <current_date>2026-09-13</current_date>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["environments.environment_context"]}}]}`

func newCodexLocationWSAlignConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.CodexLocation = config.GatewayCodexLocationConfig{
		Enabled: true,
		// 关闭启发式：只有精确标记路径能改写，用来证明对齐发生在标记被删除之前。
		MarkerAbsentHeuristic: false,
		FallbackTimezone:      "Asia/Tokyo",
		FallbackCountry:       "JP",
		FallbackRegion:        "Tokyo",
		FallbackCity:          "Tokyo",
	}
	return cfg
}

func newCodexLocationWSAlignAccount() *Account {
	return &Account{
		ID:          41,
		Name:        "openai-codex-location-ws",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_enabled": true,
		},
	}
}

func requireCodexLocationAlignedWSPayload(t *testing.T, payload []byte) {
	t.Helper()
	text := gjson.GetBytes(payload, "input.0.content.0.text").String()
	require.Contains(t, text, "<timezone>America/New_York</timezone>")
	require.Contains(t, text, "<current_date>"+expectedDate(t, "America/New_York")+"</current_date>")
	require.NotContains(t, text, "Asia/Shanghai")
	require.False(t, gjson.GetBytes(payload, "input.0.internal_chat_message_metadata_passthrough").Exists())
}

func TestOpenAIWSCtxPoolAlignsLocationBeforeMarkerStrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newCodexLocationWSAlignConfig()

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_loc_ctx_pool","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
		codexLocation: &stubCodexLocationResolver{location: CodexLocation{
			Timezone: "America/New_York", Country: "US", Region: "Ohio", City: "Piketon",
		}},
	}
	account := newCodexLocationWSAlignAccount()

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "test-token", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(codexLocationAlignWSPayload))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)

	require.Len(t, captureConn.writes, 1)
	requireCodexLocationAlignedWSPayload(t, []byte(requestToJSONString(captureConn.writes[0])))
}

func TestOpenAIWSPassthroughAlignsLocationBeforeMarkerStrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)

	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_loc_passthrough","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)

	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.CodexLocation = config.GatewayCodexLocationConfig{
		Enabled:               true,
		MarkerAbsentHeuristic: false,
		FallbackTimezone:      "Asia/Tokyo",
		FallbackCountry:       "JP",
		FallbackRegion:        "Tokyo",
		FallbackCity:          "Tokyo",
	}
	svc := newPassthroughLifecycleService(cfg, upstream)
	svc.codexLocation = &stubCodexLocationResolver{location: CodexLocation{
		Timezone: "America/New_York", Country: "US", Region: "Ohio", City: "Piketon",
	}}

	account := newCodexLocationWSAlignAccount()
	account.ID = 42
	account.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModePassthrough

	server, _ := startPassthroughHookRecordingServer(t, controlCtx, svc, account, nil)
	defer server.Close()
	clientConn := dialPassthroughLifecycleClientWithPayload(t, server, codexLocationAlignWSPayload)
	defer func() { _ = clientConn.CloseNow() }()

	payload := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
	requireCodexLocationAlignedWSPayload(t, payload)
}
