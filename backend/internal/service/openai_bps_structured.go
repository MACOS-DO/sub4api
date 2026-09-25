package service

// Adapted from ranxi2001/sub2api at 055a1cd1470b3b866d04aad8d1514bd34cac73ab.
// See docs/openai-bps-sources.md. Validation uses this project's existing schema library.
import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

const bpsStructuredSchemaURL = "https://bps.invalid/structured-output.json"
const bpsMaxStructuredTextBytes = 16 << 20

var bpsStructuredName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type bpsStructuredOutput struct {
	Format map[string]any
	Schema *jsonschema.Schema
}

func bpsPrepareStructuredOutput(raw any) (*bpsStructuredOutput, error) {
	if raw == nil {
		return nil, nil
	}
	config, ok := raw.(map[string]any)
	if !ok {
		return nil, bpsInvalid("text must be an object")
	}
	if config["format"] == nil {
		return nil, nil
	}
	format, ok := config["format"].(map[string]any)
	if !ok {
		return nil, bpsInvalid("text.format must be an object")
	}
	kind := stringValue(format["type"])
	if kind == "text" {
		return nil, nil
	}
	if kind != "json_object" && kind != "json_schema" {
		return nil, bpsInvalid("text.format.type must be text, json_object or json_schema")
	}
	for key := range format {
		if key == "type" || (kind == "json_schema" && (key == "name" || key == "schema" || key == "strict" || key == "description")) {
			continue
		}
		return nil, bpsInvalid("Unsupported text.format field: " + key)
	}
	result := &bpsStructuredOutput{Format: format}
	if kind == "json_object" {
		return result, nil
	}
	if !bpsStructuredName.MatchString(stringValue(format["name"])) {
		return nil, bpsInvalid("json_schema requires a name of 1-64 letters, digits, underscores or hyphens")
	}
	if value := format["strict"]; value != nil {
		if _, ok := value.(bool); !ok {
			return nil, bpsInvalid("json_schema strict must be a boolean")
		}
	}
	if value := format["description"]; value != nil {
		if _, ok := value.(string); !ok {
			return nil, bpsInvalid("json_schema description must be text")
		}
	}
	schema, ok := format["schema"].(map[string]any)
	if !ok || schema == nil {
		return nil, bpsInvalid("json_schema requires a schema object")
	}
	encoded := bpsJSON(schema)
	if len(encoded) > 1<<20 {
		return nil, bpsInvalid("BPS structured output schema exceeds 1 MiB")
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	compiler.AssertFormat = true
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("external schema references are unsupported")
	}
	if err := compiler.AddResource(bpsStructuredSchemaURL, strings.NewReader(encoded)); err != nil {
		return nil, bpsInvalid("Invalid structured output schema")
	}
	compiled, err := compiler.Compile(bpsStructuredSchemaURL)
	if err != nil {
		return nil, bpsInvalid("Invalid structured output schema or external reference")
	}
	result.Schema = compiled
	return result, nil
}

func (s *bpsStructuredOutput) instructions() string {
	instruction := "The client requires a structured final answer. Return exactly one JSON object without Markdown fences or surrounding prose. Tool calls and explicit refusals remain separate protocol items; use declared client tools as needed before the final answer. The proxy validates your final answer before returning it."
	if s.Schema != nil {
		instruction = "The client requires a structured final answer. Return exactly one JSON value without Markdown fences or surrounding prose, satisfying this output format: " + bpsJSON(s.Format) + ". Tool calls and explicit refusals remain separate protocol items; use declared client tools as needed before the final answer. The proxy validates your final answer before returning it."
	}
	return instruction
}
func bpsInvalidStructured(message string) error {
	return &bpsError{502, "bps_invalid_structured_output", message}
}
func (s *bpsStructuredOutput) validate(response map[string]any) error {
	output, _ := response["output"].([]any)
	var answer strings.Builder
	hasTool, hasRefusal := false, false
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		kind := stringValue(item["type"])
		hasTool = hasTool || kind == "function_call" || kind == "custom_tool_call"
		if kind != "message" {
			continue
		}
		content, ok := item["content"].([]any)
		if !ok {
			return bpsInvalidStructured("BPS structured message has invalid content")
		}
		for _, rawPart := range content {
			part, ok := rawPart.(map[string]any)
			if !ok {
				return bpsInvalidStructured("BPS structured message has invalid content")
			}
			switch part["type"] {
			case "output_text":
				value, ok := part["text"].(string)
				if !ok || len(value) > bpsMaxStructuredTextBytes-answer.Len() {
					return bpsInvalidStructured("BPS structured answer is invalid or exceeds 16 MiB")
				}
				answer.WriteString(value)
			case "refusal":
				if _, ok := part["refusal"].(string); !ok {
					return bpsInvalidStructured("BPS refusal must contain text")
				}
				hasRefusal = true
			default:
				return bpsInvalidStructured("BPS structured answer contains unsupported message content")
			}
		}
	}
	if hasTool || (hasRefusal && answer.Len() == 0) {
		return nil
	}
	var value any
	if bpsDecode([]byte(answer.String()), &value) != nil {
		return bpsInvalidStructured("BPS final answer is not one valid JSON value")
	}
	if s.Schema == nil {
		if _, ok := value.(map[string]any); !ok {
			return bpsInvalidStructured("BPS json_object answer must be an object")
		}
	} else if s.Schema.Validate(value) != nil {
		return bpsInvalidStructured("BPS final answer does not satisfy the requested JSON schema")
	}
	return nil
}
func bpsStructuredMessageEvent(kind string, item map[string]any) bool {
	return strings.HasPrefix(kind, "response.output_text.") || strings.HasPrefix(kind, "response.refusal.") || strings.HasPrefix(kind, "response.content_part.") || (strings.HasPrefix(kind, "response.output_item.") && item["type"] == "message")
}
