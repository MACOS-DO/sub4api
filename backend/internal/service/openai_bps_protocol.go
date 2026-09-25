package service

// BPS wire adaptation is informed by Nonary/ghcp_proxy (Unlicense),
// excel_upstream.py at ad23ce2db3b5212c0355762d981c3877322fb160.
// Credentials, tenant isolation and persistence are owned by Sub4API.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

type bpsTool struct {
	Name, Namespace, Kind string
	Spec                  map[string]any
	Schema                *jsonschema.Schema
}
type bpsRequest struct {
	APIKeyID                     int64
	Body                         []byte
	Source                       map[string]any
	Tools                        map[string]bpsTool
	Scope, Model, Effort         string
	Compact, Stream, RequireTool bool
	TurnID                       string
	AgentIteration               int
	Structured                   *bpsStructuredOutput
}
type bpsNativeCall struct {
	Native map[string]any `json:"native"`
	Client map[string]any `json:"client"`
}
type bpsCompactState struct {
	Summary        string `json:"summary"`
	TurnID         string `json:"turn_id,omitempty"`
	AgentIteration int    `json:"agent_iteration,omitempty"`
}

const bpsCompactPrefix = "bpscmp_v1_"
const bpsToolProtocol = `This request comes from an external Responses client. Its tools run in the client, not in Excel. Use only the client tools listed below. To request one, call the native run_officejs tool with its code field containing a JSON-encoded object: {"name":"catalog tool name","arguments":{...}} for a function. For a custom tool set summary to exactly codex2api.custom/CATALOG_NAME and put its exact raw input in code, without JSON wrapping or Markdown fences. The marker must include the exact namespace-qualified catalog name. Include the namespace in the catalog tool name. Populate the native wrapper fields summary, extended_summary, destructive=false and references=[]. For functions the code field is JSON text. For custom tools it is opaque client input; the proxy never evaluates it as JavaScript or OfficeJS. Request exactly one client tool at a time. Do not call workbook or Office tools. The proxy converts the request and the client executes it. After receiving its real result, continue the task. Do not repeat a successful tool call. Tool catalog: `
const bpsSummaryPrompt = `Summarize the conversation so a successor assistant can continue the user's task after earlier messages are discarded. Preserve the user's requests and constraints, decisions, important paths and code, completed work, actual tool results and errors, pending work and the immediate next step. Incorporate relevant earlier summaries. Return only the summary as plain text. Do not call tools.`

func bpsDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}
func bpsMessage(role, text string) map[string]any {
	return map[string]any{"type": "message", "role": role, "content": []any{map[string]any{"type": "input_text", "text": text}}}
}
func bpsJSON(v any) string { data, _ := json.Marshal(v); return string(data) }

func bpsCollectTools(raw any, namespace string, result map[string]bpsTool, catalog *[]any, depth int) error {
	if depth > 8 {
		return bpsInvalid("Tool namespaces are nested too deeply")
	}
	if raw == nil {
		return nil
	}
	tools, ok := raw.([]any)
	if !ok {
		return bpsInvalid("tools must be an array")
	}
	for _, entry := range tools {
		spec, ok := entry.(map[string]any)
		if !ok {
			return bpsInvalid("Invalid tool declaration")
		}
		name := strings.TrimSpace(stringValue(spec["name"]))
		kind := stringValue(spec["type"])
		if name == "" {
			return bpsInvalid("BPS only supports named function and custom client tools")
		}
		full := name
		if namespace != "" {
			full = namespace + "." + name
		}
		if kind == "namespace" {
			if e := bpsCollectTools(spec["tools"], full, result, catalog, depth+1); e != nil {
				return e
			}
			continue
		}
		if kind != "function" && kind != "custom" {
			return bpsInvalid("BPS only supports function, custom and namespace client tools")
		}
		if previous, exists := result[full]; exists {
			if bpsJSON(previous.Spec) != bpsJSON(spec) {
				return bpsInvalid("Conflicting client tool: " + full)
			}
			continue
		}
		if len(result) >= 256 {
			return bpsInvalid("Too many client tools")
		}
		tool := bpsTool{Name: name, Namespace: namespace, Kind: kind, Spec: spec}
		declaration := map[string]any{"name": full, "type": kind, "description": spec["description"]}
		if kind == "function" {
			schema := spec["parameters"]
			if schema == nil {
				schema = spec["inputSchema"]
			}
			if schema == nil {
				schema = spec["input_schema"]
			}
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			compiler := jsonschema.NewCompiler()
			// Never fetch a client-supplied $ref from the network or local filesystem.
			compiler.LoadURL = func(string) (io.ReadCloser, error) {
				return nil, fmt.Errorf("external schema references are unsupported")
			}
			if e := compiler.AddResource("https://bps.invalid/tool.json", strings.NewReader(bpsJSON(schema))); e != nil {
				return bpsInvalid("Invalid schema for " + full)
			}
			compiled, e := compiler.Compile("https://bps.invalid/tool.json")
			if e != nil {
				return bpsInvalid("Invalid or externally referenced schema for " + full)
			}
			tool.Schema = compiled
			declaration["parameters"] = schema
		} else {
			declaration["format"] = spec["format"]
		}
		result[full] = tool
		*catalog = append(*catalog, declaration)
	}
	return nil
}

func (s *OpenAIGatewayService) prepareOpenAIBPS(ctx context.Context, c *gin.Context, account *Account, body []byte) (*bpsRequest, error) {
	var source map[string]any
	if bpsDecode(body, &source) != nil || source == nil {
		return nil, bpsInvalid("Invalid Responses request")
	}
	if stringValue(source["previous_response_id"]) != "" {
		return nil, bpsInvalid("BPS requires complete input history; previous_response_id is not supported")
	}
	if background, _ := source["background"].(bool); background {
		return nil, bpsInvalid("BPS does not support background responses")
	}
	for _, key := range []string{"service_tier", "temperature", "top_p", "max_output_tokens"} {
		if value, exists := source[key]; exists && value != nil {
			if key == "service_tier" && (value == "auto" || value == "default") {
				continue
			}
			return nil, bpsInvalid("BPS does not support " + key)
		}
	}
	model := strings.TrimSpace(stringValue(source["model"]))
	if model == "" {
		return nil, bpsInvalid("model is required")
	}
	if !account.IsModelSupported(model) {
		return nil, bpsInvalid("Model is not allowed by this BPS account")
	}
	r := &bpsRequest{APIKeyID: getAPIKeyIDFromContext(c), Source: source, Tools: map[string]bpsTool{}, Scope: s.bpsScope(c, body), Model: account.GetMappedModel(model), Effort: "medium"}
	if routing, ok := ctx.Value(bpsRoutingContextKey{}).(bpsRoutingContext); ok {
		r.Scope = routing.Scope
	}
	r.Stream, _ = source["stream"].(bool)
	r.Compact = isOpenAIResponsesCompactPath(c) || HasCompactionTriggerInInput(body)
	if !r.Compact {
		var err error
		r.Structured, err = bpsPrepareStructuredOutput(source["text"])
		if err != nil {
			return nil, err
		}
	}
	rawEffort := source["reasoning_effort"]
	if raw, exists := source["reasoning"]; exists && raw != nil {
		reasoning, ok := raw.(map[string]any)
		if !ok {
			return nil, bpsInvalid("reasoning must be an object")
		}
		if effort, exists := reasoning["effort"]; exists {
			rawEffort = effort
		}
	}
	if rawEffort != nil {
		effort, ok := rawEffort.(string)
		if !ok || effort == "" {
			return nil, bpsInvalid("BPS reasoning effort must be low, medium, high or xhigh")
		}
		r.Effort = effort
	}
	if r.Effort == "x-high" {
		r.Effort = "xhigh"
	}
	switch r.Effort {
	case "low", "medium", "high", "xhigh":
	default:
		return nil, bpsInvalid("BPS reasoning effort must be low, medium, high or xhigh")
	}
	var input []any
	switch v := source["input"].(type) {
	case string:
		input = []any{bpsMessage("user", v)}
	case []any:
		input = v
	default:
		return nil, bpsInvalid("input must be a string or an array")
	}
	var catalog []any
	if !r.Compact && source["tool_choice"] != "none" {
		if e := bpsCollectTools(source["tools"], "", r.Tools, &catalog, 0); e != nil {
			return nil, e
		}
		for _, raw := range input {
			if item, ok := raw.(map[string]any); ok && item["type"] == "additional_tools" {
				if e := bpsCollectTools(item["tools"], "", r.Tools, &catalog, 0); e != nil {
					return nil, e
				}
			}
		}
		if choice, ok := source["tool_choice"].(map[string]any); ok {
			name := stringValue(choice["name"])
			if ns := stringValue(choice["namespace"]); ns != "" {
				name = ns + "." + name
			}
			tool, found := r.Tools[name]
			if !found {
				return nil, bpsInvalid("tool_choice must name a declared client tool")
			}
			r.Tools = map[string]bpsTool{name: tool}
			catalog = nil
			if e := bpsCollectTools([]any{tool.Spec}, tool.Namespace, map[string]bpsTool{}, &catalog, 0); e != nil {
				return nil, e
			}
			r.RequireTool = true
		} else {
			choice := stringValue(source["tool_choice"])
			if choice != "" && choice != "auto" && choice != "required" {
				return nil, bpsInvalid("Unsupported tool_choice")
			}
			r.RequireTool = choice == "required"
		}
	}
	if r.RequireTool && len(r.Tools) == 0 {
		return nil, bpsInvalid("tool_choice requires at least one client tool")
	}
	translated := make([]any, 0, len(input)+3)
	restoredCalls := map[string]bool{}
	compactIndex := -1
	compactRef := ""
	var lastCompact bpsCompactState
	for index, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, bpsInvalid("input items must be objects")
		}
		delete(item, "internal_chat_message_metadata_passthrough")
		kind := stringValue(item["type"])
		switch kind {
		case "function_call", "custom_tool_call":
			id := stringValue(item["call_id"])
			if id == "" {
				return nil, bpsInvalid("BPS tool history requires call_id")
			}
			var saved bpsNativeCall
			if e := s.bpsGet(ctx, bpsStateKey(r.Scope, account, "call", id), &saved); e != nil {
				return nil, e
			}
			translated = append(translated, saved.Native)
			restoredCalls[id] = true
		case "function_call_output", "custom_tool_call_output":
			id := stringValue(item["call_id"])
			if id == "" {
				return nil, bpsInvalid("BPS tool history requires call_id")
			}
			var saved bpsNativeCall
			if e := s.bpsGet(ctx, bpsStateKey(r.Scope, account, "call", id), &saved); e != nil {
				return nil, e
			}
			if !restoredCalls[id] {
				translated = append(translated, saved.Native)
				restoredCalls[id] = true
			}
			output := item["output"]
			if bpsIsNativePlan(stringValue(saved.Native["name"])) && strings.EqualFold(strings.TrimSpace(stringValue(output)), "Plan updated") {
				output = `{"status":"ok"}`
			}
			translated = append(translated, map[string]any{"type": "function_call_output", "id": bpsFunctionOutputID(id), "call_id": id, "output": output})
		case "reasoning":
			if stringValue(item["encrypted_content"]) != "" {
				translated = append(translated, map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": item["encrypted_content"]})
			}
		case "compaction", "compaction_summary":
			ref := stringValue(item["encrypted_content"])
			if ref == "" && r.Compact {
				continue
			}
			if !strings.HasPrefix(ref, bpsCompactPrefix) {
				return nil, bpsInvalid("BPS cannot replay compaction created by another provider")
			}
			var compact bpsCompactState
			if e := s.bpsGet(ctx, bpsStateKey(r.Scope, account, "compact", ref), &compact); e != nil {
				return nil, e
			}
			translated = append(translated, bpsMessage("user", "<conversation_summary>\n"+compact.Summary+"\n</conversation_summary>"))
			compactIndex, compactRef, lastCompact = index, ref, compact
		case "compaction_trigger", "additional_tools":
			continue
		case "item_reference":
			return nil, bpsInvalid("BPS requires full items instead of item_reference")
		default:
			if parts, ok := item["content"].([]any); ok {
				for _, raw := range parts {
					if part, ok := raw.(map[string]any); ok && (part["type"] == "input_image" || part["type"] == "input_audio" || part["type"] == "input_file") {
						return nil, bpsInvalid("BPS currently supports text input only")
					}
				}
			}
			translated = append(translated, item)
		}
	}
	prologue := []any{}
	if instructions := stringValue(source["instructions"]); instructions != "" {
		prologue = append(prologue, bpsMessage("developer", instructions))
	}
	protocol := "This is an external Responses client without an Excel workbook. Return assistant text and do not call server-injected Excel or Office tools."
	if len(r.Tools) > 0 {
		protocol = bpsToolProtocol + bpsJSON(catalog)
		if r.RequireTool {
			protocol += " You must call one of the listed tools in this response."
		}
	}
	if r.Structured != nil {
		protocol += "\n" + r.Structured.instructions()
	}
	prologue = append(prologue, bpsMessage("developer", protocol))
	translated = append(prologue, translated...)
	if r.Compact {
		translated = append(translated, bpsMessage("user", bpsSummaryPrompt))
	}
	r.TurnID, r.AgentIteration = bpsTurnState(r.Scope, input, compactIndex, compactRef, lastCompact)
	metadata := map[string]any{"task_id": uuid.NewSHA1(uuid.NameSpaceURL, []byte(r.Scope)).String(), "turn_id": r.TurnID, "agent_iteration": fmt.Sprint(r.AgentIteration)}
	upstream := map[string]any{"model": r.Model, "model_selection": "explicit", "stream": true, "store": false, "input": translated, "reasoning_effort": r.Effort, "metadata": metadata, "prompt_cache_key": bpsDigest(r.Scope)}
	r.Body, _ = json.Marshal(upstream)
	return r, nil
}

func bpsIsNativePlan(name string) bool {
	return name == "update_plan" || name == "functions.update_plan"
}

func bpsPlanArguments(raw map[string]any) (map[string]any, error) {
	textAlias := func(item map[string]any, keys ...string) (string, error) {
		value := ""
		present := false
		for _, key := range keys {
			if raw, exists := item[key]; exists {
				candidate, ok := raw.(string)
				if !ok || (present && value != candidate) {
					return "", fmt.Errorf("ambiguous plan text fields")
				}
				value, present = candidate, true
			}
		}
		return value, nil
	}
	plan, ok := raw["plan"].([]any)
	if !ok {
		return nil, fmt.Errorf("native update_plan requires a plan array")
	}
	normalized := make([]any, 0, len(plan))
	for _, entry := range plan {
		p, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid native plan entry")
		}
		step, err := textAlias(p, "step", "description", "title")
		if err != nil || strings.TrimSpace(step) == "" {
			return nil, fmt.Errorf("missing or conflicting native plan step")
		}
		status := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(stringValue(p["status"]))))
		switch status {
		case "pending", "not_started", "todo", "planned", "queued", "blocked":
			status = "pending"
		case "in_progress", "inprogress", "running", "active", "started", "doing", "current":
			status = "in_progress"
		case "completed", "done", "complete", "finished":
			status = "completed"
		default:
			return nil, fmt.Errorf("unsupported native plan status")
		}
		normalized = append(normalized, map[string]any{"step": step, "status": status})
	}
	result := map[string]any{"plan": normalized}
	explanation, err := textAlias(raw, "explanation", "summary")
	if err != nil {
		return nil, err
	}
	if explanation != "" {
		result["explanation"] = explanation
	}
	return result, nil
}

func bpsConvertTool(native map[string]any, r *bpsRequest) (map[string]any, error) {
	invalid := func(message string) (map[string]any, error) {
		return nil, &bpsError{502, "bps_invalid_tool_call", message}
	}
	nativeName := stringValue(native["name"])
	name := nativeName
	var args map[string]any
	if value, ok := native["arguments"].(map[string]any); ok {
		args = value
	} else if bpsDecode([]byte(stringValue(native["arguments"])), &args) != nil {
		return invalid("BPS returned malformed tool arguments")
	}
	if args == nil {
		return invalid("BPS returned empty tool arguments")
	}
	var envelope map[string]any
	marked := false
	if nativeName == "run_officejs" || nativeName == "functions.run_officejs" {
		var err error
		envelope, marked, err = bpsCustomTransportEnvelope(args)
		if err == nil && !marked {
			envelope, err = bpsDecodeTransportEnvelope(args["code"])
		}
		if err != nil {
			return invalid(err.Error())
		}
		name, err = bpsEnvelopeName(envelope)
		if err != nil {
			return invalid(err.Error())
		}
	} else if !bpsIsNativePlan(nativeName) {
		return nil, &bpsError{502, "bps_unsupported_tool", "BPS returned an undeclared server tool"}
	}
	tool, ok := r.Tools[name]
	if !ok && bpsIsNativePlan(nativeName) {
		for key, candidate := range r.Tools {
			if candidate.Name == "update_plan" {
				if ok {
					return invalid("Ambiguous update_plan namespace")
				}
				name, tool, ok = key, candidate, true
			}
		}
	}
	if !ok {
		return nil, &bpsError{502, "bps_unsupported_tool", "BPS returned a tool not declared by the client"}
	}
	callID, itemID := stringValue(native["call_id"]), stringValue(native["id"])
	if strings.TrimSpace(callID) == "" || strings.TrimSpace(callID) != callID || itemID == "" {
		return invalid("BPS tool call is missing its native identity")
	}
	if marked && tool.Kind != "custom" {
		return invalid("Raw tool transport requires a declared custom tool")
	}
	result := map[string]any{"type": "function_call", "id": itemID, "call_id": callID, "name": tool.Name, "status": "completed"}
	if tool.Namespace != "" {
		result["namespace"] = tool.Namespace
	}
	if tool.Kind == "custom" {
		value, exists := envelope["input"]
		if alias, hasAlias := envelope["args"]; hasAlias {
			if exists {
				return invalid("BPS custom envelope contains conflicting input fields")
			}
			value = alias
		}
		if _, exists := envelope["arguments"]; exists {
			return invalid("BPS custom tools require raw input, not function arguments")
		}
		input, ok := value.(string)
		if !ok || len(input) > bpsMaxEnvelopeBytes {
			return invalid("BPS custom tool input must be text within 1 MiB")
		}
		result["type"], result["id"], result["input"] = "custom_tool_call", "ctc_"+callID, input
	} else {
		if envelope != nil {
			value, err := bpsEnvelopeArguments(envelope)
			if err != nil {
				return invalid(err.Error())
			}
			if text, ok := value.(string); ok {
				if len(text) > bpsMaxEnvelopeBytes || bpsDecode([]byte(text), &value) != nil {
					return invalid("BPS function arguments must be one JSON object")
				}
			}
			args, _ = value.(map[string]any)
		} else {
			var err error
			args, err = bpsPlanArguments(args)
			if err != nil {
				return invalid(err.Error())
			}
		}
		if args == nil || tool.Schema == nil || tool.Schema.Validate(args) != nil {
			return invalid("BPS tool arguments do not match the client schema")
		}
		result["arguments"] = bpsJSON(args)
	}
	return result, nil
}

func (s *OpenAIGatewayService) bpsTransformResponse(ctx context.Context, account *Account, r *bpsRequest, response map[string]any) (map[string]any, error) {
	output, ok := response["output"].([]any)
	if !ok {
		return nil, &bpsError{502, "bps_invalid_response", "BPS response has no output array"}
	}
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, &bpsError{502, "bps_invalid_response", "BPS returned a malformed output item"}
		}
		switch item["type"] {
		case "message", "reasoning", "function_call", "custom_tool_call":
		default:
			return nil, &bpsError{502, "bps_unsupported_tool", "BPS returned an unsupported output item"}
		}
	}
	if r.Structured != nil {
		if err := r.Structured.validate(response); err != nil {
			return nil, err
		}
		config, _ := response["text"].(map[string]any)
		if config == nil {
			config = map[string]any{}
		}
		config["format"] = r.Structured.Format
		response["text"] = config
		delete(response, "output_text")
	}
	if r.Compact {
		summary := bpsOutputText(response)
		if strings.TrimSpace(summary) == "" {
			return nil, &bpsError{502, "bps_compaction_failed", "BPS did not return a conversation summary"}
		}
		for _, raw := range output {
			if item, ok := raw.(map[string]any); ok && (item["type"] == "function_call" || item["type"] == "custom_tool_call") {
				return nil, &bpsError{502, "bps_compaction_failed", "BPS requested a tool instead of returning a summary"}
			}
		}
		ref := bpsCompactPrefix + uuid.NewString()
		if e := s.bpsPut(ctx, bpsStateKey(r.Scope, account, "compact", ref), bpsCompactState{Summary: summary, TurnID: r.TurnID, AgentIteration: r.AgentIteration}); e != nil {
			return nil, e
		}
		if e := s.bpsPut(ctx, bpsCompactReferenceKey(r.APIKeyID, ref), bpsCompactReference{Scope: r.Scope}); e != nil {
			return nil, e
		}
		response["output"] = []any{map[string]any{"type": "compaction", "id": "cmp_" + uuid.NewString(), "status": "completed", "encrypted_content": ref}}
		delete(response, "output_text")
	} else {
		count := 0
		for _, raw := range output {
			if item, ok := raw.(map[string]any); ok && (item["type"] == "function_call" || item["type"] == "custom_tool_call") {
				count++
			}
		}
		if count > 1 {
			return nil, &bpsError{502, "bps_parallel_tools_unsupported", "BPS must return one client tool at a time"}
		}
		if r.RequireTool && count == 0 {
			return nil, &bpsError{502, "bps_tool_required", "BPS did not return the required client tool"}
		}
		for i, raw := range output {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if item["type"] != "function_call" && item["type"] != "custom_tool_call" {
				continue
			}
			client, e := bpsConvertTool(item, r)
			if e != nil {
				return nil, e
			}
			if e = s.bpsPut(ctx, bpsStateKey(r.Scope, account, "call", stringValue(item["call_id"])), bpsNativeCall{Native: item, Client: client}); e != nil {
				return nil, e
			}
			output[i] = client
		}
		response["output"] = output
	}
	response["model"] = r.Source["model"]
	return response, nil
}
func bpsOutputText(response map[string]any) string {
	var parts []string
	output, _ := response["output"].([]any)
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok || item["type"] != "message" {
			continue
		}
		content, _ := item["content"].([]any)
		for _, v := range content {
			if part, ok := v.(map[string]any); ok && part["type"] == "output_text" {
				parts = append(parts, stringValue(part["text"]))
			}
		}
	}
	return strings.Join(parts, "\n")
}
