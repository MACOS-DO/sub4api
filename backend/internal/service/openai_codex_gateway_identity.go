package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

var codexGatewayIdentityAliases = map[string][]string{
	"installation_id":       {"x-codex-installation-id", "installation_id", "installation-id"},
	"session_id":            {"session-id", "session_id", "x-codex-session-id"},
	"thread_id":             {"thread-id", "thread_id", "x-codex-thread-id"},
	"turn_id":               {"x-codex-turn-id", "turn_id", "turn-id"},
	"window_id":             {"x-codex-window-id", "window_id", "window-id"},
	"context_window_id":     {"x-codex-context-window-id", "context_window_id"},
	"parent_thread_id":      {"x-codex-parent-thread-id", "parent_thread_id"},
	"forked_from_thread_id": {"x-codex-forked-from-thread-id", "forked_from_thread_id"},
	"parent_turn_id":        {"x-codex-parent-turn-id", "parent_turn_id"},
	"root_turn_id":          {"x-codex-root-turn-id", "root_turn_id"},
	"prompt_cache_key":      {"prompt_cache_key"},
	"traceparent":           {"traceparent", "ws_request_header_traceparent"},
	"tracestate":            {"tracestate", "ws_request_header_tracestate"},
}

// Only the official identity envelope is inspected. Model input, tool arguments
// and opaque continuation values are never traversed by identity projection.
func codexGatewayIdentityInput(headers http.Header, body []byte) map[string]string {
	values := map[string]string{}
	for field, aliases := range codexGatewayIdentityAliases {
		for _, alias := range aliases {
			if value := strings.TrimSpace(headers.Get(alias)); value != "" {
				values[field] = value
				break
			}
		}
	}
	var collect func(gjson.Result, int)
	collect = func(value gjson.Result, depth int) {
		if depth > 16 {
			return
		}
		if value.Type == gjson.String {
			value = gjson.Parse(value.String())
		}
		if !value.IsObject() {
			return
		}
		object := value.Map()
		for field, aliases := range codexGatewayIdentityAliases {
			if values[field] != "" {
				continue
			}
			for _, alias := range aliases {
				if v := object[alias]; v.Type == gjson.String && strings.TrimSpace(v.String()) != "" {
					values[field] = v.String()
					break
				}
			}
		}
		for _, key := range []string{"client_metadata", "metadata", "x-codex-turn-metadata", "turn_metadata"} {
			if nested, ok := object[key]; ok {
				collect(nested, depth+1)
			}
		}
	}
	if raw := headers.Get("x-codex-turn-metadata"); raw != "" {
		collect(gjson.Parse(raw), 0)
	}
	collect(gjson.ParseBytes(body), 0)
	return values
}

func resolveCodexGatewayIdentity(account *Account, apiKeyID int64, headers http.Header, body []byte) (codexgateway.Identity, error) {
	var output codexgateway.Identity
	if account == nil || account.Gateway == nil || account.Gateway.BindingID == "" {
		return output, errors.New("Gateway account binding is unavailable")
	}
	seed, ok := codexFingerprintSeed(account.Extra)
	if !ok {
		return output, errors.New("Gateway account identity seed is unavailable")
	}
	raw := codexGatewayIdentityInput(headers, body)
	if raw["installation_id"] == "" {
		raw["installation_id"] = strings.TrimSpace(account.getExtraString("openai_device_id"))
		if raw["installation_id"] == "" {
			raw["installation_id"] = deriveStableUUIDv4("sub2api:codex-install-id:v2:" + seed)
		}
	}
	if raw["session_id"] == "" {
		raw["session_id"] = raw["thread_id"]
		if raw["session_id"] == "" {
			raw["session_id"] = raw["prompt_cache_key"]
		}
		if raw["session_id"] == "" {
			raw["session_id"] = uuid.NewString()
		}
	}
	if raw["thread_id"] == "" {
		raw["thread_id"] = raw["session_id"]
	}
	if raw["turn_id"] == "" {
		raw["turn_id"] = uuid.Must(uuid.NewV7()).String()
	}
	scope := func(kind, value string) string {
		if value == "" {
			return ""
		}
		return deriveStableUUIDv4(fmt.Sprintf("sub4api:codex-identity:v1:key:%d:binding:%s:kind:%s:value:%s", apiKeyID, account.Gateway.BindingID, kind, value))
	}
	output = codexgateway.Identity{Version: 1, InstallationID: scope("installation", raw["installation_id"]), SessionID: scope("session", raw["session_id"]), ThreadID: scope("thread", raw["thread_id"]), TurnID: scope("turn", raw["turn_id"]), WindowID: scope("window", raw["window_id"]), ContextWindowID: scope("window", raw["context_window_id"]), ParentThreadID: scope("thread", raw["parent_thread_id"]), ForkedFromThreadID: scope("thread", raw["forked_from_thread_id"]), ParentTurnID: scope("turn", raw["parent_turn_id"]), RootTurnID: scope("turn", raw["root_turn_id"]), PromptCacheKey: scope("prompt-cache", raw["prompt_cache_key"]), Traceparent: raw["traceparent"], Tracestate: raw["tracestate"]}
	if raw["prompt_cache_key"] != "" && raw["prompt_cache_key"] == raw["session_id"] {
		output.PromptCacheKey = output.SessionID
	}
	mode := codexFingerprintModeFromExtra(account.Extra)
	if mode != codexFingerprintOff {
		output.InstallationID = strings.TrimSpace(account.getExtraString("openai_device_id"))
		if output.InstallationID == "" {
			output.InstallationID = deriveStableUUIDv4("sub2api:codex-install-id:v2:" + seed)
		}
	}
	if mode == codexFingerprintSession || mode == codexFingerprintFull {
		output.SessionID = resolveConvergedSessionID(seed)
		output.ThreadID = resolveConvergedThreadID(seed, raw["session_id"])
		if mode == codexFingerprintFull {
			output.ThreadID = output.SessionID
		}
		output.TurnID = uuid.Must(uuid.NewV7()).String()
		output.WindowID = output.ThreadID + ":0"
		if raw["prompt_cache_key"] == raw["session_id"] {
			output.PromptCacheKey = output.SessionID
		}
	}
	if raw["root_turn_id"] != "" && raw["root_turn_id"] == raw["turn_id"] {
		output.RootTurnID = output.TurnID
	}
	if raw["parent_turn_id"] != "" && raw["parent_turn_id"] == raw["turn_id"] {
		output.ParentTurnID = output.TurnID
	}
	if raw["parent_thread_id"] != "" && raw["parent_thread_id"] == raw["thread_id"] {
		output.ParentThreadID = output.ThreadID
	}
	if raw["forked_from_thread_id"] != "" && raw["forked_from_thread_id"] == raw["thread_id"] {
		output.ForkedFromThreadID = output.ThreadID
	}
	return output, nil
}

func codexGatewayWSMessage(body []byte, identity codexgateway.Identity, turnState string) ([]byte, error) {
	var message map[string]json.RawMessage
	if json.Unmarshal(body, &message) != nil {
		return nil, errors.New("invalid WebSocket message")
	}
	delete(message, "codex4server_identity")
	delete(message, "codex4server_transport")
	var kind string
	_ = json.Unmarshal(message["type"], &kind)
	if kind == "response.create" {
		raw, _ := json.Marshal(identity)
		message["codex4server_identity"] = raw
		var metadata map[string]json.RawMessage
		if value := message["client_metadata"]; len(value) > 0 && string(value) != "null" {
			if json.Unmarshal(value, &metadata) != nil {
				return nil, errors.New("invalid client metadata")
			}
		}
		if metadata == nil {
			metadata = map[string]json.RawMessage{}
		}
		for name, value := range map[string]string{"x-codex-installation-id": identity.InstallationID, "session_id": identity.SessionID, "thread_id": identity.ThreadID, "turn_id": identity.TurnID} {
			if value != "" {
				raw, _ := json.Marshal(value)
				metadata[name] = raw
			}
		}
		delete(metadata, "x-codex-turn-state")
		if turnState != "" {
			raw, _ := json.Marshal(turnState)
			metadata["x-codex-turn-state"] = raw
		}
		rawMetadata, _ := json.Marshal(metadata)
		message["client_metadata"] = rawMetadata
	}
	return json.Marshal(message)
}
