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

type codexTicketReconnectDialer struct {
	openAIWSQueueDialer
	sent []http.Header
}

func (d *codexTicketReconnectDialer) Dial(ctx context.Context, url string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	conn, _, _, err := d.openAIWSQueueDialer.Dial(ctx, url, headers, proxy)
	d.mu.Lock()
	d.sent = append(d.sent, headers.Clone())
	dialCount := d.dialCount
	d.mu.Unlock()
	response := http.Header{}
	if dialCount == 1 {
		response.Set(openAICodexTurnStateHeader, "previous-connection-state")
		response.Set("Set-Cookie", "__oailb=first-cookie; Path=/")
	} else {
		response.Set(openAICodexTurnStateHeader, "current-ticket")
		response.Set("Set-Cookie", "__oailb=rotated-cookie; Path=/")
	}
	return conn, http.StatusSwitchingProtocols, response, err
}

func TestCodexTicketWSReusedHandshakeDoesNotInvalidateNewGeneration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAICodexTicket.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	// 首会话用完 events 后归还池内，再次借出时首读即 EOF，等价于上游已判死的空闲连接。
	staleA := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_retry_first","model":"gpt-6-astra","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	fresh := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_retry_fresh","model":"gpt-6-astra","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &codexTicketReconnectDialer{openAIWSQueueDialer: openAIWSQueueDialer{conns: []openAIWSClientConn{staleA, fresh}}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          443,
		Name:        "ticket-reconnect-test",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "tok", "chatgpt_account_id": "test-account",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	ticket := verifiedTicket(account, "gpt-6-astra", "first-ticket", "__oailb=first-cookie")
	capture := &codexInvalidationCapture{current: ticket}
	svc.openaiCodexTicketLifecycle = capture
	svc.openaiCodexTicketWake = make(chan struct{}, 1)
	defer pool.Close()

	serverErrCh := make(chan error, 2)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
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

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	runSingleTurnSession := func(expectedResponseID string) {
		dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
		clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
		cancelDial()
		require.NoError(t, err)
		defer func() {
			_ = clientConn.CloseNow()
		}()

		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-6-astra","stream":false}`))
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		require.Equal(t, expectedResponseID, gjson.GetBytes(event, "response.id").String())

		require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

		select {
		case serverErr := <-serverErrCh:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			t.Fatal("等待 ingress websocket 结束超时")
		}
	}

	runSingleTurnSession("resp_retry_first")
	require.Empty(t, capture.events)
	freshTicket := verifiedTicket(account, "gpt-6-astra", "current-ticket", "__oailb=current-cookie")
	capture.current = freshTicket
	capture.onInvalidate = func() { capture.current = nil }
	key := openAICodexTicketKey(account.ID, freshTicket.Model)
	retry := time.Now().Add(time.Hour)
	svc.openaiCodexTickets.Store(key, freshTicket)
	svc.openaiCodexTicketNextAttempt.Store(key, retry)
	require.Equal(t, 1, dialer.DialCount())

	runSingleTurnSession("resp_retry_fresh")

	require.Equal(t, 2, dialer.DialCount(), "首读失败后的重试应新建连接")
	fresh.mu.Lock()
	freshWrites := len(fresh.writes)
	fresh.mu.Unlock()
	require.Equal(t, 1, freshWrites)
	// Reusing the old connection must keep its continuation state, without
	// attributing that state to the newly stored ticket generation.
	dialer.mu.Lock()
	sent := append([]http.Header(nil), dialer.sent...)
	dialer.mu.Unlock()
	require.Len(t, sent, 2)
	require.Equal(t, "previous-connection-state", sent[1].Get(openAICodexTurnStateHeader))
	require.Equal(t, freshTicket.Cookie, sent[1].Get("Cookie"))
	require.Empty(t, capture.events)
	require.Same(t, freshTicket, capture.current)
	cached, ok := svc.openaiCodexTickets.Load(key)
	require.True(t, ok)
	require.Same(t, freshTicket, cached)
	remainingRetry, ok := svc.openaiCodexTicketNextAttempt.Load(key)
	require.True(t, ok)
	require.Equal(t, retry, remainingRetry)
	require.Empty(t, svc.openaiCodexTicketWake)
}
