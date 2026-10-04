package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	"github.com/MACOS-DO/sub4api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
)

func (s *AccountTestService) testCodexGatewayAccount(c *gin.Context, account *Account, model, prompt, mode string) error {
	if s.openaiGatewayService == nil {
		return s.sendErrorAndEnd(c, "Gateway service is unavailable")
	}
	if model == "" {
		model = openai.DefaultTestModel
	}
	requestedModel := model
	_, model = resolveOpenAIForwardMappedModels(account, model, mode == AccountTestModeCompact)
	if prompt == "" {
		prompt = "Reply with OK."
	}
	body, err := json.Marshal(map[string]any{"model": model, "instructions": "", "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": prompt}}}}, "stream": true, "store": false})
	if err != nil {
		return s.sendErrorAndEnd(c, "Cannot prepare test request")
	}
	path := "/v1/responses"
	image := strings.HasPrefix(model, "gpt-image-")
	if mode == AccountTestModeCompact {
		body, _ = json.Marshal(createOpenAICompactProbePayload(model, true))
	} else if image {
		path = "/v1/images/generations"
		body, _ = json.Marshal(map[string]any{"model": model, "prompt": prompt, "n": 1})
	}
	s.sendEvent(c, TestEvent{Type: "test_start", Model: requestedModel})
	request, err := s.openaiGatewayService.newCodexGatewayRequest(c.Request.Context(), c, account, body, path)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	if mode == AccountTestModeCompact {
		ensureOpenAIRemoteCompactionV2BetaFeature(request.Header)
	}
	response, err := s.openaiGatewayService.doCodexGatewayRequest(request, account)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return s.sendErrorAndEnd(c, fmt.Sprintf("Gateway returned HTTP %d: %s", response.StatusCode, string(raw)))
	}
	if mode == AccountTestModeCompact || image {
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<20))
		if readErr != nil {
			return s.sendErrorAndEnd(c, "Cannot read Gateway test response")
		}
		if mode == AccountTestModeCompact {
			if !openAICompactProbeFoundCompactionItem(raw) {
				return s.sendErrorAndEnd(c, "No compaction output returned")
			}
			s.sendEvent(c, TestEvent{Type: "content", Text: "Compact probe succeeded (native remote compaction v2)"})
		} else {
			results, parseErr := parseCodexDirectImagesResponse(raw)
			if parseErr != nil || len(results) == 0 {
				return s.sendErrorAndEnd(c, "No image output returned")
			}
			for _, item := range results {
				mime := openAIImageOutputMIMEType(item.OutputFormat)
				s.sendEvent(c, TestEvent{Type: "image", ImageURL: "data:" + mime + ";base64," + item.Result, MimeType: mime})
			}
		}
		s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
		return nil
	}
	return s.processOpenAIStream(c, response.Body)
}

func (s *OpenAIGatewayService) codexGatewayProbeResponse(ctx context.Context, account *Account, model, proxyURL string, body []byte, replay http.Header) (*http.Response, error) {
	if s.codexGateway == nil || !s.codexGateway.Ready() || account.Gateway == nil {
		return nil, codexgateway.Unavailable()
	}
	headers := replay.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	applyOpenAICodexTicketHarvestIdentity(headers, model)
	source, err := resolveCredentialAccount(ctx, s.accountRepo, account)
	if err != nil {
		return nil, err
	}
	identity, err := s.resolveGatewayIdentity(ctx, source, 0, headers, body)
	if err != nil {
		return nil, err
	}
	emptyCookie := ""
	response, err := s.codexGateway.client.Forward(ctx, account.Gateway.BindingID, http.MethodPost, "/v1/responses", bytes.NewReader(body), headers, identity, account.OpenAIRequestTimezone(), &codexgateway.Transport{ProxyURL: proxyURL, CookieHeader: &emptyCookie, CaptureSetCookie: true})
	if err != nil {
		return nil, err
	}
	if err = codexgateway.DecodeResponse(response); err != nil {
		_ = response.Body.Close()
		return nil, err
	}
	return response, nil
}

func (s *OpenAIGatewayService) codexGatewayTicketProxy(ctx context.Context, account *Account, ticket *openAICodexTicket) (string, error) {
	if ticket != nil && ticket.ProxyID != nil {
		if s.settingService == nil || s.settingService.proxyRepo == nil {
			return "", ErrCodexTicketUnavailable
		}
		proxy, err := s.settingService.proxyRepo.GetByID(ctx, *ticket.ProxyID)
		if err != nil || proxy == nil || proxy.Status != StatusActive || proxy.IsExpired(time.Now()) {
			return "", ErrCodexTicketUnavailable
		}
		return proxy.URL(), nil
	}
	if account.Proxy != nil {
		return account.Proxy.URL(), nil
	}
	return "", nil
}
