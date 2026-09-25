package service

import "github.com/google/uuid"

// Turn identity is derived from immutable history. Retried requests never advance
// a mutable Redis counter; compaction retains the identity and iteration baseline.
func bpsTurnState(scope string, input []any, compactIndex int, compactRef string, compact bpsCompactState) (string, int) {
	lastUser := -1
	for i, raw := range input {
		if item, ok := raw.(map[string]any); ok && item["role"] == "user" {
			lastUser = i
		}
	}
	iteration := 1
	start := lastUser + 1
	turnID := ""
	if compactIndex >= 0 && lastUser <= compactIndex {
		turnID = compact.TurnID
		if turnID == "" {
			turnID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(scope+":compact:"+compactRef)).String()
		}
		if compact.AgentIteration > 0 {
			iteration = compact.AgentIteration
		}
		start = compactIndex + 1
	} else {
		prefix := input
		if lastUser >= 0 {
			prefix = input[:lastUser+1]
		} else if len(prefix) > 1 {
			prefix = prefix[:1]
		}
		turnID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(scope+":"+bpsJSON(prefix))).String()
	}
	for _, raw := range input[start:] {
		if item, ok := raw.(map[string]any); ok && (item["type"] == "function_call_output" || item["type"] == "custom_tool_call_output") {
			iteration++
		}
	}
	return turnID, iteration
}

// Matches ghcp_proxy's responses_replay_ids.function_item_id. This ID belongs
// to the replayed result, not to the original upstream call stored in Redis.
func bpsFunctionOutputID(callID string) string {
	candidate := "fc_" + callID
	if len(candidate) <= 64 {
		return candidate
	}
	return "fc_" + bpsDigest(callID)[:61]
}
