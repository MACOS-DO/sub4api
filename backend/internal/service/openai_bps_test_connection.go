package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Use the production adapter, Redis state and transport for connectivity tests.
// A fresh scope ensures tests never attach to a real client's conversation.
func (s *AccountTestService) testOpenAIBPSAccountConnection(c *gin.Context, account *Account, model, prompt, mode string) error {
	if s.openaiGatewayService == nil {
		return s.sendErrorAndEnd(c, "BPS gateway is unavailable")
	}
	if model == "" {
		model = OpenAIBPSDefaultModels()[0]
	}
	if !account.IsModelSupported(model) {
		return s.sendErrorAndEnd(c, "Model is not allowed by this account")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Reply with OK."
	}
	s.sendEvent(c, TestEvent{Type: "test_start", Model: model})
	path := "/v1/responses"
	if mode == "compact" {
		path += "/compact"
	}
	body := []byte(bpsJSON(map[string]any{"model": model, "input": prompt, "stream": false, "prompt_cache_key": "bps-probe-" + uuid.NewString()}))
	rec := httptest.NewRecorder()
	probe, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	probe.Request = httptest.NewRequest("POST", path, strings.NewReader(string(body))).WithContext(ctx)
	if err := s.openaiGatewayService.PrepareOpenAIBPSRouting(probe, body); err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	_, err := s.openaiGatewayService.forwardOpenAIBPS(probe.Request.Context(), probe, account, body)
	if err != nil {
		var typed *bpsError
		if errors.As(err, &typed) {
			return s.sendErrorAndEnd(c, typed.Message)
		}
		return s.sendErrorAndEnd(c, "BPS upstream request failed")
	}
	var response map[string]any
	if bpsDecode(rec.Body.Bytes(), &response) != nil {
		return s.sendErrorAndEnd(c, "Invalid BPS response")
	}
	text := bpsOutputText(response)
	if mode == "compact" {
		text = "BPS conversation summary saved successfully."
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: text})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}
