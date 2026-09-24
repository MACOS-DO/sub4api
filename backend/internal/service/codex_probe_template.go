package service

import (
	"context"
	_ "embed"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const CodexProbeTemplateMaxBytes = 256 << 10

//go:embed codex_probe_template.jsonl
var defaultCodexProbeTemplateJSONL string

var ErrCodexProbeTemplateInvalid = errors.New("invalid Codex probe template")
var ErrCodexProbeIdentity = errors.New("Codex probe identity unavailable")

// CodexProbeTemplate is an immutable, validated initial conversation. It can be
// shared by concurrent requests; rendering never changes its stored strings.
type CodexProbeTemplate struct {
	replay CodexHypothesisReplay
}

type codexProbeText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexProbeMessage struct {
	Type    string           `json:"type"`
	Role    string           `json:"role"`
	Content []codexProbeText `json:"content"`
}

func DefaultCodexProbeTemplate() string { return defaultCodexProbeTemplateJSONL }

func EffectiveCodexProbeTemplate(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return DefaultCodexProbeTemplate()
	}
	return raw
}

func decodeCodexProbeJSON(raw string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

var codexProbePlaceholder = regexp.MustCompile(`\{\{[^{}]*\}\}`)

func validateCodexProbePlaceholders(text string) bool {
	valid := true
	rest := codexProbePlaceholder.ReplaceAllStringFunc(text, func(value string) string {
		switch value {
		case "{{TIMEZONE}}", "{{CURRENT_DATE}}", "{{MODEL}}", CodexHypothesisPromptPlaceholder:
		default:
			valid = false
		}
		return ""
	})
	return valid && !strings.Contains(rest, "{{") && !strings.Contains(rest, "}}")
}

// Only environment_context is XML. Other wrappers, notably <permissions
// instructions>, are literal prompt delimiters and must not be normalized.
func codexProbeEnvironmentShape(text string) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(text))
	var shape strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return shape.String(), nil
		}
		if err != nil {
			return "", err
		}
		switch token := token.(type) {
		case xml.StartElement:
			attrs := make([]string, 0, len(token.Attr))
			for _, attr := range token.Attr {
				attrs = append(attrs, fmt.Sprintf("%v=%q", attr.Name, attr.Value))
			}
			sort.Strings(attrs)
			fmt.Fprintf(&shape, "+%v%v;", token.Name, attrs)
		case xml.EndElement:
			fmt.Fprintf(&shape, "-%v;", token.Name)
		case xml.Directive, xml.ProcInst:
			return "", errors.New("unexpected XML directive")
		}
	}
}

var defaultCodexProbeEnvironmentShape = sync.OnceValue(func() string {
	var record struct {
		Payload codexProbeMessage `json:"payload"`
	}
	lines := strings.Split(DefaultCodexProbeTemplate(), "\n")
	if err := json.Unmarshal([]byte(lines[4]), &record); err != nil {
		panic("invalid embedded Codex probe environment")
	}
	shape, err := codexProbeEnvironmentShape(record.Payload.Content[0].Text)
	if err != nil {
		panic("invalid embedded Codex probe environment XML")
	}
	return shape
})

// ParseCodexProbeTemplate accepts the initial-context format only, never a raw
// recording's identifiers, reasoning, assistant outputs or tool events. Errors
// name the line and rule, but do not echo any submitted prompt text.
func ParseCodexProbeTemplate(raw string) (*CodexProbeTemplate, error) {
	if len(raw) > CodexProbeTemplateMaxBytes {
		return nil, fmt.Errorf("%w: line 1: maximum size is 256 KiB", ErrCodexProbeTemplateInvalid)
	}
	raw = EffectiveCodexProbeTemplate(raw)
	template := &CodexProbeTemplate{}
	types := []string{"session_meta", "response_item", "response_item", "response_item", "response_item", "turn_context", "response_item"}
	roles := map[int]string{1: "developer", 2: "developer", 3: "developer", 4: "user", 6: "user"}
	parts := map[int]int{1: 5, 2: 1, 3: 1, 4: 1, 6: 1}
	index, placeholders := 0, 0
	lastLine := 1
	for line, rawLine := range strings.Split(raw, "\n") {
		lastLine = line + 1
		if strings.TrimSpace(rawLine) == "" {
			continue
		}
		fail := func(reason string) (*CodexProbeTemplate, error) {
			return nil, fmt.Errorf("%w: line %d: %s", ErrCodexProbeTemplateInvalid, line+1, reason)
		}
		var record struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if decodeCodexProbeJSON(rawLine, &record) != nil {
			return fail("expected a JSON object containing only type and payload")
		}
		if index >= len(types) || record.Type != types[index] {
			return fail("unexpected record type or initial-context order")
		}
		switch record.Type {
		case "session_meta":
			var payload struct {
				Originator string `json:"originator"`
				Base       struct {
					Text string `json:"text"`
				} `json:"base_instructions"`
			}
			if decodeCodexProbeJSON(string(record.Payload), &payload) != nil || payload.Originator != "Codex Desktop" || strings.TrimSpace(payload.Base.Text) == "" {
				return fail("expected Codex Desktop originator and nonempty base_instructions.text")
			}
			if !validateCodexProbePlaceholders(payload.Base.Text) {
				return fail("unknown or malformed placeholder")
			}
			placeholders += strings.Count(payload.Base.Text, CodexHypothesisPromptPlaceholder)
			template.replay.Instructions = payload.Base.Text
		case "turn_context":
			var payload struct {
				Date     string `json:"current_date"`
				Timezone string `json:"timezone"`
				Model    string `json:"model"`
			}
			if decodeCodexProbeJSON(string(record.Payload), &payload) != nil || payload.Date != "{{CURRENT_DATE}}" || payload.Timezone != "{{TIMEZONE}}" || payload.Model != "{{MODEL}}" {
				return fail("turn_context requires CURRENT_DATE, TIMEZONE and MODEL placeholders")
			}
		case "response_item":
			var payload codexProbeMessage
			if decodeCodexProbeJSON(string(record.Payload), &payload) != nil || payload.Type != "message" || payload.Role != roles[index] || len(payload.Content) != parts[index] {
				return fail("retain the initial developer/user message roles and content parts")
			}
			message := CodexHypothesisReplayMessage{Role: payload.Role}
			for partIndex, part := range payload.Content {
				if part.Type != "input_text" || strings.TrimSpace(part.Text) == "" {
					return fail("each content part must be nonempty input_text")
				}
				if !validateCodexProbePlaceholders(part.Text) {
					return fail("unknown or malformed placeholder")
				}
				placeholders += strings.Count(part.Text, CodexHypothesisPromptPlaceholder)
				var tag string
				switch index {
				case 1:
					tag = []string{"app-context", "", "skills_instructions", "permissions instructions", "collaboration_mode"}[partIndex]
					if partIndex == 1 && !strings.HasPrefix(part.Text, "## Memory\n") {
						return fail("retain the Memory heading")
					}
				case 2:
					tag = "multi_agent_role"
				case 3:
					tag = "multi_agent_mode"
				case 4:
					tag = "environment_context"
					shape, err := codexProbeEnvironmentShape(part.Text)
					var env struct {
						Date     string `xml:"current_date"`
						Timezone string `xml:"timezone"`
					}
					if err != nil || shape != defaultCodexProbeEnvironmentShape() || xml.Unmarshal([]byte(part.Text), &env) != nil {
						return fail("retain the environment XML hierarchy and attributes")
					}
					if strings.TrimSpace(env.Date) != "{{CURRENT_DATE}}" || strings.TrimSpace(env.Timezone) != "{{TIMEZONE}}" {
						return fail("environment date and timezone must use their placeholders")
					}
				case 6:
					if part.Text != CodexHypothesisPromptPlaceholder {
						return fail("final user message must contain only the challenge placeholder")
					}
				}
				if tag != "" && (!strings.HasPrefix(strings.TrimSpace(part.Text), "<"+tag+">") || strings.Count(part.Text, "<"+tag+">") != 1 || strings.Count(part.Text, "</"+tag+">") != 1) {
					return fail("retain the original prompt wrapper tags")
				}
				message.Parts = append(message.Parts, part.Text)
			}
			template.replay.Messages = append(template.replay.Messages, message)
		}
		index++
	}
	if index != len(types) || placeholders != 1 {
		return nil, fmt.Errorf("%w: line %d: expected seven initial records and exactly one challenge placeholder", ErrCodexProbeTemplateInvalid, lastLine)
	}
	return template, nil
}

var parsedDefaultCodexProbeTemplate = sync.OnceValues(func() (*CodexProbeTemplate, error) {
	return ParseCodexProbeTemplate(DefaultCodexProbeTemplate())
})

type codexProbeTemplateContextKey struct{}

// PrepareCodexProbeContext pins one snapshot for a model's harvest and diagnostic.
func (s *OpenAIGatewayService) PrepareCodexProbeContext(ctx context.Context) (context.Context, error) {
	if _, ok := ctx.Value(codexProbeTemplateContextKey{}).(*CodexProbeTemplate); ok {
		return ctx, nil
	}
	template, err := s.settingService.GetCodexProbeTemplate(ctx)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, codexProbeTemplateContextKey{}, template), nil
}

func (s *OpenAIGatewayService) buildCodexProbeRequest(ctx context.Context, account *Account, model string, challenge ModelTraceChallenge) ([]byte, http.Header, error) {
	ctx, err := s.PrepareCodexProbeContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	credential, err := resolveCredentialAccount(ctx, s.accountRepo, account)
	if err != nil || codexAccountIdentityNamespace(credential) == "" {
		return nil, nil, ErrCodexProbeIdentity
	}
	installation := scopeCodexAccountIdentityValue(credential, 0, "installation", "codex-probe-v1")
	identity, err := newCodexReplayIdentity(installation, "", "", time.Now())
	if err != nil {
		return nil, nil, ErrCodexProbeIdentity
	}
	template := ctx.Value(codexProbeTemplateContextKey{}).(*CodexProbeTemplate)
	return template.render(model, challenge.Prompt, account.OpenAIRequestTimezone(), identity)
}

func (template *CodexProbeTemplate) render(model, prompt, timezone string, identity codexReplayIdentity) ([]byte, http.Header, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, nil, ErrCodexProbeIdentity
	}
	replace := strings.NewReplacer("{{TIMEZONE}}", location.String(), "{{CURRENT_DATE}}", identity.startedAt.In(location).Format(time.DateOnly), "{{MODEL}}", model, CodexHypothesisPromptPlaceholder, prompt)
	body, headers := buildCodexReplayRequest(&template.replay, model, "", identity, replace.Replace)
	encoded, err := json.Marshal(body)
	return encoded, headers, err
}

// BuildCodexDiagnosticRequest supplies a normal gateway ingress request. The
// gateway still owns authentication, billing and final account identity scoping.
func (s *OpenAIGatewayService) BuildCodexDiagnosticRequest(ctx context.Context, accountID int64, model string, challenge ModelTraceChallenge) ([]byte, http.Header, error) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return nil, nil, ErrCodexProbeIdentity
	}
	return s.buildCodexProbeRequest(ctx, account, model, challenge)
}
