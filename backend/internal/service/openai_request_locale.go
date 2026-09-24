package service

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

type openAIRequestTimezoneReplacement struct {
	start int
	end   int
	value string
}

func rewriteOpenAIRequestEnvironment(text, timezone, date string) (string, *string, *string, bool, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "<environment_context>") || !strings.HasSuffix(trimmed, "</environment_context>") {
		return text, nil, nil, false, false
	}
	decoder := xml.NewDecoder(strings.NewReader(text))
	depth := 0
	activeTag := ""
	activeStart := 0
	var before, after *string
	var replacements []openAIRequestTimezoneReplacement
	for {
		start := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return text, nil, nil, false, false
		}
		switch element := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 && element.Name.Local != "environment_context" {
				return text, nil, nil, false, false
			}
			if depth == 2 && (element.Name.Local == "timezone" || element.Name.Local == "current_date") {
				if activeTag != "" || (element.Name.Local == "timezone" && before != nil) {
					return text, nil, nil, false, false
				}
				activeTag = element.Name.Local
				activeStart = int(decoder.InputOffset())
				if activeStart >= 2 && text[activeStart-2:activeStart] == "/>" {
					return text, nil, nil, false, false
				}
			}
			if depth > 2 && activeTag != "" {
				return text, nil, nil, false, false
			}
		case xml.EndElement:
			if depth == 2 && activeTag == element.Name.Local {
				original := text[activeStart:start]
				if strings.Contains(original, "<") {
					return text, nil, nil, false, false
				}
				left := len(original) - len(strings.TrimLeft(original, " \t\n\r"))
				right := len(strings.TrimRight(original, " \t\n\r"))
				value := timezone
				if activeTag == "current_date" {
					value = date
				} else {
					previous := strings.TrimSpace(original)
					if len(previous) > 128 {
						previous = previous[:128]
					}
					before = &previous
					next := timezone
					after = &next
				}
				if right >= left {
					replacement := original[:left] + value + original[right:]
					if replacement != original {
						replacements = append(replacements, openAIRequestTimezoneReplacement{activeStart, start, replacement})
					}
				}
				activeTag = ""
			}
			depth--
			if depth < 0 {
				return text, nil, nil, false, false
			}
		}
	}
	if depth != 0 {
		return text, nil, nil, false, false
	}
	for index := len(replacements) - 1; index >= 0; index-- {
		replacement := replacements[index]
		text = text[:replacement.start] + replacement.value + text[replacement.end:]
	}
	return text, before, after, true, before != nil && *before != timezone
}

func normalizeOpenAIRequestLocale(ctx context.Context, account *Account, body []byte, transport string, now time.Time) []byte {
	if account == nil || !account.IsOpenAI() {
		return body
	}
	timezone := account.OpenAIRequestTimezone()
	location, _ := time.LoadLocation(timezone)
	date := now.In(location).Format("2006-01-02")
	before := make([]*string, 0)
	after := make([]*string, 0)
	matched := 0
	replaced := 0
	invalidXML := false
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		for inputIndex, item := range input.Array() {
			if item.Get("role").String() != "user" {
				continue
			}
			kinds := item.Get("internal_chat_message_metadata_passthrough.content_item_kinds")
			content := item.Get("content")
			if !kinds.IsArray() || !content.IsArray() {
				continue
			}
			kindValues := kinds.Array()
			for contentIndex, part := range content.Array() {
				if contentIndex >= len(kindValues) || kindValues[contentIndex].String() != "environments.environment_context" || part.Get("type").String() != "input_text" {
					continue
				}
				text := part.Get("text")
				if text.Type != gjson.String {
					continue
				}
				matched++
				next, prior, current, valid, changed := rewriteOpenAIRequestEnvironment(text.String(), timezone, date)
				before = append(before, prior)
				after = append(after, current)
				if !valid {
					invalidXML = true
					continue
				}
				if next != text.String() {
					updated, err := sjson.SetBytes(body, "input."+strconv.Itoa(inputIndex)+".content."+strconv.Itoa(contentIndex)+".text", next)
					if err == nil {
						body = updated
						if changed {
							replaced++
						}
					} else {
						invalidXML = true
					}
				}
			}
		}
	}
	webSearchBefore := make([]*string, 0)
	webSearchAfter := make([]*string, 0)
	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		for index, tool := range tools.Array() {
			kind := tool.Get("type").String()
			if kind != "web_search" && !strings.HasPrefix(kind, "web_search_") {
				continue
			}
			value := tool.Get("user_location.timezone")
			if value.Type == gjson.String {
				previous := value.String()
				loggedPrevious := previous
				if len(loggedPrevious) > 128 {
					loggedPrevious = loggedPrevious[:128]
				}
				current := loggedPrevious
				if previous != timezone {
					if updated, err := sjson.SetBytes(body, "tools."+strconv.Itoa(index)+".user_location.timezone", timezone); err == nil {
						body = updated
						current = timezone
						replaced++
					}
				}
				webSearchBefore = append(webSearchBefore, &loggedPrevious)
				webSearchAfter = append(webSearchAfter, &current)
			}
		}
	}
	reason := "no_marked_context"
	if replaced > 0 {
		reason = "replaced"
	} else if invalidXML {
		reason = "invalid_xml"
	} else if matched > 0 {
		reason = "no_timezone"
		for _, value := range before {
			if value != nil {
				reason = "already_target"
				break
			}
		}
	}
	logger.FromContext(ctx).Debug("openai request timezone normalization",
		zap.Int64("account_id", account.ID), zap.String("transport", transport),
		zap.Bool("timezone_replaced", replaced > 0), zap.Any("timezone_before", before),
		zap.Any("timezone_after", after), zap.Any("web_search_timezone_before", webSearchBefore),
		zap.Any("web_search_timezone_after", webSearchAfter), zap.String("target_timezone", timezone),
		zap.Int("matched_count", matched), zap.Int("replaced_count", replaced), zap.String("reason", reason))
	return body
}

func normalizeOpenAIRequestAcceptLanguage(account *Account, headers http.Header) {
	if account == nil || !account.IsOpenAI() || headers == nil {
		return
	}
	found := false
	for name := range headers {
		if strings.EqualFold(name, "Accept-Language") {
			found = true
			delete(headers, name)
		}
	}
	if found {
		headers.Set("Accept-Language", "en-US,en;q=0.9")
	}
}
