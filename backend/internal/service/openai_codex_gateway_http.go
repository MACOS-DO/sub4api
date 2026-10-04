package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (s *OpenAIGatewayService) openAIForwardToken(ctx context.Context, account *Account) (string, error) {
	if account.IsOpenAICodex() {
		if s.codexGateway == nil || !s.codexGateway.Ready() {
			return "", codexgateway.Unavailable()
		}
		return "", nil
	}
	token, _, err := s.GetAccessToken(ctx, account)
	return token, err
}

func (s *OpenAIGatewayService) newCodexGatewayRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, path string) (*http.Request, error) {
	if s.codexGateway == nil || !s.codexGateway.Ready() || account.Gateway == nil || !account.Gateway.Usable() {
		return nil, codexgateway.Unavailable()
	}
	// These extensions belong to the internal service contract, never the caller.
	for _, field := range []string{"codex4server_identity", "codex4server_transport"} {
		if gjson.GetBytes(body, field).Exists() {
			cleaned, err := sjson.DeleteBytes(body, field)
			if err != nil {
				return nil, err
			}
			body = cleaned
		}
	}
	source := codexAccountIdentitySource(c, account)
	if source.IsShadow() {
		var err error
		source, err = resolveCredentialAccount(ctx, s.accountRepo, source)
		if err != nil {
			return nil, err
		}
	}
	var headers http.Header
	if c != nil && c.Request != nil {
		headers = c.Request.Header.Clone()
	} else {
		headers = make(http.Header)
	}
	headers.Del("Cookie")
	headers.Set("Content-Type", "application/json")
	headers.Del("Content-Encoding")
	enforceCodexIdentityHeaders(headers)
	identity, err := s.resolveGatewayIdentity(ctx, source, getAPIKeyIDFromContext(c), headers, body)
	if err != nil {
		return nil, writeCodexGatewayServiceFailure(c, err)
	}
	transport := &codexgateway.Transport{CaptureSetCookie: true}
	if account.Proxy != nil {
		transport.ProxyURL = account.Proxy.URL()
	}
	if path == "/v1/responses" || path == "/v1/responses/compact" || path == "/v1/images/generations" || path == "/v1/images/edits" {
		ticket, ticketErr := s.applyOpenAICodexTicketWithGeneration(ctx, account, gjson.GetBytes(body, "model").String(), headers)
		if ticketErr != nil {
			return nil, ticketErr
		}
		transport.ProxyURL, err = s.codexGatewayTicketProxy(ctx, account, ticket)
		if err != nil {
			return nil, err
		}
		if cookie := headers.Get("Cookie"); cookie != "" {
			transport.CookieHeader = &cookie
		}
	} else {
		transport = nil
	}
	return s.codexGateway.client.NewRequest(ctx, account.Gateway.BindingID, http.MethodPost, path, bytes.NewReader(body), headers, identity, account.OpenAIRequestTimezone(), transport)
}

func (s *OpenAIGatewayService) doCodexGatewayRequest(request *http.Request, account *Account) (*http.Response, error) {
	if s.codexGateway == nil {
		return nil, codexgateway.Unavailable()
	}
	snapshot := snapshotCodexTicketHTTPRequest(request, account)
	response, err := s.codexGateway.client.Do(request)
	if err != nil {
		return response, err
	}
	s.observeCodexTicketHTTPResponse(snapshot, response)
	if response.StatusCode >= 400 && response.Header.Get("X-Codex4Server-Error-Origin") == "gateway" {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		_ = response.Body.Close()
		return nil, codexgateway.ManagementResponseError(response, raw)
	}
	return response, nil
}

func CodexGatewayFailureWritten(c *gin.Context) bool {
	return c != nil && c.GetBool("codex_gateway_error_written")
}

func writeCodexGatewayServiceFailure(c *gin.Context, err error) error {
	var failure *codexgateway.Error
	if !errors.As(err, &failure) {
		failure = &codexgateway.Error{Status: 503, Origin: "gateway", Code: "codex_gateway_unavailable", Message: "Gateway service unavailable"}
	}
	if c != nil && !c.Writer.Written() {
		c.Set("codex_gateway_error_written", true)
		if c.Request != nil && strings.HasSuffix(c.Request.URL.Path, "/messages") {
			c.JSON(failure.Status, gin.H{"type": "error", "error": gin.H{"type": "api_error", "message": failure.Message}})
		} else {
			c.JSON(failure.Status, gin.H{"error": gin.H{"type": "server_error", "code": failure.Code, "message": failure.Message}})
		}
	}
	return failure
}

// Run before legacy credential lookup, including when capabilities failed at startup.
func (s *OpenAIGatewayService) checkCodexGatewayForwardReady(c *gin.Context, account *Account) error {
	if !account.IsOpenAICodex() {
		return nil
	}
	if s.codexGateway == nil || !s.codexGateway.Ready() {
		return writeCodexGatewayServiceFailure(c, codexgateway.Unavailable())
	}
	if account.Gateway == nil || !account.Gateway.Usable() {
		return writeCodexGatewayServiceFailure(c, &codexgateway.Error{Status: 503, Origin: "account", Code: "codex_account_unavailable", Message: "Gateway account is not ready"})
	}
	return nil
}
