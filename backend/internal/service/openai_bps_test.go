package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type bpsMemoryStore struct {
	GatewayCache
	mu   sync.Mutex
	data map[string][]byte
}

func (s *bpsMemoryStore) BPSGet(_ context.Context, k string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data[k]...), nil
}
func (s *bpsMemoryStore) BPSPut(_ context.Context, k string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[k] = append([]byte(nil), v...)
	return nil
}
func (s *bpsMemoryStore) BPSBind(_ context.Context, k string, v []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.data[k]; ok {
		return old, nil
	}
	s.data[k] = append([]byte(nil), v...)
	return v, nil
}
func bpsFixture() (*OpenAIGatewayService, *Account) {
	return &OpenAIGatewayService{cache: &bpsMemoryStore{data: map[string][]byte{}}}, &Account{ID: 7, Platform: PlatformOpenAIBPS, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "test-secret", "chatgpt_account_id": "workspace-1", "model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra", "gpt-5.6-sol": "gpt-5.6-sol"}}}
}
func bpsContext(key int64, path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", path, nil)
	c.Request.Header.Set("session_id", "conversation-1")
	c.Set("api_key", &APIKey{ID: key})
	return c, rec
}
func bpsRequestBody(input any, stream bool) []byte {
	return []byte(bpsJSON(map[string]any{"model": "gpt-6-astra", "input": input, "stream": stream}))
}
func bpsFunction(name string) map[string]any {
	return map[string]any{"type": "function", "name": name, "parameters": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []any{"command"}, "additionalProperties": false}}
}
func bpsNative(id, name string, args any) map[string]any {
	return map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": id, "name": "run_officejs", "arguments": bpsJSON(map[string]any{"code": bpsJSON(map[string]any{"name": name, "arguments": args})}), "status": "completed"}
}
func bpsResponse(output ...any) map[string]any {
	return map[string]any{"id": "resp_test", "object": "response", "status": "completed", "model": "gpt-6-astra", "output": output, "usage": map[string]any{"input_tokens": 100, "output_tokens": 12, "input_tokens_details": map[string]any{"cached_tokens": 35}}}
}
func bpsText(text string) map[string]any {
	return map[string]any{"type": "message", "id": "msg_test", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}
func bpsEvent(typ string, fields map[string]any) string {
	fields["type"] = typ
	return "event: " + typ + "\ndata: " + bpsJSON(fields) + "\n\n"
}

type bpsHTTPStub struct {
	HTTPUpstream
	body        string
	contentType string
	status      int
	request     *http.Request
	requestBody []byte
	requests    int
}

func (u *bpsHTTPStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.requests++
	u.request = req
	u.requestBody, _ = io.ReadAll(req.Body)
	status := u.status
	if status == 0 {
		status = 200
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{u.contentType}}, Body: io.NopCloser(strings.NewReader(u.body))}, nil
}

func TestOpenAIBPSCredentials(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	jwt := "e30." + base64.RawURLEncoding.EncodeToString([]byte(bpsJSON(map[string]any{"exp": exp, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "jwt-account"}}))) + ".sig"
	creds, err := NormalizeOpenAIBPSCredentials(AccountTypeOAuth, map[string]any{"access_token": jwt}, nil)
	require.NoError(t, err)
	require.Equal(t, "jwt-account", creds["chatgpt_account_id"])
	require.Equal(t, time.Unix(exp, 0).UTC().Format(time.RFC3339), creds["expires_at"])
	kept, err := NormalizeOpenAIBPSCredentials(AccountTypeOAuth, map[string]any{"access_token": "", "chatgpt_account_id": "override"}, creds)
	require.NoError(t, err)
	require.Equal(t, jwt, kept["access_token"])
	require.Equal(t, "override", kept["chatgpt_account_id"])
	_, err = NormalizeOpenAIBPSCredentials(AccountTypeAPIKey, map[string]any{"access_token": jwt}, nil)
	require.Error(t, err)
	_, err = NormalizeOpenAIBPSCredentials(AccountTypeOAuth, map[string]any{"access_token": "opaque"}, nil)
	require.Error(t, err)
	_, account := bpsFixture()
	account.Credentials["expires_at"] = time.Now().Add(-time.Second).Format(time.RFC3339)
	require.False(t, account.IsSchedulable())
	_, err = buildOpenAIBPSRequest(context.Background(), account, []byte(`{}`))
	require.Error(t, err)
}
func TestOpenAIBPSProtocolAndHeaders(t *testing.T) {
	s, a := bpsFixture()
	c, _ := bpsContext(1, "/v1/responses")
	c.Request.Header.Set("Authorization", "Bearer hostile")
	c.Request.Header.Set("chatgpt-account-id", "hostile")
	body := []byte(`{"model":"gpt-6-astra","input":"hi","reasoning":{"effort":"xhigh"}}`)
	first, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, body)
	require.NoError(t, err)
	again, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, body)
	require.NoError(t, err)
	require.Equal(t, first.Body, again.Body)
	require.Equal(t, "explicit", gjson.GetBytes(first.Body, "model_selection").String())
	require.False(t, gjson.GetBytes(first.Body, "store").Bool())
	require.Equal(t, "xhigh", gjson.GetBytes(first.Body, "reasoning_effort").String())
	req, err := buildOpenAIBPSRequest(c.Request.Context(), a, first.Body)
	require.NoError(t, err)
	require.Equal(t, OpenAIBPSResponsesURL, req.URL.String())
	require.Equal(t, "Bearer test-secret", req.Header.Get("Authorization"))
	require.Equal(t, "workspace-1", req.Header.Get("chatgpt-account-id"))
	require.Equal(t, "workspace-1", req.Header.Get("x-openai-account-id"))
	require.Equal(t, "chatgpt", req.Header.Get("x-basispoints-auth-mode"))
	for _, bad := range []string{`{"model":"gpt-6-astra","input":"hi","reasoning":{"effort":"max"}}`, `{"model":"gpt-6-astra","input":"hi","previous_response_id":"resp_1"}`, `{"model":"missing","input":"hi"}`, `{"model":"gpt-6-astra","input":[],"tools":[{"type":"web_search"}]}`} {
		_, err = s.prepareOpenAIBPS(c.Request.Context(), c, a, []byte(bad))
		require.Error(t, err, bad)
	}
	require.True(t, a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses))
	require.False(t, a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
}
func TestOpenAIBPSToolsAndStableTurns(t *testing.T) {
	s, a := bpsFixture()
	c, _ := bpsContext(1, "/v1/responses")
	source := map[string]any{"model": "gpt-6-astra", "input": []any{bpsMessage("user", "implement")}, "tools": []any{map[string]any{"type": "namespace", "name": "functions", "tools": []any{bpsFunction("exec")}}}}
	first, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, []byte(bpsJSON(source)))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(first.Body, "tools").Exists())
	native := bpsNative("call_1", "functions.exec", map[string]any{"command": "pwd"})
	original := bpsJSON(native)
	response, err := s.bpsTransformResponse(c.Request.Context(), a, first, bpsResponse(map[string]any{"id": "rs1", "type": "reasoning", "encrypted_content": "ciphertext"}, native))
	require.NoError(t, err)
	output := response["output"].([]any)
	client := output[1].(map[string]any)
	require.Equal(t, "exec", client["name"])
	require.Equal(t, "functions", client["namespace"])
	source["input"] = []any{bpsMessage("user", "implement"), output[0], client, map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "/workspace"}}
	second, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, []byte(bpsJSON(source)))
	require.NoError(t, err)
	require.Equal(t, gjson.GetBytes(first.Body, "metadata.turn_id").String(), gjson.GetBytes(second.Body, "metadata.turn_id").String())
	require.Equal(t, "2", gjson.GetBytes(second.Body, "metadata.agent_iteration").String())
	require.Contains(t, string(second.Body), "ciphertext")
	nativeRestored := gjson.GetBytes(second.Body, "input").Array()[3].Raw
	require.JSONEq(t, original, nativeRestored)
	secondOutput, err := s.bpsTransformResponse(c.Request.Context(), a, second, bpsResponse(bpsNative("call_2", "functions.exec", map[string]any{"command": "ls"})))
	require.NoError(t, err)
	source["input"] = append(source["input"].([]any), secondOutput["output"].([]any)[0], map[string]any{"type": "function_call_output", "call_id": "call_2", "output": "files"})
	third, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, []byte(bpsJSON(source)))
	require.NoError(t, err)
	require.Equal(t, "3", gjson.GetBytes(third.Body, "metadata.agent_iteration").String())
	require.Equal(t, gjson.GetBytes(first.Body, "metadata.turn_id").String(), gjson.GetBytes(third.Body, "metadata.turn_id").String())
	for _, call := range []map[string]any{bpsNative("call_bad", "missing", map[string]any{}), bpsNative("call_bad", "functions.exec", map[string]any{"command": 123}), bpsNative("call_bad", "functions.exec", map[string]any{"command": "pwd", "extra": true})} {
		_, err = bpsConvertTool(call, first)
		require.Error(t, err)
	}
	// The envelope may be JSON encoded twice; trailing code must never be evaluated.
	native["arguments"] = bpsJSON(map[string]any{"code": bpsJSON(bpsJSON(map[string]any{"name": "functions.exec", "arguments": map[string]any{"command": "pwd"}}))})
	_, err = bpsConvertTool(native, first)
	require.NoError(t, err)
	native["arguments"] = bpsJSON(map[string]any{"code": `{"name":"functions.exec","arguments":{"command":"pwd"}}; doSomething()`})
	_, err = bpsConvertTool(native, first)
	require.Error(t, err)
}
func TestOpenAIBPSCustomAndPlan(t *testing.T) {
	tools := map[string]bpsTool{}
	var catalog []any
	plan := map[string]any{"type": "function", "name": "update_plan", "parameters": map[string]any{"type": "object", "required": []any{"plan"}, "properties": map[string]any{"plan": map[string]any{"type": "array", "items": map[string]any{"type": "object", "required": []any{"step", "status"}, "properties": map[string]any{"step": map[string]any{"type": "string"}, "status": map[string]any{"enum": []any{"pending", "in_progress", "completed"}}}}}}}}
	require.NoError(t, bpsCollectTools([]any{plan, map[string]any{"type": "custom", "name": "apply_patch"}}, "", tools, &catalog, 0))
	r := &bpsRequest{Tools: tools}
	native := map[string]any{"type": "function_call", "id": "fc_p", "call_id": "p", "name": "update_plan", "arguments": `{"plan":[{"description":"fix it","status":"done"}]}`}
	client, err := bpsConvertTool(native, r)
	require.NoError(t, err)
	require.JSONEq(t, `{"plan":[{"step":"fix it","status":"completed"}]}`, client["arguments"].(string))
	native["name"] = "run_officejs"
	native["arguments"] = bpsJSON(map[string]any{"code": bpsJSON(map[string]any{"name": "apply_patch", "input": "*** Begin Patch\n*** End Patch"})})
	client, err = bpsConvertTool(native, r)
	require.NoError(t, err)
	require.Equal(t, "custom_tool_call", client["type"])
	require.Equal(t, "ctc_p", client["id"])
	// A client schema can never trigger file or network reads.
	tools = map[string]bpsTool{}
	err = bpsCollectTools([]any{map[string]any{"type": "function", "name": "remote", "parameters": map[string]any{"$ref": "file:///etc/passwd"}}}, "", tools, &catalog, 0)
	require.Error(t, err)
}
func TestOpenAIBPSCompactionAndIsolation(t *testing.T) {
	s, a := bpsFixture()
	c, _ := bpsContext(1, "/v1/responses/compact")
	body := bpsRequestBody("previous history", false)
	require.NoError(t, s.PrepareOpenAIBPSRouting(c, body))
	require.NoError(t, s.bpsBind(c.Request.Context(), s.bpsScope(c, body), a))
	r, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, body)
	require.NoError(t, err)
	require.Empty(t, r.Tools)
	compact, err := s.bpsTransformResponse(c.Request.Context(), a, r, bpsResponse(bpsText("Real summary containing pending work.")))
	require.NoError(t, err)
	ref := compact["output"].([]any)[0]
	require.Contains(t, bpsJSON(ref), bpsCompactPrefix)
	nextBody := []byte(bpsJSON(map[string]any{"model": "gpt-6-astra", "input": []any{ref, bpsMessage("user", "continue")}, "tools": []any{bpsFunction("exec")}}))
	// A new service instance, same shared store, can continue the conversation.
	next := &OpenAIGatewayService{cache: s.cache}
	c2, _ := bpsContext(1, "/v1/responses")
	require.NoError(t, next.PrepareOpenAIBPSRouting(c2, nextBody))
	require.True(t, bpsHasBinding(c2.Request.Context()))
	resumed, err := next.prepareOpenAIBPS(c2.Request.Context(), c2, a, nextBody)
	require.NoError(t, err)
	require.False(t, resumed.Compact)
	require.Contains(t, string(resumed.Body), "Real summary")
	require.NotContains(t, string(resumed.Body), bpsCompactPrefix)
	_, err = next.bpsTransformResponse(c2.Request.Context(), a, resumed, bpsResponse(bpsNative("after_compact", "exec", map[string]any{"command": "pwd"})))
	require.NoError(t, err)
	other := *a
	other.ID++
	require.False(t, bpsBoundAccountAllowed(c2.Request.Context(), &other))
	require.Error(t, next.bpsBind(c2.Request.Context(), r.Scope, &other))
	stranger, _ := bpsContext(2, "/v1/responses")
	require.ErrorContains(t, next.PrepareOpenAIBPSRouting(stranger, nextBody), "expired")
	store := s.cache.(*bpsMemoryStore)
	store.data = map[string][]byte{}
	require.ErrorContains(t, next.PrepareOpenAIBPSRouting(c2, nextBody), "expired")
}
func TestOpenAIBPSForward(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "JSON", true: "SSE"}[stream], func(t *testing.T) {
			s, a := bpsFixture()
			up := &bpsHTTPStub{body: bpsJSON(bpsResponse(bpsText("hello"))), contentType: "application/json"}
			s.httpUpstream = up
			c, rec := bpsContext(1, "/v1/responses")
			body := bpsRequestBody("hi", stream)
			require.NoError(t, s.PrepareOpenAIBPSRouting(c, body))
			result, err := s.Forward(c.Request.Context(), c, a, body)
			require.NoError(t, err)
			require.Equal(t, 200, rec.Code)
			require.Equal(t, 100, result.Usage.InputTokens)
			require.Equal(t, 35, result.Usage.CacheReadInputTokens)
			require.Equal(t, 12, result.Usage.OutputTokens)
			require.Contains(t, rec.Body.String(), "hello")
			if stream {
				require.Contains(t, rec.Body.String(), "event: response.output_text.delta")
				require.Contains(t, rec.Body.String(), "event: response.completed")
			}
		})
	}
	t.Run("tool buffering", func(t *testing.T) {
		s, a := bpsFixture()
		native := bpsNative("call1", "exec", map[string]any{"command": "pwd"})
		up := &bpsHTTPStub{contentType: "text/event-stream", body: bpsEvent("response.output_item.added", map[string]any{"item": native, "output_index": 0}) + bpsEvent("response.function_call_arguments.delta", map[string]any{"delta": "private-office-code"}) + bpsEvent("response.completed", map[string]any{"response": bpsResponse(native)})}
		s.httpUpstream = up
		c, rec := bpsContext(1, "/v1/responses")
		body := []byte(bpsJSON(map[string]any{"model": "gpt-6-astra", "input": "hi", "stream": true, "tools": []any{bpsFunction("exec")}}))
		_, err := s.Forward(c.Request.Context(), c, a, body)
		require.NoError(t, err)
		require.NotContains(t, rec.Body.String(), "run_officejs")
		require.NotContains(t, rec.Body.String(), "private-office-code")
		require.Contains(t, rec.Body.String(), `"name":"exec"`)
		require.Contains(t, rec.Body.String(), "response.function_call_arguments.done")
	})
	t.Run("interrupted", func(t *testing.T) {
		s, a := bpsFixture()
		s.httpUpstream = &bpsHTTPStub{contentType: "text/event-stream", body: bpsEvent("response.created", map[string]any{"response": map[string]any{"id": "resp1", "status": "in_progress"}}) + bpsEvent("response.output_text.delta", map[string]any{"delta": "partial"})}
		c, rec := bpsContext(1, "/v1/responses")
		result, err := s.Forward(c.Request.Context(), c, a, bpsRequestBody("hi", true))
		require.Error(t, err)
		require.Equal(t, "response.failed", result.UpstreamTerminalEvent)
		require.Contains(t, rec.Body.String(), "bps_stream_interrupted")
		require.NotContains(t, rec.Body.String(), "event: response.completed")
	})
	t.Run("unknown tool never completes", func(t *testing.T) {
		s, a := bpsFixture()
		s.httpUpstream = &bpsHTTPStub{contentType: "application/json", body: bpsJSON(bpsResponse(bpsNative("x", "unknown", map[string]any{})))}
		c, rec := bpsContext(1, "/v1/responses")
		_, err := s.Forward(c.Request.Context(), c, a, bpsRequestBody("hi", true))
		require.Error(t, err)
		require.Equal(t, 502, rec.Code)
		require.NotContains(t, rec.Body.String(), "event: response.completed")
	})
	t.Run("native compaction signal", func(t *testing.T) {
		s, a := bpsFixture()
		s.httpUpstream = &bpsHTTPStub{contentType: "application/json", body: bpsJSON(bpsResponse(bpsText("summary")))}
		c, rec := bpsContext(1, "/v1/responses")
		_, err := s.Forward(c.Request.Context(), c, a, bpsRequestBody([]any{bpsMessage("user", "task"), map[string]any{"type": "compaction_trigger"}}, true))
		require.NoError(t, err)
		require.Contains(t, rec.Body.String(), bpsCompactPrefix)
		require.NotContains(t, rec.Body.String(), `"text":"summary"`)
		up := s.httpUpstream.(*bpsHTTPStub)
		require.NotContains(t, string(up.requestBody), "compaction_trigger")
	})
}
func TestOpenAIBPSHTTPFailoverBoundary(t *testing.T) {
	s, a := bpsFixture()
	c, _ := bpsContext(1, "/v1/responses")
	body := bpsRequestBody("hi", false)
	require.NoError(t, s.PrepareOpenAIBPSRouting(c, body))
	s.httpUpstream = &bpsHTTPStub{status: 503, body: `{"error":{"message":"busy"}}`}
	_, err := s.Forward(c.Request.Context(), c, a, body)
	var failover *UpstreamFailoverError
	require.True(t, errors.As(err, &failover))
	require.NoError(t, s.bpsBind(c.Request.Context(), s.bpsScope(c, body), a))
	require.NoError(t, s.PrepareOpenAIBPSRouting(c, body))
	_, err = s.Forward(c.Request.Context(), c, a, body)
	require.False(t, errors.As(err, &failover))
	var failure *bpsError
	require.True(t, errors.As(err, &failure))
	require.Equal(t, 503, failure.Status)
}
func TestOpenAIBPSCatalogAndPlatform(t *testing.T) {
	body, err := buildOpenAIBPSCodexModelsManifest(OpenAIBPSDefaultModels())
	require.NoError(t, err)
	require.Equal(t, 2, len(gjson.GetBytes(body, "models").Array()))
	for _, m := range gjson.GetBytes(body, "models").Array() {
		require.False(t, m.Get("supports_parallel_tool_calls").Bool())
		require.Equal(t, "medium", m.Get("default_reasoning_level").String())
		require.Equal(t, 4, len(m.Get("supported_reasoning_levels").Array()))
		require.NotContains(t, m.Get("supported_reasoning_levels").Raw, "max")
	}
	platform, ok := DetectModelPlatform("gpt-6-astra")
	require.True(t, ok)
	require.Equal(t, PlatformOpenAI, platform)
	require.Equal(t, PlatformOpenAIBPS, NormalizeOpenAICompatiblePlatform(PlatformOpenAIBPS))
	require.True(t, isConcreteRequestPlatform(PlatformOpenAIBPS))
	require.NoError(t, validateProvider(MonitorProviderOpenAIBPS))
	require.NoError(t, validateAPIMode(MonitorProviderOpenAIBPS, MonitorAPIModeResponses))
	require.Error(t, validateCheckMode(MonitorProviderOpenAIBPS, MonitorCheckModeQuota))
}

// Ensure response.failed remains valid JSON even before a response ID is known.
func TestOpenAIBPSErrorJSON(t *testing.T) {
	c, rec := bpsContext(1, "/responses")
	WriteOpenAIBPSError(c, bpsExpired())
	require.Equal(t, 409, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
}

func TestOpenAIBPSCredentialReplacementPreservesDisabledState(t *testing.T) {
	for _, status := range []string{StatusDisabled, "inactive", StatusActive, StatusError} {
		t.Run(status, func(t *testing.T) {
			_, account := bpsFixture()
			account.Status = status
			account.Schedulable = false
			account.ErrorMessage = "BPS authentication failed; replace access_token"
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
			admin := &adminServiceImpl{accountRepo: repo}
			updated, err := admin.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Credentials: map[string]any{"access_token": "replacement"}})
			require.NoError(t, err)
			require.False(t, updated.Schedulable)
			require.Equal(t, "replacement", updated.GetCredential("access_token"))
			if status == StatusError {
				require.Equal(t, StatusActive, updated.Status)
				require.Empty(t, updated.ErrorMessage)
			} else {
				require.Equal(t, status, updated.Status)
			}
		})
	}
}
func TestOpenAIBPSCompositeRequiresExplicitRule(t *testing.T) {
	_, account := bpsFixture()
	gateway := &GatewayService{accountRepo: &compositeOwnershipAccountRepo{accounts: []Account{*account}}}
	resolver := NewCompositeRouteResolver(nil)
	resolver.SetModelOwnershipResolver(gateway.resolveCompositeModelOwnership)
	decision, err := resolver.Resolve(context.Background(), 1, "gpt-6-astra", CompositeRouteEndpointResponses)
	require.NoError(t, err)
	require.Equal(t, PlatformOpenAI, decision.TargetPlatform)
	platform, _, ok := resolveCodexCompositeModelTarget("gpt-6-astra", []Account{*account}, nil, true)
	require.True(t, ok)
	require.Equal(t, PlatformOpenAI, platform)
	routes := []CompositeModelRoute{{PublicModel: "bps", UpstreamModel: "gpt-6-astra", TargetPlatform: PlatformOpenAIBPS, Endpoint: CompositeRouteEndpointResponses, MatchType: CompositeRouteMatchExact}}
	body, err := buildCodexModelsManifestForAccounts(PlatformComposite, []string{"bps"}, []Account{*account}, &Group{Platform: PlatformComposite}, routes, true)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "models.0.supports_parallel_tool_calls").Bool())
	require.Equal(t, "medium", gjson.GetBytes(body, "models.0.default_reasoning_level").String())
}

func TestOpenAIBPSCreateValidatesCredentials(t *testing.T) {
	_, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAIBPS, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "opaque"}}, nil)
	require.Error(t, err)
	account, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAIBPS, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "opaque", "chatgpt_account_id": "workspace"}}, nil)
	require.NoError(t, err)
	require.Len(t, account.GetModelMapping(), 2)
	_, err = buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAIBPS, Type: AccountTypeAPIKey, Credentials: map[string]any{"access_token": "opaque", "chatgpt_account_id": "workspace"}}, nil)
	require.Error(t, err)
}

type bpsFailureRepo struct {
	AccountRepository
	authErrors    []string
	limitedModels []string
	cooldown      time.Time
}

func (r *bpsFailureRepo) SetError(_ context.Context, _ int64, message string) error {
	r.authErrors = append(r.authErrors, message)
	return nil
}
func (r *bpsFailureRepo) SetModelRateLimit(_ context.Context, _ int64, model string, _ time.Time, _ ...string) error {
	r.limitedModels = append(r.limitedModels, model)
	return nil
}
func (r *bpsFailureRepo) SetRateLimited(_ context.Context, _ int64, until time.Time) error {
	r.cooldown = until
	return nil
}
func TestOpenAIBPSHTTPErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		status      int
		body        string
		auth, model bool
	}{{401, `{"error":{"message":"invalid test-secret"}}`, true, false}, {403, `{"error":{"code":"model_not_allowed"}}`, false, true}, {400, `{"error":{"message":"bad parameter"}}`, false, false}, {429, `{"error":{"message":"limited"}}`, false, false}} {
		s, a := bpsFixture()
		repo := &bpsFailureRepo{}
		s.accountRepo = repo
		c, rec := bpsContext(1, "/responses")
		response := &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{"Retry-After": []string{"120"}}}
		err := s.bpsHTTPError(c.Request.Context(), c, a, response, "gpt-6-astra")
		require.Error(t, err)
		require.Equal(t, tc.auth, len(repo.authErrors) > 0)
		require.Equal(t, tc.model, len(repo.limitedModels) > 0)
		require.NotContains(t, err.Error(), "test-secret")
		if tc.status == 429 {
			require.InDelta(t, 120, time.Until(repo.cooldown).Seconds(), 2)
			require.Equal(t, "120", rec.Header().Get("Retry-After"))
		}
	}
}
func TestOpenAIBPSTextContinuationRequiresSession(t *testing.T) {
	s, _ := bpsFixture()
	c, _ := bpsContext(1, "/responses")
	body := bpsRequestBody([]any{bpsMessage("user", "hello"), bpsText("hello back"), bpsMessage("user", "continue")}, false)
	require.ErrorContains(t, s.PrepareOpenAIBPSRouting(c, body), "expired")
}
func TestOpenAIBPSSchedulerBinding(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	for _, advanced := range []bool{false, true} {
		_, a := bpsFixture()
		a.Concurrency = 1
		a.GroupIDs = []int64{91}
		other := *a
		other.ID = 8
		other.Priority = -10
		svc := newOpenAICompactionSchedulerTestService([]Account{other, *a}, advanced)
		ctx := context.WithValue(context.Background(), bpsRoutingContextKey{}, bpsRoutingContext{Scope: "test", Binding: &bpsSessionBinding{a.ID, a.GetCredential("chatgpt_account_id")}})
		group := int64(91)
		selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "", "gpt-6-astra", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, true, false, false, PlatformOpenAIBPS)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.Equal(t, a.ID, selection.Account.ID)
	}
}

func TestOpenAIBPSImplicitSessionSurvivesModelAndToolChanges(t *testing.T) {
	s, a := bpsFixture()
	c, _ := bpsContext(1, "/responses")
	c.Request.Header.Del("session_id")
	original := []byte(`{"model":"gpt-6-astra","input":"task"}`)
	require.NoError(t, s.PrepareOpenAIBPSRouting(c, original))
	first, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, original)
	require.NoError(t, err)
	secondContext, _ := bpsContext(1, "/responses")
	secondContext.Request.Header.Del("session_id")
	changed := []byte(bpsJSON(map[string]any{"model": "gpt-5.6-sol", "instructions": "new rules", "tools": []any{bpsFunction("exec")}, "input": []any{bpsMessage("user", "task"), bpsMessage("user", "next")}}))
	second, err := s.prepareOpenAIBPS(secondContext.Request.Context(), secondContext, a, changed)
	require.NoError(t, err)
	require.Equal(t, first.Scope, second.Scope)
	require.Equal(t, gjson.GetBytes(first.Body, "metadata.task_id").String(), gjson.GetBytes(second.Body, "metadata.task_id").String())
	require.NotEqual(t, gjson.GetBytes(first.Body, "metadata.turn_id").String(), gjson.GetBytes(second.Body, "metadata.turn_id").String())
	// Explicit IDs and derived conversation roots occupy different namespaces.
	secondContext.Request.Header.Set("session_id", "root:task")
	require.NotEqual(t, first.Scope, s.bpsScope(secondContext, changed))
}

func TestOpenAIBPSCompactionWithoutSessionHeader(t *testing.T) {
	s, a := bpsFixture()
	c, _ := bpsContext(9, "/responses/compact")
	c.Request.Header.Del("session_id")
	body := bpsRequestBody("original task", false)
	require.NoError(t, s.PrepareOpenAIBPSRouting(c, body))
	r, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, body)
	require.NoError(t, err)
	require.NoError(t, s.bpsBind(c.Request.Context(), r.Scope, a))
	response, err := s.bpsTransformResponse(c.Request.Context(), a, r, bpsResponse(bpsText("real summary")))
	require.NoError(t, err)
	nextBody := bpsRequestBody(append(response["output"].([]any), bpsMessage("user", "continue")), false)
	next, _ := bpsContext(9, "/responses")
	next.Request.Header.Del("session_id")
	fresh := &OpenAIGatewayService{cache: s.cache}
	require.NoError(t, fresh.PrepareOpenAIBPSRouting(next, nextBody))
	continued, err := fresh.prepareOpenAIBPS(next.Request.Context(), next, a, nextBody)
	require.NoError(t, err)
	require.Equal(t, r.Scope, continued.Scope)
	require.Contains(t, string(continued.Body), "real summary")
	require.NotContains(t, string(continued.Body), bpsCompactPrefix)
	stranger, _ := bpsContext(10, "/responses")
	stranger.Request.Header.Del("session_id")
	require.ErrorContains(t, fresh.PrepareOpenAIBPSRouting(stranger, nextBody), "expired")
}

func TestOpenAIBPSRefusesUnknownNativeItemsAndEvents(t *testing.T) {
	for _, stream := range []bool{false, true} {
		s, a := bpsFixture()
		s.httpUpstream = &bpsHTTPStub{contentType: "application/json", body: bpsJSON(bpsResponse(map[string]any{"type": "web_search_call", "id": "ws1", "status": "completed"}))}
		c, rec := bpsContext(1, "/responses")
		_, err := s.Forward(c.Request.Context(), c, a, bpsRequestBody("task", stream))
		require.Error(t, err)
		require.NotContains(t, rec.Body.String(), "web_search_call")
		require.NotContains(t, rec.Body.String(), "event: response.completed")
	}
	s, a := bpsFixture()
	s.httpUpstream = &bpsHTTPStub{contentType: "text/event-stream", body: bpsEvent("response.web_search_call.in_progress", map[string]any{"item_id": "ws1"})}
	c, rec := bpsContext(1, "/responses")
	_, err := s.Forward(c.Request.Context(), c, a, bpsRequestBody("task", true))
	require.Error(t, err)
	require.Contains(t, rec.Body.String(), "bps_unsupported_event")
}

func TestOpenAIBPSFailedJSONPreservesUsage(t *testing.T) {
	s, a := bpsFixture()
	response := bpsResponse(bpsText("partial"))
	response["status"] = "incomplete"
	s.httpUpstream = &bpsHTTPStub{contentType: "application/json", body: bpsJSON(response)}
	c, rec := bpsContext(1, "/responses")
	result, err := s.Forward(c.Request.Context(), c, a, bpsRequestBody("task", false))
	require.Error(t, err)
	require.Equal(t, 100, result.Usage.InputTokens)
	require.Equal(t, 12, result.Usage.OutputTokens)
	require.Equal(t, 502, rec.Code)
	require.False(t, result.SucceededForScheduling())
}

func TestOpenAIBPSStrictReasoningAndAuthRecovery(t *testing.T) {
	s, a := bpsFixture()
	c, _ := bpsContext(1, "/responses")
	for _, effort := range []any{false, 7, "max", ""} {
		body := []byte(bpsJSON(map[string]any{"model": "gpt-6-astra", "input": "task", "reasoning": map[string]any{"effort": effort}}))
		_, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, body)
		require.Error(t, err)
	}
	a.Status = StatusError
	a.ErrorMessage = "BPS authentication failed; replace access_token"
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{a.ID: a}}
	admin := &adminServiceImpl{accountRepo: repo}
	updated, err := admin.UpdateAccount(context.Background(), a.ID, &UpdateAccountInput{Status: StatusError, Credentials: map[string]any{"access_token": "new-token"}})
	require.NoError(t, err)
	require.Equal(t, StatusActive, updated.Status)
	require.Empty(t, updated.ErrorMessage)
}

func TestOpenAIBPSPlanResultRestoresNativeCall(t *testing.T) {
	s, a := bpsFixture()
	c, _ := bpsContext(1, "/responses")
	body := []byte(`{"model":"gpt-6-astra","input":"make a plan","tools":[{"type":"function","name":"update_plan","parameters":{"type":"object"}}]}`)
	require.NoError(t, s.PrepareOpenAIBPSRouting(c, body))
	r, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, body)
	require.NoError(t, err)
	native := map[string]any{"type": "function_call", "id": "native-plan-item", "call_id": "plan-call", "name": "update_plan", "arguments": `{"summary":"next","plan":[{"id":"s1","description":"code","status":"in_progress","result":""}]}`}
	original := bpsJSON(native)
	_, err = s.bpsTransformResponse(c.Request.Context(), a, r, bpsResponse(native))
	require.NoError(t, err)
	// The client may send just the result alongside full user history; replay
	// the complete native call from Redis, never synthesize a different identity.
	nextBody := bpsRequestBody([]any{bpsMessage("user", "make a plan"), map[string]any{"type": "function_call_output", "call_id": "plan-call", "output": "Plan updated"}}, false)
	next, err := s.prepareOpenAIBPS(c.Request.Context(), c, a, nextBody)
	require.NoError(t, err)
	items := gjson.GetBytes(next.Body, "input").Array()
	require.JSONEq(t, original, items[2].Raw)
	require.Equal(t, `{"status":"ok"}`, items[3].Get("output").String())
}

func TestOpenAIBPSNativeSSETextIsNotDuplicated(t *testing.T) {
	s, a := bpsFixture()
	item := bpsText("hello")
	stream := bpsEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_test", "status": "in_progress"}}) + bpsEvent("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_test", "type": "message", "content": []any{}}}) + bpsEvent("response.output_text.delta", map[string]any{"output_index": 0, "item_id": "msg_test", "content_index": 0, "delta": "hello"}) + bpsEvent("response.output_item.done", map[string]any{"output_index": 0, "item": item}) + bpsEvent("response.completed", map[string]any{"response": bpsResponse(item)})
	s.httpUpstream = &bpsHTTPStub{contentType: "text/event-stream", body: stream}
	c, rec := bpsContext(1, "/responses")
	_, err := s.Forward(c.Request.Context(), c, a, bpsRequestBody("task", true))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.output_text.delta"))
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.output_item.added"))
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.output_item.done"))
}

func TestOpenAIBPSConnectivityUsesBPSForBothModels(t *testing.T) {
	for _, model := range OpenAIBPSDefaultModels() {
		for _, mode := range []string{"", "compact"} {
			gateway, account := bpsFixture()
			upstream := &bpsHTTPStub{contentType: "application/json", body: bpsJSON(bpsResponse(bpsText("OK")))}
			gateway.httpUpstream = upstream
			tester := &AccountTestService{openaiGatewayService: gateway, accountRepo: &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}}
			c, rec := bpsContext(1, "/admin/test")
			err := tester.TestAccountConnection(c, account.ID, model, "", mode)
			require.NoError(t, err)
			require.Equal(t, OpenAIBPSResponsesURL, upstream.request.URL.String())
			require.Equal(t, model, gjson.GetBytes(upstream.requestBody, "model").String())
			require.Contains(t, rec.Body.String(), `"success":true`)
		}
	}
}
