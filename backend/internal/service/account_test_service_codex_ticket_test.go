//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newCodexTicketAccountTestService(t *testing.T, failClosed bool) (*AccountTestService, *OpenAIGatewayService, *queuedHTTPUpstream, *openAIAccountTestRepo) {
	t.Helper()
	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	repo := &openAIAccountTestRepo{}
	gateway := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled: true, TargetLength: 292, TTLSeconds: 3600, FailClosed: failClosed,
		Models: []string{"gpt-6-astra"},
	}, nil)
	svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}
	svc.openaiGatewayService = gateway
	return svc, gateway, upstream, repo
}

func TestAccountTestService_OpenAIOAuthInjectsCodexTicketAndCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := newTestContext()
	svc, gateway, upstream, _ := newCodexTicketAccountTestService(t, true)

	account := &Account{
		ID: 89, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
	}
	gateway.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID: account.ID, Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292,
		Cookie: "__cf_bm=abc; oai-did=xyz", CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})

	require.NoError(t, svc.testOpenAIAccountConnection(ctx, account, "gpt-6-astra", "", ""))
	require.Len(t, upstream.requests, 1)
	headers := upstream.requests[0].Header
	require.Equal(t, fakeCodexTicketState(292), headers.Get(openAICodexTurnStateHeader))
	require.Contains(t, headers.Get("Cookie"), "__cf_bm=abc")
}

func TestAccountTestService_OpenAIOAuthWarnsWhenTicketDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, recorder := newTestContext()
	svc, _, upstream, _ := newCodexTicketAccountTestService(t, true)

	account := &Account{
		ID: 90, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
	}

	require.NoError(t, svc.testOpenAIAccountConnection(ctx, account, "gpt-6-astra", "", ""))
	// 测试仍然发出站请求，仅告警，不改变“只验证凭据连通性”的语义。
	require.Len(t, upstream.requests, 1)
	require.Empty(t, upstream.requests[0].Header.Get(openAICodexTurnStateHeader))
	require.Contains(t, recorder.Body.String(), "警告")
}
