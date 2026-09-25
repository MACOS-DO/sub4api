// Adapted from ranxi2001/sub2api at 055a1cd1470b3b866d04aad8d1514bd34cac73ab.
// Protocol provenance and licenses are recorded in docs/openai-bps-sources.md.
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const bpsMaxEnvelopeBytes = 1 << 20

// decodeTransportCode unwraps common model formatting without evaluating code.
// Each layer must contain one complete JSON value; ambiguous batches are rejected.
func bpsDecodeTransportCode(value any) (map[string]any, error) {
	original := value
	for depth := 0; depth < 4; depth++ {
		if item, ok := value.(map[string]any); ok && item != nil {
			if len(bpsJSON(item)) > bpsMaxEnvelopeBytes {
				break
			}
			return item, nil
		}
		raw, ok := value.(string)
		if !ok || len(raw) > bpsMaxEnvelopeBytes {
			break
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			break
		}
		if decoded, ok := bpsDecodeEnvelopeValue(raw); ok {
			value = decoded
			continue
		}
		// Strip a complete Markdown fence, optionally preceded by a short prose label.
		if start := strings.Index(raw, "```"); start >= 0 && bpsProsePrefix(raw[:start]) {
			fenced := raw[start:]
			newline := strings.IndexByte(fenced, '\n')
			if newline >= 0 && strings.HasSuffix(fenced, "```") {
				language := strings.TrimSpace(fenced[3:newline])
				if language == "" || strings.EqualFold(language, "json") {
					value = strings.TrimSpace(fenced[newline+1 : len(fenced)-3])
					continue
				}
			}
		}
		if start := strings.IndexByte(raw, '{'); start > 0 && bpsProsePrefix(raw[:start]) {
			if decoded, ok := bpsDecodeEnvelopeValue(raw[start:]); ok {
				value = decoded
				continue
			}
		}
		break
	}
	return nil, fmt.Errorf("basispoints tool transport code must contain one JSON client-tool envelope; OfficeJS and multiple calls are unsupported (%s)", bpsTransportShape(original))
}

// Report only structural facts, never client code, prompts or tool arguments.
func bpsTransportShape(value any) string {
	raw, ok := value.(string)
	if !ok {
		if value == nil {
			return "format=missing"
		}
		return fmt.Sprintf("format=non_string_%T", value)
	}
	trimmed := strings.TrimSpace(raw)
	format := "text_or_code"
	switch {
	case trimmed == "":
		format = "empty"
	case strings.HasPrefix(trimmed, "```"):
		format = "markdown"
	case strings.HasPrefix(trimmed, "{"):
		format = "json_object"
	case strings.HasPrefix(trimmed, "["):
		format = "json_array"
	case strings.HasPrefix(trimmed, `"`):
		format = "json_string"
	}
	detail := fmt.Sprintf("format=%s; bytes=%d", format, len(raw))
	var syntax *json.SyntaxError
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); errors.As(err, &syntax) {
		detail += fmt.Sprintf("; json_offset=%d; json_failure=%s", syntax.Offset, bpsJsonFailureKind(raw, syntax))
	}
	return detail
}

// Classify the parser's structural context without returning its message, which
// can contain a byte from the caller's code. Offsets use the original wire text.
func bpsJsonFailureKind(raw string, syntax *json.SyntaxError) string {
	if syntax == nil {
		return "unknown"
	}
	message := syntax.Error()
	if syntax.Offset > 0 && syntax.Offset <= int64(len(raw)) && raw[syntax.Offset-1] < 0x20 {
		return "raw_control"
	}
	if syntax.Offset > 1 && raw[syntax.Offset-2] == '\\' {
		return "invalid_escape"
	}
	if syntax.Offset > 5 && raw[syntax.Offset-6] == '\\' && raw[syntax.Offset-5] == 'u' {
		return "invalid_escape"
	}
	switch {
	case strings.Contains(message, "unexpected end of JSON input"):
		return "unexpected_eof"
	case strings.Contains(message, "after top-level value"):
		return "trailing_data"
	case strings.Contains(message, "in string escape code"), strings.Contains(message, "in \\u hexadecimal character escape"):
		return "invalid_escape"
	case strings.Contains(message, "in string literal"):
		if syntax.Offset > 0 && syntax.Offset <= int64(len(raw)) && raw[syntax.Offset-1] < 0x20 {
			return "raw_control"
		}
		return "invalid_string"
	case strings.Contains(message, "after object key"), strings.Contains(message, "after array element"):
		return "missing_separator"
	default:
		return "unexpected_token"
	}
}

func bpsDecodeTransportEnvelope(value any) (map[string]any, error) {
	for depth := 0; depth < 3; depth++ {
		envelope, err := bpsDecodeTransportCode(value)
		if err != nil {
			return nil, err
		}
		name, err := bpsEnvelopeName(envelope)
		if err != nil {
			return nil, err
		}
		if name != "run_officejs" && name != "functions.run_officejs" {
			return envelope, nil
		}
		args, err := bpsEnvelopeArguments(envelope)
		if err != nil {
			return nil, err
		}
		if raw, ok := args.(string); ok {
			var parsed map[string]any
			if bpsDecode([]byte(raw), &parsed) != nil {
				return nil, fmt.Errorf("basispoints nested transport arguments must be one JSON object")
			}
			args = parsed
		}
		outer, ok := args.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("basispoints nested transport arguments must be an object")
		}
		value = outer["code"]
	}
	return nil, fmt.Errorf("basispoints tool transport exceeds two nested wrappers")
}

func bpsEnvelopeName(envelope map[string]any) (string, error) {
	name := stringValue(envelope["name"])
	alias := stringValue(envelope["tool"])
	if name != "" && alias != "" && name != alias {
		return "", fmt.Errorf("basispoints tool envelope contains conflicting names")
	}
	if name == "" {
		name = alias
	}
	return name, nil
}

func bpsEnvelopeArguments(envelope map[string]any) (any, error) {
	args, exists := envelope["arguments"]
	alias, hasAlias := envelope["args"]
	if exists && hasAlias {
		return nil, fmt.Errorf("basispoints tool envelope contains conflicting argument fields")
	}
	if !exists {
		args = alias
	}
	return args, nil
}

func bpsProsePrefix(prefix string) bool {
	return len(prefix) <= 512 && !strings.ContainsAny(prefix, "{}[]();=`\"")
}

func bpsDecodeEnvelopeValue(raw string) (any, bool) {
	var value any
	if bpsDecode([]byte(raw), &value) == nil {
		return value, true
	}
	// Escape raw line breaks and tabs inside strings, preserving those exact
	// characters, and repair illegal backslash escapes only after strict decoding
	// fails. Never infer missing quotes, separators, closing delimiters or values.
	fixed := bpsRepairTransportJSONStrings(raw)
	if fixed != raw && bpsDecode([]byte(fixed), &value) == nil {
		return value, true
	}
	return nil, false
}

func bpsRepairTransportJSONStrings(raw string) string {
	var out strings.Builder
	out.Grow(len(raw))
	quoted := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch == '"' {
			quoted = !quoted
		}
		if quoted {
			switch ch {
			case '\n':
				_, _ = out.WriteString(`\n`)
				continue
			case '\r':
				_, _ = out.WriteString(`\r`)
				continue
			case '\t':
				_, _ = out.WriteString(`\t`)
				continue
			}
		}
		if ch != '\\' || !quoted || i+1 >= len(raw) {
			_ = out.WriteByte(ch)
			continue
		}
		next := raw[i+1]
		valid := strings.ContainsRune(`"\/bfnrt`, rune(next))
		if next == 'u' && i+5 < len(raw) {
			valid = true
			for _, digit := range raw[i+2 : i+6] {
				if !strings.ContainsRune("0123456789abcdefABCDEF", digit) {
					valid = false
				}
			}
		}
		_ = out.WriteByte('\\')
		if valid {
			_ = out.WriteByte(next)
			i++
		} else {
			_ = out.WriteByte('\\')
		}
	}
	return out.String()
}
