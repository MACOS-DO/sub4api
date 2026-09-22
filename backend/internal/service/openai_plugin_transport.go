package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
//
// Codex OAuth 账号默认使用官方一致的 OpenSSL TLS 指纹（无 ALPN，h1）；
// 未启用时退回标准 Go TLS 传输。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if profile := s.openAITLSFingerprintProfile(account, TLSFingerprintTransportHTTP); profile != nil {
		return s.httpUpstream.DoWithTLS(request, proxyURL, account.ID, account.Concurrency, profile)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}

// doOpenAIAccountTestUpstream 让 OpenAI OAuth 账号测试与真实转发使用同一插件路径。
// API Key 和未命中插件的账号保持各自原有的 HTTPUpstream 行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
) (*http.Response, error) {
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			ObserveHTTPUpstreamResponse(request, response)
			return response, err
		}
	}
	if useTLSFallback {
		return s.httpUpstream.DoWithTLS(
			request,
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}

// applyOpenAICodexTicketForTest 让账号测试与真实转发共用同一份票据注入逻辑：
// 有可用票据时注入 x-codex-turn-state 与 Cookie；无票且策略禁止无票时降级为
// SSE 警告，不阻断凭据连通性测试。
func (s *AccountTestService) applyOpenAICodexTicketForTest(ctx context.Context, c *gin.Context, account *Account, outboundModel string, h http.Header) {
	if s == nil || s.openaiGatewayService == nil || account == nil || h == nil {
		return
	}
	err := s.openaiGatewayService.applyOpenAICodexTicket(ctx, account, outboundModel, h)
	if err == nil || !errors.Is(err, ErrOpenAICodexTicketUnavailable) || c == nil {
		return
	}
	s.sendEvent(c, TestEvent{
		Type: "status",
		Text: "警告：该账号当前没有可用票据，且策略为禁止无票，真实网关流量会被拦截；本次测试仅验证凭据连通性。",
	})
}
