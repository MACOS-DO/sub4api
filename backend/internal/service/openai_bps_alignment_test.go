package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIBPSReferenceEnvelopes(t *testing.T) {
	tools := map[string]bpsTool{}
	var catalog []any
	require.NoError(t, bpsCollectTools([]any{bpsFunction("exec"), map[string]any{"type": "custom", "name": "apply_patch"}}, "functions", tools, &catalog, 0))
	r := &bpsRequest{Tools: tools}
	envelope := `{"tool":"functions.exec","args":{"command":"pwd"}}`
	for name, code := range map[string]any{
		"aliases":          envelope,
		"double JSON":      bpsJSON(envelope),
		"fenced JSON":      "```json\n" + envelope + "\n```",
		"nested transport": bpsJSON(map[string]any{"name": "run_officejs", "arguments": map[string]any{"code": envelope}}),
		"raw newline":      "{\"name\":\"functions.exec\",\"arguments\":{\"command\":\"pwd\nls\"}}",
	} {
		t.Run(name, func(t *testing.T) {
			native := bpsNative("call_envelope", "functions.exec", nil)
			native["arguments"] = bpsJSON(map[string]any{"code": code})
			result, err := bpsConvertTool(native, r)
			require.NoError(t, err)
			require.Equal(t, "exec", result["name"])
			require.Equal(t, "functions", result["namespace"])
			require.NotEmpty(t, result["arguments"])
		})
	}
	for _, code := range []any{
		`{"tool":"functions.exec","name":"other","args":{"command":"pwd"}}`,
		`{"name":"functions.exec","args":{},"arguments":{}}`,
		`{"name":"functions.exec","arguments":{"command":2}}`,
		`{"name":"functions.exec","arguments":{"command":"pwd"}}; alert(1)`,
		`[{"name":"functions.exec","arguments":{"command":"pwd"}}]`,
		`{"name":"not-declared","arguments":{}}`,
		strings.Repeat("x", bpsMaxEnvelopeBytes+1),
	} {
		native := bpsNative("invalid", "functions.exec", nil)
		native["arguments"] = bpsJSON(map[string]any{"code": code})
		_, err := bpsConvertTool(native, r)
		require.Error(t, err)
	}
	patch := "  *** Begin Patch\n" + strings.Repeat("+value \\\"\t\n", 1000) + "*** End Patch\n  "
	native := bpsNative("patch", "functions.apply_patch", nil)
	native["arguments"] = bpsJSON(map[string]any{"summary": "codex2api.custom/functions.apply_patch", "code": patch, "references": []any{}})
	result, err := bpsConvertTool(native, r)
	require.NoError(t, err)
	require.Equal(t, patch, result["input"])
	native["arguments"] = bpsJSON(map[string]any{"code": bpsJSON(map[string]any{"tool": "functions.apply_patch", "args": patch})})
	result, err = bpsConvertTool(native, r)
	require.NoError(t, err)
	require.Equal(t, patch, result["input"])
	native["arguments"] = bpsJSON(map[string]any{"summary": "codex2api.custom/functions.exec", "code": "pwd"})
	_, err = bpsConvertTool(native, r)
	require.Error(t, err)
}

func TestOpenAIBPSAdditionalToolsAndReplay(t *testing.T) {
	s, a := bpsFixture()
	c, _ := bpsContext(1, "/responses")
	source := map[string]any{"model": "gpt-6-astra", "tools": []any{bpsFunction("exec")}, "input": []any{bpsMessage("user", "inspect"), map[string]any{"type": "additional_tools", "tools": []any{bpsFunction("exec"), map[string]any{"type": "namespace", "name": "functions", "tools": []any{map[string]any{"type": "custom", "name": "apply_patch"}}}}}}}
	prepared, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, []byte(bpsJSON(source)))
	require.NoError(t, err)
	require.Len(t, prepared.Tools, 2)
	require.NotContains(t, string(prepared.Body), "additional_tools")
	native := bpsNative("call_custom", "functions.apply_patch", nil)
	native["arguments"] = bpsJSON(map[string]any{"summary": "codex2api.custom/functions.apply_patch", "extended_summary": "preserve this", "references": []any{"native-reference"}, "code": "*** Begin Patch\n*** End Patch"})
	original := bpsJSON(native)
	response, err := s.bpsTransformResponse(c.Request.Context(), a, prepared, bpsResponse(native))
	require.NoError(t, err)
	call := response["output"].([]any)[0]
	input := source["input"].([]any)
	source["input"] = append(input, map[string]any{"type": "reasoning", "id": "rs_original", "summary": []any{"not-replayed"}, "encrypted_content": "opaque-ciphertext"}, call, map[string]any{"type": "custom_tool_call_output", "id": "ctco_client", "call_id": "call_custom", "output": "applied"})
	replay, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, []byte(bpsJSON(source)))
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, bpsDecode(replay.Body, &wire))
	items := wire["input"].([]any)
	reasoning := items[len(items)-3].(map[string]any)
	require.Equal(t, map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": "opaque-ciphertext"}, reasoning)
	require.JSONEq(t, original, bpsJSON(items[len(items)-2]))
	result := items[len(items)-1].(map[string]any)
	require.Equal(t, "function_call_output", result["type"])
	require.Equal(t, "fc_call_custom", result["id"])
	require.Equal(t, "applied", result["output"])
	require.Equal(t, prepared.TurnID, replay.TurnID)
	require.Equal(t, 2, replay.AgentIteration)
	// A conflicting late declaration cannot silently change the schema.
	source["input"] = []any{bpsMessage("user", "inspect"), map[string]any{"type": "additional_tools", "tools": []any{map[string]any{"type": "function", "name": "exec", "parameters": map[string]any{"type": "object"}}}}}
	_, err = s.prepareOpenAIBPS(c.Request.Context(), c, a, []byte(bpsJSON(source)))
	require.ErrorContains(t, err, "Conflicting")
	require.Equal(t, "fc_short", bpsFunctionOutputID("short"))
	long := strings.Repeat("c", 100)
	require.Len(t, bpsFunctionOutputID(long), 64)
	require.Equal(t, "fc_"+bpsDigest(long)[:61], bpsFunctionOutputID(long))
}

func TestOpenAIBPSCompactedTurnLineage(t *testing.T) {
	for _, retainedUser := range []bool{false, true} {
		t.Run(fmt.Sprint(retainedUser), func(t *testing.T) {
			s, a := bpsFixture()
			c, _ := bpsContext(1, "/responses")
			user := bpsMessage("user", "complete pending work")
			source := map[string]any{"model": "gpt-6-astra", "input": []any{user}, "tools": []any{bpsFunction("exec")}}
			prepare := func(ctxPath string, src map[string]any) *bpsRequest {
				t.Helper()
				cx, _ := bpsContext(1, ctxPath)
				r, e := s.prepareOpenAIBPS(cx.Request.Context(), cx, a, []byte(bpsJSON(src)))
				require.NoError(t, e)
				return r
			}
			before := prepare("/responses", source)
			response, err := s.bpsTransformResponse(c.Request.Context(), a, before, bpsResponse(bpsNative("before", "exec", map[string]any{"command": "pwd"})))
			require.NoError(t, err)
			source["input"] = append(source["input"].([]any), response["output"].([]any)[0], map[string]any{"type": "function_call_output", "call_id": "before", "output": "workspace"})
			compactRequest := prepare("/responses/compact", source)
			require.Equal(t, before.TurnID, compactRequest.TurnID)
			require.Equal(t, 2, compactRequest.AgentIteration)
			compactResponse, err := s.bpsTransformResponse(c.Request.Context(), a, compactRequest, bpsResponse(bpsText("The work is pending.")))
			require.NoError(t, err)
			ref := compactResponse["output"].([]any)[0]
			input := []any{ref}
			if retainedUser {
				input = []any{user, ref}
			}
			source["input"] = input
			// A new service uses the same shared store, with no in-process turn counters.
			s = &OpenAIGatewayService{cache: s.cache}
			after := prepare("/responses", source)
			require.Equal(t, before.TurnID, after.TurnID)
			require.Equal(t, 2, after.AgentIteration)
			for i := 0; i < 3; i++ {
				id := fmt.Sprintf("after_%d", i)
				result, e := s.bpsTransformResponse(c.Request.Context(), a, after, bpsResponse(bpsNative(id, "exec", map[string]any{"command": "ls"})))
				require.NoError(t, e)
				source["input"] = append(source["input"].([]any), result["output"].([]any)[0], map[string]any{"type": "function_call_output", "call_id": id, "output": "files"})
				after = prepare("/responses", source)
				require.Equal(t, before.TurnID, after.TurnID)
				require.Equal(t, 3+i, after.AgentIteration)
				require.Equal(t, after.Body, prepare("/responses", source).Body)
			}
			// Consecutive compactions retain the accumulated iteration baseline.
			secondCompact := prepare("/responses/compact", source)
			compactResponse, err = s.bpsTransformResponse(c.Request.Context(), a, secondCompact, bpsResponse(bpsText("Updated summary.")))
			require.NoError(t, err)
			source["input"] = compactResponse["output"]
			after = prepare("/responses", source)
			require.Equal(t, before.TurnID, after.TurnID)
			require.Equal(t, 5, after.AgentIteration)
			source["input"] = append(source["input"].([]any), bpsMessage("user", "new request"))
			next := prepare("/responses", source)
			require.NotEqual(t, before.TurnID, next.TurnID)
			require.Equal(t, 1, next.AgentIteration)
		})
	}
}

func TestOpenAIBPSLegacyCompactStateAndStableRetry(t *testing.T) {
	s, a := bpsFixture()
	c, _ := bpsContext(1, "/responses")
	ref := bpsCompactPrefix + "legacy"
	source := map[string]any{"model": "gpt-6-astra", "input": []any{map[string]any{"type": "compaction", "encrypted_content": ref}}, "tools": []any{bpsFunction("exec")}}
	body := []byte(bpsJSON(source))
	scope := s.bpsScope(c, body)
	require.NoError(t, s.bpsPut(c.Request.Context(), bpsStateKey(scope, a, "compact", ref), map[string]any{"summary": "old record"}))
	first, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, body)
	require.NoError(t, err)
	response, err := s.bpsTransformResponse(c.Request.Context(), a, first, bpsResponse(bpsNative("legacy_call", "exec", map[string]any{"command": "pwd"})))
	require.NoError(t, err)
	source["input"] = append(source["input"].([]any), response["output"].([]any)[0], map[string]any{"type": "function_call_output", "call_id": "legacy_call", "output": "done"})
	second, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, []byte(bpsJSON(source)))
	require.NoError(t, err)
	require.Equal(t, first.TurnID, second.TurnID)
	require.Equal(t, 2, second.AgentIteration)
}

func TestOpenAIBPSNativePlanAliasesAndFailureReplay(t *testing.T) {
	s, a := bpsFixture()
	c, _ := bpsContext(1, "/responses")
	schema := map[string]any{"type": "object", "properties": map[string]any{"plan": map[string]any{"type": "array"}}, "required": []any{"plan"}}
	source := map[string]any{"model": "gpt-6-astra", "input": []any{bpsMessage("user", "plan")}, "tools": []any{map[string]any{"type": "function", "name": "update_plan", "parameters": schema}}}
	prepared, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, []byte(bpsJSON(source)))
	require.NoError(t, err)
	native := map[string]any{"type": "function_call", "id": "fc_plan", "call_id": "plan", "name": "functions.update_plan", "arguments": `{"summary":"next","plan":[{"title":"inspect","status":"active"}]}`}
	response, err := s.bpsTransformResponse(c.Request.Context(), a, prepared, bpsResponse(native))
	require.NoError(t, err)
	call := response["output"].([]any)[0].(map[string]any)
	require.Equal(t, "in_progress", gjson.Get(stringValue(call["arguments"]), "plan.0.status").String())
	for _, output := range []string{"Plan updated", "Plan update failed: rejected"} {
		source["input"] = []any{bpsMessage("user", "plan"), call, map[string]any{"type": "function_call_output", "call_id": "plan", "output": output}}
		replay, e := s.prepareOpenAIBPS(c.Request.Context(), c, a, []byte(bpsJSON(source)))
		require.NoError(t, e)
		items := gjson.GetBytes(replay.Body, "input").Array()
		got := items[len(items)-1].Get("output").String()
		if output == "Plan updated" {
			require.JSONEq(t, `{"status":"ok"}`, got)
		} else {
			require.Equal(t, output, got)
		}
	}
	native["arguments"] = `{"plan":[{"title":"a","step":"b","status":"done"}]}`
	_, err = bpsConvertTool(native, prepared)
	require.Error(t, err)
	native["arguments"] = `{"plan":[{"title":"a","status":"mystery"}]}`
	_, err = bpsConvertTool(native, prepared)
	require.Error(t, err)
}

func TestOpenAIBPSModelPermissionCode(t *testing.T) {
	for _, code := range []string{"basispoints_model_access_changed", "model_not_allowed"} {
		s, a := bpsFixture()
		repo := &bpsFailureRepo{}
		s.accountRepo = repo
		c, rec := bpsContext(1, "/responses")
		a.Credentials["model_mapping"] = map[string]any{"alias": "gpt-6-astra"}
		s.httpUpstream = &bpsHTTPStub{status: http.StatusForbidden, contentType: "application/json", body: bpsJSON(map[string]any{"error": map[string]any{"code": code, "message": "model permission changed"}})}
		_, err := s.Forward(c.Request.Context(), c, a, []byte(`{"model":"alias","input":"hello"}`))
		require.Error(t, err)
		require.Equal(t, "bps_model_not_allowed", gjson.Get(rec.Body.String(), "error.code").String())
		require.Equal(t, []string{"gpt-6-astra"}, repo.limitedModels)
		require.Empty(t, repo.authErrors)
	}
	// JSON numbers must remain exact through envelope decoding and schema checking.
	var args map[string]any
	require.NoError(t, bpsDecode([]byte(`{"n":9007199254740993}`), &args))
	require.Equal(t, json.Number("9007199254740993"), args["n"])
}

func TestOpenAIBPSMissingTerminalToolIsNotSuccessful(t *testing.T) {
	for _, stream := range []bool{false, true} {
		s, a := bpsFixture()
		c, rec := bpsContext(1, "/responses")
		native := bpsNative("lost", "exec", map[string]any{"command": "pwd"})
		s.httpUpstream = &bpsHTTPStub{contentType: "text/event-stream", body: bpsEvent("response.output_item.done", map[string]any{"item": native, "output_index": 0}) + bpsEvent("response.completed", map[string]any{"response": bpsResponse()})}
		body := []byte(bpsJSON(map[string]any{"model": "gpt-6-astra", "input": "inspect", "stream": stream, "tools": []any{bpsFunction("exec")}}))
		result, err := s.Forward(c.Request.Context(), c, a, body)
		require.Error(t, err)
		require.Equal(t, 100, result.Usage.InputTokens)
		require.Contains(t, rec.Body.String(), "bps_invalid_tool_call")
		require.NotContains(t, rec.Body.String(), "event: response.completed")
		require.NotContains(t, rec.Body.String(), "response.function_call_arguments.done")
	}
}

// Exercise the public forwarding service through the whole client-tool loop,
// compaction and a structured final answer, without bypassing routing/binding.
func TestOpenAIBPSForwardToolsCompactStructuredContinuation(t *testing.T) {
	for _, model := range OpenAIBPSDefaultModels() {
		t.Run(model, func(t *testing.T) {
			s, a := bpsFixture()
			tools := []any{bpsFunction("exec"), map[string]any{"type": "custom", "name": "apply_patch"}, map[string]any{"type": "function", "name": "update_plan", "parameters": map[string]any{"type": "object", "properties": map[string]any{"plan": map[string]any{"type": "array"}}, "required": []any{"plan"}}}}
			source := bpsTestStructuredRequest(bpsTestStructuredFormat(true), false)
			source["model"], source["input"], source["tools"] = model, []any{bpsMessage("user", "inspect, plan, patch, and finish")}, tools
			invoke := func(path string, output ...any) (map[string]any, map[string]any) {
				t.Helper()
				up := &bpsHTTPStub{contentType: "application/json", body: bpsJSON(bpsResponse(output...))}
				s.httpUpstream = up
				c, rec := bpsContext(71, path)
				_, err := s.Forward(c.Request.Context(), c, a, []byte(bpsJSON(source)))
				require.NoError(t, err)
				require.Equal(t, 200, rec.Code)
				var response, wire map[string]any
				require.NoError(t, bpsDecode(rec.Body.Bytes(), &response))
				require.NoError(t, bpsDecode(up.requestBody, &wire))
				require.Equal(t, model, wire["model"])
				require.NotContains(t, wire, "tools")
				require.NotContains(t, wire, "text")
				return response, wire
			}
			appendResult := func(response map[string]any, text string) {
				t.Helper()
				output := response["output"].([]any)
				source["input"] = append(source["input"].([]any), output...)
				for _, raw := range output {
					call := raw.(map[string]any)
					kind := stringValue(call["type"])
					if kind == "function_call" || kind == "custom_tool_call" {
						source["input"] = append(source["input"].([]any), map[string]any{"type": kind + "_output", "call_id": call["call_id"], "output": text})
					}
				}
			}
			response, wire := invoke("/responses", map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}, "encrypted_content": "ciphertext"}, bpsNative("inspect", "exec", map[string]any{"command": "pwd"}))
			turn := wire["metadata"].(map[string]any)["turn_id"]
			appendResult(response, "/workspace")
			response, wire = invoke("/responses", map[string]any{"type": "function_call", "id": "fc_plan", "call_id": "plan", "name": "functions.update_plan", "arguments": `{"plan":[{"description":"patch","status":"active"}]}`})
			require.Equal(t, turn, wire["metadata"].(map[string]any)["turn_id"])
			require.Contains(t, bpsJSON(wire), "ciphertext")
			require.Contains(t, bpsJSON(wire), "fc_inspect")
			appendResult(response, "Plan updated")
			native := bpsNative("patch", "apply_patch", nil)
			native["arguments"] = bpsJSON(map[string]any{"summary": "codex2api.custom/apply_patch", "code": "*** Begin Patch\n*** End Patch", "references": []any{}, "extended_summary": "original wrapper"})
			response, wire = invoke("/responses", native)
			require.Equal(t, turn, wire["metadata"].(map[string]any)["turn_id"])
			require.Contains(t, bpsJSON(wire), `status\":\"ok`)
			appendResult(response, "patch applied")
			compact, wire := invoke("/responses/compact", bpsText("Patch applied. Verify and return the final result."))
			require.Equal(t, "4", wire["metadata"].(map[string]any)["agent_iteration"])
			require.Equal(t, turn, wire["metadata"].(map[string]any)["turn_id"])
			source["input"] = compact["output"]
			s = &OpenAIGatewayService{cache: s.cache}
			response, wire = invoke("/responses", bpsNative("verify", "exec", map[string]any{"command": "test -f file"}))
			require.NotContains(t, bpsJSON(wire), bpsCompactPrefix)
			require.Equal(t, "4", wire["metadata"].(map[string]any)["agent_iteration"])
			require.Equal(t, turn, wire["metadata"].(map[string]any)["turn_id"])
			appendResult(response, "verified")
			response, wire = invoke("/responses", bpsText(`{"ok":true}`))
			require.Equal(t, "5", wire["metadata"].(map[string]any)["agent_iteration"])
			require.Equal(t, turn, wire["metadata"].(map[string]any)["turn_id"])
			require.Equal(t, "json_schema", response["text"].(map[string]any)["format"].(map[string]any)["type"])
		})
	}
}
