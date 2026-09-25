package service

import "encoding/json"

func buildOpenAIBPSCodexModelsManifest(modelIDs []string) ([]byte, error) {
	body, err := BuildCodexModelsManifest(modelIDs)
	if err != nil {
		return nil, err
	}
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if err = json.Unmarshal(body, &catalog); err != nil {
		return nil, err
	}
	for _, model := range catalog.Models {
		applyOpenAIBPSModelCapabilities(model)
	}
	return json.Marshal(catalog)
}

func applyOpenAIBPSModelCapabilities(model map[string]any) {
	model["default_reasoning_level"] = "medium"
	model["multi_agent_reasoning_effort"] = "medium"
	model["supported_reasoning_levels"] = []configuredCodexReasoningLevel{{"low", "Low"}, {"medium", "Medium"}, {"high", "High"}, {"xhigh", "Extra high"}}
	model["supports_parallel_tool_calls"] = false
	model["prefer_websockets"] = false
	model["supports_reasoning_summary_parameter"] = false
	model["support_verbosity"] = false
	model["input_modalities"] = []string{"text"}
	model["supports_image_detail_original"] = false
	model["supports_search_tool"] = false
	model["additional_speed_tiers"] = []string{}
	model["service_tiers"] = []any{}
	model["default_service_tier"] = nil
	// Conservative local compaction policy, not a claim about upstream limits.
	model["context_window"] = 200000
	model["max_context_window"] = 200000
	model["auto_compact_token_limit"] = 160000
}
