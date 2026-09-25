// Adapted from ranxi2001/sub2api at 055a1cd1470b3b866d04aad8d1514bd34cac73ab.
// Protocol provenance and licenses are recorded in docs/openai-bps-sources.md.
package service

import (
	"fmt"
	"strings"
	"unicode"
)

const bpsCustomTransportPrefix = "codex2api.custom/"

// customTransportEnvelope recognizes only the explicitly tagged raw custom-tool
// transport. The caller must still check the exact catalog name, custom kind and
// call identity. Ordinary run_officejs calls continue through the JSON decoder.
func bpsCustomTransportEnvelope(arguments map[string]any) (map[string]any, bool, error) {
	summary, ok := arguments["summary"].(string)
	if !ok || !strings.HasPrefix(summary, bpsCustomTransportPrefix) {
		return nil, false, nil
	}
	name := strings.TrimPrefix(summary, bpsCustomTransportPrefix)
	if name == "" || strings.ContainsAny(name, "/\\") || strings.IndexFunc(name, unicode.IsSpace) >= 0 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return nil, true, fmt.Errorf("basispoints raw custom transport requires an exact nonempty catalog tool name in summary")
	}
	input, ok := arguments["code"].(string)
	if !ok {
		return nil, true, fmt.Errorf("basispoints raw custom transport code must be a string")
	}
	if len(input) > bpsMaxEnvelopeBytes {
		return nil, true, fmt.Errorf("basispoints raw custom transport code exceeds the size limit")
	}
	return map[string]any{"name": name, "input": input}, true, nil
}
