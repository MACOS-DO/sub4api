package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// WriteOpenAIBPSError preserves an already-started SSE stream's protocol.
func WriteOpenAIBPSError(c *gin.Context, err error) {
	failure := &bpsError{502, "bps_upstream_error", "BPS upstream request failed"}
	var typed *bpsError
	if errors.As(err, &typed) {
		failure = typed
	}
	setOpsUpstreamError(c, failure.Status, failure.Message, "")
	payload := gin.H{"type": "server_error", "code": failure.Code, "message": failure.Message}
	if failure.Status == 400 || failure.Status == 409 {
		payload["type"] = "invalid_request_error"
	}
	if c.Writer.Written() && strings.Contains(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
		MarkOpsStreamError(c, failure.Code, failure.Message, failure.Status)
		sequence := c.GetInt("bps_sequence")
		responseID := c.GetString("bps_response_id")
		if responseID == "" {
			responseID = "resp_bps_error"
		}
		data, _ := json.Marshal(gin.H{"type": "response.failed", "sequence_number": sequence, "response": gin.H{"id": responseID, "object": "response", "status": "failed", "error": payload}})
		_, _ = fmt.Fprintf(c.Writer, "event: response.failed\ndata: %s\n\n", data)
		c.Writer.Flush()
		return
	}
	c.JSON(failure.Status, gin.H{"error": payload})
}

func buildOpenAIBPSRequest(ctx context.Context, account *Account, body []byte) (*http.Request, error) {
	if account.Type != AccountTypeOAuth || !account.IsOpenAIBPS() {
		return nil, bpsInvalid("Invalid BPS account")
	}
	token := strings.TrimSpace(account.GetCredential("access_token"))
	id := strings.TrimSpace(account.GetCredential("chatgpt_account_id"))
	if token == "" || id == "" || strings.ContainsAny(token+id, "\r\n") {
		return nil, &bpsError{401, "bps_invalid_credentials", "BPS credentials are incomplete"}
	}
	if account.bpsTokenExpired() {
		return nil, &bpsError{401, "bps_token_expired", "BPS access token has expired; replace it in the account settings"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OpenAIBPSResponsesURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("chatgpt-account-id", id)
	req.Header.Set("x-openai-account-id", id)
	req.Header.Set("x-basispoints-auth-mode", "chatgpt")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Origin", "https://bps.openai.com")
	for k, v := range map[string]string{"client-agent-profile": "excel", "client-editor": "excel", "client-host": "office", "client-platform": "excel", "client-platform-class": "PC", "client-product": "basispoints-excel-plugin", "client-runtime": "desktop", "office-host": "Excel", "office-platform": "PC"} {
		req.Header.Set("x-openai-internal-basispoints-"+k, v)
	}
	return req, nil
}

func (s *OpenAIGatewayService) bpsHTTPError(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, model string) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, openAIUpstreamErrorBodyReadLimit))
	message := sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(data))
	message = strings.ReplaceAll(message, account.GetCredential("access_token"), "[REDACTED]")
	if message == "" {
		message = fmt.Sprintf("BPS returned HTTP %d", resp.StatusCode)
	}
	var upstreamError struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &upstreamError)
	upstreamCode := strings.ToLower(strings.TrimSpace(upstreamError.Error.Code))
	diagnosticCode := strings.ReplaceAll(sanitizeUpstreamErrorMessage(strings.TrimSpace(upstreamError.Error.Code)), account.GetCredential("access_token"), "[REDACTED]")
	if len(diagnosticCode) > 128 {
		diagnosticCode = diagnosticCode[:128]
	}
	code := "bps_upstream_error"
	switch resp.StatusCode {
	case 401:
		code = "bps_invalid_credentials"
		if s.accountRepo != nil {
			_ = s.accountRepo.SetError(ctx, account.ID, "BPS authentication failed; replace access_token")
		}
	case 403:
		if upstreamCode == "model_not_allowed" || upstreamCode == "basispoints_model_access_changed" {
			code = "bps_model_not_allowed"
			if s.accountRepo != nil {
				_ = s.accountRepo.SetModelRateLimit(ctx, account.ID, model, time.Now().Add(30*time.Minute), "BPS model is not permitted: "+upstreamCode)
			}
		} else {
			// A workspace, policy or model entitlement 403 does not prove
			// that this access token is invalid for all models.
			code = "bps_access_denied"
		}
	case 429:
		delay := time.Minute
		if n, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && n > 0 {
			delay = time.Duration(n) * time.Second
		} else if until, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil && until.After(time.Now()) {
			delay = time.Until(until)
		}
		if s.accountRepo != nil {
			_ = s.accountRepo.SetRateLimited(ctx, account.ID, time.Now().Add(delay))
		}
	}
	if value := resp.Header.Get("Retry-After"); value != "" {
		c.Header("Retry-After", value)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: account.ID, AccountName: account.Name, Platform: account.Platform, UpstreamStatusCode: resp.StatusCode, Kind: "http_error", Message: message, Detail: diagnosticCode})
	if !bpsHasBinding(ctx) && (resp.StatusCode == 429 || resp.StatusCode >= 500) {
		return &UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: []byte(bpsJSON(map[string]any{"error": map[string]any{"message": message}})), ClientMessage: message}
	}
	return &bpsError{resp.StatusCode, code, message}
}

// readBPSEvents accepts SSE (including multi-line data) and unary JSON without
// converting an EOF into response.completed. Each event has a bounded size.
func readBPSEvents(resp *http.Response, maxSize int, consume func(string, map[string]any) error) error {
	if maxSize <= 0 {
		maxSize = 16 << 20
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		data, e := io.ReadAll(io.LimitReader(resp.Body, int64(maxSize)+1))
		if e != nil {
			return e
		}
		if len(data) > maxSize {
			return fmt.Errorf("BPS response exceeds maximum size")
		}
		var response map[string]any
		if e = bpsDecode(data, &response); e != nil {
			return e
		}
		event := "response.completed"
		if response["status"] != "completed" {
			event = "response.failed"
		}
		return consume(event, map[string]any{"type": event, "response": response})
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), maxSize)
	event := ""
	terminal := false
	var data strings.Builder
	dispatch := func() error {
		if data.Len() == 0 {
			event = ""
			return nil
		}
		text := strings.TrimSpace(data.String())
		data.Reset()
		if text == "[DONE]" {
			event = ""
			return nil
		}
		var payload map[string]any
		if e := bpsDecode([]byte(text), &payload); e != nil || payload == nil {
			return fmt.Errorf("Malformed BPS SSE event")
		}
		typ := stringValue(payload["type"])
		if typ == "" {
			typ = event
		}
		event = ""
		terminal = typ == "response.completed" || typ == "response.done" || typ == "response.failed" || typ == "response.incomplete" || typ == "error"
		return consume(typ, payload)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if e := dispatch(); e != nil {
				return e
			}
			if terminal {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			if data.Len() > maxSize {
				return fmt.Errorf("BPS event exceeds maximum size")
			}
		}
	}
	if e := scanner.Err(); e != nil {
		return e
	}
	return dispatch()
}

func bpsReadUsage(response map[string]any) OpenAIUsage {
	data, _ := json.Marshal(response["usage"])
	var usage struct {
		Input   int `json:"input_tokens"`
		Output  int `json:"output_tokens"`
		Details struct {
			Cached  int `json:"cached_tokens"`
			Written int `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
	}
	_ = json.Unmarshal(data, &usage)
	return OpenAIUsage{InputTokens: usage.Input, OutputTokens: usage.Output, CacheReadInputTokens: usage.Details.Cached, CacheCreationInputTokens: usage.Details.Written}
}

func (s *OpenAIGatewayService) forwardOpenAIBPS(ctx context.Context, c *gin.Context, account *Account, body []byte) (result *OpenAIForwardResult, err error) {
	started := time.Now()
	defer func() {
		if err != nil {
			var failover *UpstreamFailoverError
			if !errors.As(err, &failover) {
				WriteOpenAIBPSError(c, err)
			}
		}
	}()
	if _, ok := ctx.Value(bpsRoutingContextKey{}).(bpsRoutingContext); !ok {
		if err = s.PrepareOpenAIBPSRouting(c, body); err != nil {
			return nil, err
		}
		ctx = c.Request.Context()
	}
	if !bpsBoundAccountAllowed(ctx, account) {
		return nil, &bpsError{409, "bps_account_unavailable", "The account bound to this BPS conversation is unavailable"}
	}
	r, err := s.prepareOpenAIBPS(ctx, c, account, body)
	if err != nil {
		return nil, err
	}
	req, err := buildOpenAIBPSRequest(ctx, account, r.Body)
	if err != nil {
		if s.accountRepo != nil && account.bpsTokenExpired() {
			_ = s.accountRepo.SetError(ctx, account.ID, "BPS access token expired; replace it manually")
		}
		return nil, err
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	SetActualOpenAIUpstreamEndpoint(c, "/basispoints/api/responses")
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		if !bpsHasBinding(ctx) && ctx.Err() == nil {
			return nil, &UpstreamFailoverError{StatusCode: 502, ClientMessage: "BPS upstream is temporarily unavailable"}
		}
		return nil, err
	}
	defer resp.Body.Close()
	// Bound silent upstream stalls. The timer only closes the reader; all
	// downstream writes and stream state remain on this goroutine.
	var timedOut atomic.Bool
	idle := 5 * time.Minute
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		idle = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	timer := time.AfterFunc(idle, func() { timedOut.Store(true); _ = resp.Body.Close() })
	defer timer.Stop()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, s.bpsHTTPError(ctx, c, account, resp, r.Model)
	}
	if err = s.bpsBind(ctx, r.Scope, account); err != nil {
		return nil, err
	}
	result = &OpenAIForwardResult{Model: stringValue(r.Source["model"]), UpstreamModel: r.Model, BillingModel: r.Model, Stream: r.Stream, RequestID: resp.Header.Get("x-request-id"), UpstreamHeaders: resp.Header.Clone(), UpstreamEndpoint: "/basispoints/api/responses", ReasoningEffort: &r.Effort}
	sequence := 0
	created := false
	completed := false
	seenItems := map[string]bool{}
	pendingTools := map[string]bool{}
	emit := func(event string, payload map[string]any) error {
		if !r.Stream {
			return nil
		}
		if !c.Writer.Written() {
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Header("X-Accel-Buffering", "no")
		}
		payload["type"] = event
		payload["sequence_number"] = sequence
		sequence++
		c.Set("bps_sequence", sequence)
		if response, ok := payload["response"].(map[string]any); ok {
			c.Set("bps_response_id", response["id"])
		}
		data, _ := json.Marshal(payload)
		if _, e := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, data); e != nil {
			return e
		}
		c.Writer.Flush()
		if result.FirstTokenMs == nil && (event == "response.output_text.delta" || event == "response.output_item.done") {
			ms := int(time.Since(started).Milliseconds())
			result.FirstTokenMs = &ms
		}
		return nil
	}
	maxSize := 16 << 20
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxSize = s.cfg.Gateway.MaxLineSize
	}
	err = readBPSEvents(resp, maxSize, func(typ string, payload map[string]any) error {
		timer.Reset(idle)
		if completed {
			return nil
		}
		if typ == "response.failed" || typ == "response.incomplete" || typ == "error" {
			if v, ok := payload["response"].(map[string]any); ok {
				result.Usage = bpsReadUsage(v)
			}
			return &bpsError{502, "bps_response_failed", "BPS failed to complete the response"}
		}
		if typ == "response.completed" || typ == "response.done" {
			response, ok := payload["response"].(map[string]any)
			if !ok || response["status"] != "completed" {
				return &bpsError{502, "bps_invalid_response", "BPS terminal event is not a completed response"}
			}
			result.Usage = bpsReadUsage(response)
			result.ResponseID = stringValue(response["id"])
			result.UpstreamResponseModel = stringValue(response["model"])
			output, _ := response["output"].([]any)
			for _, raw := range output {
				item, _ := raw.(map[string]any)
				if item["type"] == "function_call" || item["type"] == "custom_tool_call" {
					delete(pendingTools, stringValue(item["id"])+"\x00"+stringValue(item["call_id"]))
				}
			}
			if len(pendingTools) > 0 {
				return &bpsError{502, "bps_invalid_tool_call", "BPS completed response omitted an original tool item"}
			}
			transformed, e := s.bpsTransformResponse(ctx, account, r, response)
			if e != nil {
				return e
			}
			if !r.Stream {
				if r.Compact && isOpenAIResponsesCompactPath(c) {
					transformed["object"] = "response.compaction"
				}
				c.JSON(http.StatusOK, transformed)
				completed = true
				return nil
			}
			if !created {
				initial := map[string]any{}
				for k, v := range transformed {
					initial[k] = v
				}
				initial["output"] = []any{}
				initial["status"] = "in_progress"
				if e = emit("response.created", map[string]any{"response": initial}); e != nil {
					return e
				}
				created = true
			}
			output, _ = transformed["output"].([]any)
			for i, raw := range output {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				kind := stringValue(item["type"])
				if kind != "function_call" && kind != "custom_tool_call" && kind != "compaction" {
					if seenItems[stringValue(item["id"])] {
						continue
					}
					if e = bpsEmitBufferedItem(i, item, emit); e != nil {
						return e
					}
					continue
				}
				added := map[string]any{}
				for k, v := range item {
					added[k] = v
				}
				added["status"] = "in_progress"
				if kind == "function_call" {
					added["arguments"] = ""
				}
				if kind == "custom_tool_call" {
					added["input"] = ""
				}
				if e = emit("response.output_item.added", map[string]any{"output_index": i, "item": added}); e != nil {
					return e
				}
				if kind == "function_call" || kind == "custom_tool_call" {
					prefix := "response.function_call_arguments"
					field := "arguments"
					if kind == "custom_tool_call" {
						prefix = "response.custom_tool_call_input"
						field = "input"
					}
					if e = emit(prefix+".delta", map[string]any{"item_id": item["id"], "output_index": i, "delta": item[field]}); e != nil {
						return e
					}
					if e = emit(prefix+".done", map[string]any{"item_id": item["id"], "output_index": i, field: item[field]}); e != nil {
						return e
					}
				}
				if e = emit("response.output_item.done", map[string]any{"output_index": i, "item": item}); e != nil {
					return e
				}
			}
			if e = emit("response.completed", map[string]any{"response": transformed}); e != nil {
				return e
			}
			completed = true
			return nil
		}
		item, _ := payload["item"].(map[string]any)
		if typ == "response.output_item.done" && (item["type"] == "function_call" || item["type"] == "custom_tool_call") {
			if len(pendingTools) >= 1024 {
				return &bpsError{502, "bps_invalid_tool_call", "BPS returned too many pending tool items"}
			}
			pendingTools[stringValue(item["id"])+"\x00"+stringValue(item["call_id"])] = true
		}
		if r.Compact {
			return nil
		}
		if r.Structured != nil && bpsStructuredMessageEvent(typ, item) {
			return nil
		}
		if strings.HasPrefix(typ, "response.function_call_arguments.") || strings.HasPrefix(typ, "response.custom_tool_call_input.") {
			return nil
		}
		if item, ok := payload["item"].(map[string]any); ok {
			if item["type"] == "function_call" || item["type"] == "custom_tool_call" {
				return nil
			}
			if item["type"] != "message" && item["type"] != "reasoning" {
				return &bpsError{502, "bps_unsupported_tool", "BPS returned an unsupported output item"}
			}
			if typ == "response.output_item.done" {
				seenItems[stringValue(item["id"])] = true
			}
		}
		if response, ok := payload["response"].(map[string]any); ok {
			response["model"] = r.Source["model"]
			response["output"] = []any{}
			if r.Structured != nil {
				delete(response, "output_text")
				response["text"] = map[string]any{"format": r.Structured.Format}
			}
		}
		if typ == "response.created" {
			created = true
		}
		if !bpsSafeStreamingEvent(typ) {
			return &bpsError{502, "bps_unsupported_event", "BPS returned an unsupported streaming event"}
		}
		return emit(typ, payload)
	})
	result.Duration = time.Since(started)
	if timedOut.Load() && !completed {
		err = &bpsError{504, "bps_upstream_timeout", "BPS upstream stopped sending data"}
	}
	if err == nil && !completed {
		err = &bpsError{502, "bps_stream_interrupted", "BPS stream ended before response.completed"}
	}
	if err != nil {
		result.UpstreamTerminalEvent = "response.failed"
		return result, err
	}
	result.UpstreamTerminalEvent = "response.completed"
	return result, nil
}

// Unary upstream responses still produce a complete Responses event sequence
// for a streaming client. Incremental SSE items have already been emitted.
func bpsEmitBufferedItem(index int, item map[string]any, emit func(string, map[string]any) error) error {
	added := map[string]any{}
	for k, v := range item {
		added[k] = v
	}
	added["status"] = "in_progress"
	if item["type"] == "message" {
		added["content"] = []any{}
	}
	if err := emit("response.output_item.added", map[string]any{"output_index": index, "item": added}); err != nil {
		return err
	}
	if item["type"] == "message" {
		parts, _ := item["content"].([]any)
		for i, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok || (part["type"] != "output_text" && part["type"] != "refusal") {
				continue
			}
			field, prefix := "text", "response.output_text"
			if part["type"] == "refusal" {
				field, prefix = "refusal", "response.refusal"
			}
			fields := func() map[string]any {
				return map[string]any{"item_id": item["id"], "output_index": index, "content_index": i}
			}
			payload := fields()
			empty := map[string]any{}
			for key, value := range part {
				empty[key] = value
			}
			empty[field] = ""
			payload["part"] = empty
			if err := emit("response.content_part.added", payload); err != nil {
				return err
			}
			payload = fields()
			payload["delta"] = part[field]
			if err := emit(prefix+".delta", payload); err != nil {
				return err
			}
			payload = fields()
			payload[field] = part[field]
			if err := emit(prefix+".done", payload); err != nil {
				return err
			}
			payload = fields()
			payload["part"] = part
			if err := emit("response.content_part.done", payload); err != nil {
				return err
			}
		}
	}
	return emit("response.output_item.done", map[string]any{"output_index": index, "item": item})
}

func bpsSafeStreamingEvent(typ string) bool {
	switch typ {
	case "response.created", "response.in_progress", "response.queued",
		"response.output_item.added", "response.output_item.done",
		"response.content_part.added", "response.content_part.done",
		"response.output_text.delta", "response.output_text.done", "response.output_text.annotation.added",
		"response.refusal.delta", "response.refusal.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_text.delta", "response.reasoning_text.done":
		return true
	default:
		return false
	}
}
