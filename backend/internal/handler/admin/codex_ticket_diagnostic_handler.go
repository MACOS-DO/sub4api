package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/response"
	"github.com/MACOS-DO/sub4api/internal/server/middleware"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func (h *AccountHandler) SetCodexTicketDiagnosticRouter(router http.Handler, keys *service.APIKeyService) {
	h.codexTicketRouter = router
	h.codexTicketAPIKeys = keys
}

type codexDiagnosticItem struct {
	Model            string                      `json:"model"`
	Status           string                      `json:"status"`
	Reason           string                      `json:"reason,omitempty"`
	PredictedModel   string                      `json:"predicted_model,omitempty"`
	Probability      float64                     `json:"probability,omitempty"`
	ParsedCount      int                         `json:"parsed_number_count,omitempty"`
	HTTPStatus       int                         `json:"http_status,omitempty"`
	GatewayErrorCode string                      `json:"gateway_error_code,omitempty"`
	Harvest          *service.CodexTicketAttempt `json:"harvest,omitempty"`
}

func codexDiagnosticGatewayError(raw []byte) string {
	if len(raw) > 16<<10 {
		raw = raw[:16<<10]
	}
	for _, path := range []string{"reason", "error.code"} {
		code := gjson.GetBytes(raw, path).String()
		if code == "" || len(code) > 64 {
			continue
		}
		for _, character := range code {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
				continue
			}
			code = ""
			break
		}
		if code != "" {
			return code
		}
	}
	return ""
}

func codexDiagnosticOutput(raw []byte) (string, bool) {
	completed := false
	streamed := false
	var output strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			continue
		}
		streamed = true
		switch gjson.Get(payload, "type").String() {
		case "response.output_text.delta":
			output.WriteString(gjson.Get(payload, "delta").String())
		case "response.completed":
			completed = true
			if output.Len() == 0 {
				gjson.Get(payload, "response.output").ForEach(func(_, message gjson.Result) bool {
					message.Get("content").ForEach(func(_, part gjson.Result) bool {
						if part.Get("type").String() == "output_text" {
							output.WriteString(part.Get("text").String())
						}
						return true
					})
					return true
				})
			}
		}
	}
	if streamed {
		return output.String(), completed && scanner.Err() == nil
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return "", false
	}
	if gjson.GetBytes(raw, "status").String() != "completed" {
		return "", false
	}
	if text := gjson.GetBytes(raw, "output_text").String(); text != "" {
		return text, true
	}
	return gjson.GetBytes(raw, "output.0.content.0.text").String(), true
}

func (h *AccountHandler) DiagnoseCodexModels(c *gin.Context) {
	accountID, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	if h.codexTicketRouter == nil || h.codexTicketAPIKeys == nil || h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	var input struct {
		APIKeyID int64    `json:"api_key_id"`
		Models   []string `json:"models"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.APIKeyID <= 0 || len(input.Models) == 0 || len(input.Models) > 32 {
		response.BadRequest(c, "Select an API key and up to 32 models")
		return
	}
	subject, authorized := middleware.GetAuthSubjectFromContext(c)
	if !authorized || subject.UserID <= 0 {
		response.ErrorWithDetails(c, http.StatusForbidden, "Admin user required", "FORBIDDEN", nil)
		return
	}
	key, err := h.codexTicketAPIKeys.GetByID(c.Request.Context(), input.APIKeyID)
	if err != nil || key == nil || key.UserID != subject.UserID || !key.IsActive() || key.IsExpired() || key.IsQuotaExhausted() || key.Key == "" {
		response.ErrorWithDetails(c, http.StatusForbidden, "API key unavailable or not owned by this admin", "API_KEY_UNAVAILABLE", nil)
		return
	}
	allowed, err := service.ModelTraceGPTModels()
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	modelSet := make(map[string]bool, len(allowed))
	for _, model := range allowed {
		modelSet[model] = true
	}
	seen := make(map[string]bool, len(input.Models))
	for _, model := range input.Models {
		if !modelSet[model] || seen[model] {
			response.BadRequest(c, "Invalid or duplicate GPT model")
			return
		}
		seen[model] = true
	}
	ticketModels, err := service.ModelTraceTicketModels()
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	ticketEligible := make(map[string]bool, len(ticketModels))
	for _, model := range ticketModels {
		ticketEligible[model] = true
	}
	results := make([]codexDiagnosticItem, 0, len(input.Models))
	for _, model := range input.Models {
		if c.Request.Context().Err() != nil {
			break
		}
		item := codexDiagnosticItem{Model: model, Status: "failed"}
		limited, err := h.codexTicketGateway.CodexTicketDiagnosticLimited(c.Request.Context(), accountID, model)
		if err != nil {
			item.Reason = "account_lookup_failed"
			results = append(results, item)
			continue
		}
		if limited {
			item.Reason = "rate_limited"
			results = append(results, item)
			continue
		}
		generation := ""
		if ticketEligible[model] {
			generation, err = h.codexTicketGateway.HasCodexTicket(c.Request.Context(), accountID, model)
		}
		if err != nil {
			item.Reason = "ticket_lookup_failed"
			results = append(results, item)
			continue
		}
		if ticketEligible[model] && generation == "" {
			harvest, err := h.codexTicketGateway.DiagnosticCodexTicketHarvest(c.Request.Context(), accountID, model)
			if err != nil {
				item.Reason = "harvest_unavailable"
				if errors.Is(err, service.ErrCodexTicketRateLimited) {
					item.Reason = "rate_limited"
				}
				results = append(results, item)
				continue
			}
			item.Harvest = &harvest.CodexTicketAttempt
			if harvest.Outcome != "success" {
				item.Reason = "harvest_" + harvest.ReasonCode
				results = append(results, item)
				continue
			}
			generation, err = h.codexTicketGateway.HasCodexTicket(c.Request.Context(), accountID, model)
			if err != nil || generation == "" {
				item.Reason = "ticket_lookup_failed"
				results = append(results, item)
				continue
			}
		}
		body, _ := json.Marshal(map[string]any{"model": model, "stream": true, "store": false, "input": []any{map[string]any{"role": "user", "content": service.ModelTraceChallengePrompt(292)}}})
		modelCtx, cancel := context.WithTimeout(service.WithCodexTicketDiagnostic(c.Request.Context(), accountID), 120*time.Second)
		request, requestErr := http.NewRequestWithContext(modelCtx, http.MethodPost, "/v1/responses", bytes.NewReader(body))
		if requestErr != nil {
			cancel()
			item.Reason = "request_build_failed"
			results = append(results, item)
			continue
		}
		request.Header.Set("Authorization", "Bearer "+key.Key)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "text/event-stream")
		request.RemoteAddr = c.Request.RemoteAddr
		request.Host = c.Request.Host
		writer := httptest.NewRecorder()
		h.codexTicketRouter.ServeHTTP(writer, request)
		cancel()
		item.HTTPStatus = writer.Code
		currentGeneration, lookupErr := "", error(nil)
		if ticketEligible[model] {
			currentGeneration, lookupErr = h.codexTicketGateway.HasCodexTicket(c.Request.Context(), accountID, model)
		}
		if lookupErr != nil || currentGeneration != generation {
			item.Status, item.Reason = "uncertain", "ticket_invalidated_during_test"
			_, _ = h.codexTicketGateway.DiagnosticCodexTicketHarvest(c.Request.Context(), accountID, model)
			results = append(results, item)
			continue
		}
		if writer.Code < 200 || writer.Code >= 300 {
			item.Reason = "gateway_request_failed"
			item.GatewayErrorCode = codexDiagnosticGatewayError(writer.Body.Bytes())
			results = append(results, item)
			continue
		}
		output, readErr := io.ReadAll(io.LimitReader(writer.Body, 2<<20))
		if readErr != nil {
			item.Reason = "response_read_failed"
			results = append(results, item)
			continue
		}
		diagnosticText, complete := codexDiagnosticOutput(output)
		if !complete {
			item.Reason = "response_incomplete"
			results = append(results, item)
			continue
		}
		prediction, _, predictErr := service.ModelTracePredictCommitted(diagnosticText, 292)
		item.ParsedCount = prediction.ParsedCount
		if predictErr != nil {
			item.Reason = "insufficient_numbers"
			results = append(results, item)
			continue
		}
		item.PredictedModel, item.Probability = prediction.Model, prediction.Probability
		if prediction.Model == model {
			item.Status = "normal"
		} else {
			item.Status = "degraded"
			item.Reason = "fingerprint_mismatch"
		}
		results = append(results, item)
	}
	response.Success(c, gin.H{"items": results, "canceled": c.Request.Context().Err() != nil})
}
