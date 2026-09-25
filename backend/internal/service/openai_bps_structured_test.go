package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func bpsTestStructuredFormat(strict bool) map[string]any {
	return map[string]any{"type": "json_schema", "name": "answer", "strict": strict, "schema": map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []any{"ok"}, "additionalProperties": false}}
}
func bpsTestStructuredRequest(format map[string]any, stream bool) map[string]any {
	return map[string]any{"model": "gpt-6-astra", "input": "Answer the request", "stream": stream, "text": map[string]any{"format": format}}
}

func TestOpenAIBPSStructuredJSONAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, strict := range []bool{false, true} {
			for _, valid := range []bool{false, true} {
				name := bpsJSON([]bool{stream, strict, valid})
				t.Run(name, func(t *testing.T) {
					s, a := bpsFixture()
					c, rec := bpsContext(1, "/responses")
					format := bpsTestStructuredFormat(strict)
					text := `{"ok":true}`
					if !valid {
						text = `{"ok":"wrong"}`
					}
					final := bpsResponse(bpsText(text))
					up := &bpsHTTPStub{contentType: "application/json", body: bpsJSON(final)}
					if stream {
						up.contentType = "text/event-stream"
						up.body = bpsEvent("response.created", map[string]any{"response": map[string]any{"id": "r1", "status": "in_progress", "output": []any{}, "output_text": "unchecked-created"}}) +
							bpsEvent("response.output_item.added", map[string]any{"output_index": 0, "item": bpsText("unchecked-item")}) +
							bpsEvent("response.output_text.delta", map[string]any{"item_id": "msg_test", "output_index": 0, "content_index": 0, "delta": "unchecked-delta"}) +
							bpsEvent("response.output_item.done", map[string]any{"output_index": 0, "item": bpsText("unchecked-done")}) +
							bpsEvent("response.completed", map[string]any{"response": final})
					}
					s.httpUpstream = up
					result, err := s.Forward(c.Request.Context(), c, a, []byte(bpsJSON(bpsTestStructuredRequest(format, stream))))
					require.NotNil(t, result)
					require.Equal(t, 100, result.Usage.InputTokens)
					require.Equal(t, 12, result.Usage.OutputTokens)
					require.NotContains(t, rec.Body.String(), "unchecked-")
					require.False(t, gjson.GetBytes(up.requestBody, "text").Exists())
					require.Contains(t, string(up.requestBody), "structured final answer")
					require.Equal(t, 1, up.requests)
					if valid {
						require.NoError(t, err)
						if stream {
							require.Contains(t, rec.Body.String(), "event: response.output_text.delta")
							require.Contains(t, rec.Body.String(), "event: response.completed")
						} else {
							require.Equal(t, 200, rec.Code)
							require.Equal(t, "json_schema", gjson.Get(rec.Body.String(), "text.format.type").String())
							require.Equal(t, text, gjson.Get(rec.Body.String(), "output.0.content.0.text").String())
						}
					} else {
						require.Error(t, err)
						require.Contains(t, rec.Body.String(), "bps_invalid_structured_output")
						require.NotContains(t, rec.Body.String(), "event: response.completed")
						if stream {
							require.Contains(t, rec.Body.String(), "event: response.failed")
						} else {
							require.Equal(t, 502, rec.Code)
						}
					}
				})
			}
		}
	}
}

func TestOpenAIBPSStructuredValidationBoundaries(t *testing.T) {
	object, err := bpsPrepareStructuredOutput(map[string]any{"format": map[string]any{"type": "json_object"}})
	require.NoError(t, err)
	for _, text := range []string{`[]`, `null`, `true`, `{"ok":true} trailing`, `{"ok":true}{}`, "```json\n{}\n```", ""} {
		require.Error(t, object.validate(bpsResponse(bpsText(text))), text)
	}
	require.NoError(t, object.validate(bpsResponse(bpsText(`{"ok":true}`))))
	require.Error(t, object.validate(bpsResponse(bpsText(strings.Repeat(" ", bpsMaxStructuredTextBytes+1)))))
	var format map[string]any
	require.NoError(t, bpsDecode([]byte(`{"type":"json_schema","name":"large","schema":{"type":"object","properties":{"id":{"const":9007199254740993}},"required":["id"]}}`), &format))
	large, err := bpsPrepareStructuredOutput(map[string]any{"format": format})
	require.NoError(t, err)
	require.NoError(t, large.validate(bpsResponse(bpsText(`{"id":9007199254740993}`))))
	require.Error(t, large.validate(bpsResponse(bpsText(`{"id":9007199254740992}`))))
	require.IsType(t, json.Number(""), format["schema"].(map[string]any)["properties"].(map[string]any)["id"].(map[string]any)["const"])
	// Only references within this schema document are permitted.
	require.NoError(t, bpsDecode([]byte(`{"type":"json_schema","name":"local","schema":{"type":"object","$defs":{"flag":{"type":"boolean"}},"properties":{"ok":{"$ref":"#/$defs/flag"}},"required":["ok"]}}`), &format))
	local, err := bpsPrepareStructuredOutput(map[string]any{"format": format})
	require.NoError(t, err)
	require.NoError(t, local.validate(bpsResponse(bpsText(`{"ok":true}`))))
	for _, schema := range []map[string]any{{"$ref": "file:///etc/passwd"}, {"$ref": "https://example.invalid/schema.json"}, {"type": "invalid"}, {"description": strings.Repeat("x", 1<<20)}} {
		_, err := bpsPrepareStructuredOutput(map[string]any{"format": map[string]any{"type": "json_schema", "name": "bad", "schema": schema}})
		require.Error(t, err)
	}
	for _, format := range []any{true, map[string]any{"type": "unknown"}, map[string]any{"type": "json_schema", "name": "bad name", "schema": map[string]any{}}, map[string]any{"type": "json_schema", "name": "ok", "strict": "yes", "schema": map[string]any{}}, map[string]any{"type": "json_schema", "name": "ok"}} {
		s, a := bpsFixture()
		up := &bpsHTTPStub{}
		s.httpUpstream = up
		c, rec := bpsContext(1, "/responses")
		_, err := s.Forward(c.Request.Context(), c, a, []byte(bpsJSON(map[string]any{"model": "gpt-6-astra", "input": "test", "text": map[string]any{"format": format}})))
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Zero(t, up.requests)
	}
}

func TestOpenAIBPSStructuredToolsRefusalsAndCompaction(t *testing.T) {
	for _, stream := range []bool{false, true} {
		s, a := bpsFixture()
		c, rec := bpsContext(1, "/responses")
		source := bpsTestStructuredRequest(bpsTestStructuredFormat(true), stream)
		source["input"] = []any{bpsMessage("user", "inspect then return JSON")}
		source["tools"] = []any{bpsFunction("exec")}
		native := bpsNative("structured_tool", "exec", map[string]any{"command": "pwd"})
		s.httpUpstream = &bpsHTTPStub{body: bpsJSON(bpsResponse(bpsText("Inspecting first"), native)), contentType: "application/json"}
		_, err := s.Forward(c.Request.Context(), c, a, []byte(bpsJSON(source)))
		require.NoError(t, err)
		require.Contains(t, rec.Body.String(), "function_call")
		require.NotContains(t, rec.Body.String(), "run_officejs")
		call := map[string]any{"type": "function_call", "id": "fc_structured_tool", "call_id": "structured_tool", "name": "exec", "arguments": `{"command":"pwd"}`}
		source["input"] = append(source["input"].([]any), call, map[string]any{"type": "function_call_output", "call_id": "structured_tool", "output": "/workspace"})
		s.httpUpstream = &bpsHTTPStub{body: bpsJSON(bpsResponse(bpsText(`{"ok":true}`))), contentType: "application/json"}
		next, nextRec := bpsContext(1, "/responses")
		_, err = s.Forward(next.Request.Context(), next, a, []byte(bpsJSON(source)))
		require.NoError(t, err)
		require.NotContains(t, nextRec.Body.String(), "bps_invalid_structured_output")
		// A pure refusal is a protocol item, not a fabricated JSON answer.
		refusal := map[string]any{"type": "message", "id": "msg_refusal", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "refusal", "refusal": "Cannot provide that"}}}
		s.httpUpstream = &bpsHTTPStub{body: bpsJSON(bpsResponse(refusal)), contentType: "application/json"}
		refusalCtx, refusalRec := bpsContext(2, "/responses")
		_, err = s.Forward(refusalCtx.Request.Context(), refusalCtx, a, []byte(bpsJSON(bpsTestStructuredRequest(bpsTestStructuredFormat(true), stream))))
		require.NoError(t, err)
		require.Contains(t, refusalRec.Body.String(), "Cannot provide that")
		if stream {
			require.Contains(t, refusalRec.Body.String(), "response.refusal.delta")
			require.Contains(t, refusalRec.Body.String(), "response.refusal.done")
		}
	}
	s, a := bpsFixture()
	c, _ := bpsContext(1, "/responses/compact")
	r, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, []byte(bpsJSON(bpsTestStructuredRequest(bpsTestStructuredFormat(true), false))))
	require.NoError(t, err)
	require.Nil(t, r.Structured)
	response, err := s.bpsTransformResponse(c.Request.Context(), a, r, bpsResponse(bpsText("A plain summary.")))
	require.NoError(t, err)
	require.Equal(t, "compaction", response["output"].([]any)[0].(map[string]any)["type"])
}

func TestOpenAIBPSStructuredInterruptedStreamNeverLeaksText(t *testing.T) {
	for _, terminal := range []string{"", "response.incomplete", "response.failed"} {
		s, a := bpsFixture()
		c, rec := bpsContext(1, "/responses")
		raw := bpsEvent("response.created", map[string]any{"response": map[string]any{"id": "r", "status": "in_progress"}}) + bpsEvent("response.output_text.delta", map[string]any{"delta": "unvalidated partial content"})
		if terminal != "" {
			raw += bpsEvent(terminal, map[string]any{"response": map[string]any{"status": "failed", "usage": map[string]any{"input_tokens": 42}}})
		}
		s.httpUpstream = &bpsHTTPStub{contentType: "text/event-stream", body: raw}
		result, err := s.Forward(c.Request.Context(), c, a, []byte(bpsJSON(bpsTestStructuredRequest(bpsTestStructuredFormat(true), true))))
		require.Error(t, err)
		require.NotContains(t, rec.Body.String(), "unvalidated partial content")
		require.NotContains(t, rec.Body.String(), "event: response.completed")
		require.Contains(t, rec.Body.String(), "response.failed")
		if terminal != "" {
			require.Equal(t, 42, result.Usage.InputTokens)
		}
	}
}

func TestOpenAIBPSStructuredNullableMetadata(t *testing.T) {
	format := bpsTestStructuredFormat(false)
	format["strict"] = nil
	format["description"] = nil
	prepared, err := bpsPrepareStructuredOutput(map[string]any{"format": format})
	require.NoError(t, err)
	require.NoError(t, prepared.validate(bpsResponse(bpsText(`{"ok":true}`))))
	require.Error(t, prepared.validate(bpsResponse(bpsText(`{"ok":"wrong"}`))))
}
