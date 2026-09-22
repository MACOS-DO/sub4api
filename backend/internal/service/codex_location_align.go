package service

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexEnvironmentContextOpenPrefix = "<environment_context"
	codexEnvironmentContextCloseTag   = "</environment_context>"
	codexEnvironmentContextKind       = "environments.environment_context"

	codexCurrentTimeReminderOpenTag  = "<current_time_reminder>"
	codexCurrentTimeReminderCloseTag = "</current_time_reminder>"
	codexCurrentTimeReminderKind     = "current_time.reminder"
)

// codexCurrentTimeReminderPattern 匹配 Codex 注入的 UTC 时间提醒主体，
// 例如 "2026-09-22 07:15:30 UTC"。
var codexCurrentTimeReminderPattern = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})\s+UTC`)

// isCodexLocationAlignEnabled 判断当前账号是否启用出口地理对齐。
func (s *OpenAIGatewayService) isCodexLocationAlignEnabled(account *Account) bool {
	if s == nil || s.cfg == nil || !s.cfg.Gateway.CodexLocation.Enabled {
		return false
	}
	if account == nil || !account.IsOpenAI() {
		return false
	}
	if override := account.CodexLocationAlignOverride(); override != nil {
		return *override
	}
	return true
}

// alignCodexLocationInRequestBody 把 Codex 请求里的时区/时间/地点提示对齐到账号
// 出口地理：
//   - environment_context 的 <timezone> 与 <current_date>（日期取目标时区“今天”，
//     不按原 current_date 做跨时区换算）；
//   - current_time_reminder 的本地时间（解析原 UTC 时刻后按目标时区换算日期 + 时刻 + 时区缩写）；
//   - 已存在的 web_search user_location 四字段。
//
// 返回是否发生改写。
func (s *OpenAIGatewayService) alignCodexLocationInRequestBody(ctx context.Context, account *Account, body []byte) ([]byte, bool) {
	if s == nil || s.codexLocation == nil || len(body) == 0 {
		return body, false
	}
	if !s.isCodexLocationAlignEnabled(account) {
		return body, false
	}
	hasEnvironmentContext := bytes.Contains(body, []byte(codexEnvironmentContextOpenPrefix))
	hasTimeReminder := bytes.Contains(body, []byte(codexCurrentTimeReminderOpenTag))
	hasUserLocation := bytes.Contains(body, []byte("user_location"))
	if !hasEnvironmentContext && !hasTimeReminder && !hasUserLocation {
		return body, false
	}

	// 惰性解析：只有真正命中待改写目标时才探测出口地理，避免正文恰好出现
	// "user_location" 或标签字样的请求触发无意义的探测与等待。每次请求最多解析一次。
	var (
		resolved bool
		location CodexLocation
		date     string
		usable   bool
	)
	resolveLocation := func() (CodexLocation, string, bool) {
		if !resolved {
			resolved = true
			location = s.codexLocation.Resolve(ctx, account)
			usable = codexLocationTimezoneUsable(location.Timezone)
			if usable {
				// 日期取目标时区的“今天”，不按原 current_date 换算：单个日期没有
				// 时刻，跨时区换算存在歧义，而环境上下文语义就是“出口地的当天”。
				date = location.Date(time.Now())
			}
		}
		return location, date, usable
	}

	heuristic := true
	if s.cfg != nil {
		heuristic = s.cfg.Gateway.CodexLocation.MarkerAbsentHeuristic
	}

	updated := body
	changed := false
	if hasEnvironmentContext {
		next, envChanged := alignCodexContentTexts(updated, codexEnvironmentContextKind, heuristic, func(text string, marked bool) (string, bool) {
			if !marked && !isStandaloneEnvironmentContextText(text) {
				return text, false
			}
			loc, date, ok := resolveLocation()
			if !ok {
				return text, false
			}
			return alignEnvironmentContextText(text, loc.Timezone, date)
		})
		if envChanged {
			updated = next
			changed = true
		}
	}
	if hasTimeReminder {
		next, reminderChanged := alignCodexContentTexts(updated, codexCurrentTimeReminderKind, heuristic, func(text string, marked bool) (string, bool) {
			if !marked && !isStandaloneCurrentTimeReminderText(text) {
				return text, false
			}
			loc, _, ok := resolveLocation()
			if !ok {
				return text, false
			}
			return alignCurrentTimeReminderText(text, loc.Timezone)
		})
		if reminderChanged {
			updated = next
			changed = true
		}
	}
	if hasUserLocation {
		next, toolsChanged := alignCodexWebSearchUserLocations(updated, resolveLocation)
		if toolsChanged {
			updated = next
			changed = true
		}
	}
	return updated, changed
}

// alignCodexContentTexts 遍历 input[] 中类型为 input_text 的内容：
// content_item_kinds 标记命中指定 kind 时精确改写；标记缺失（或越界）时，只有
// 满足启发式（rewrite 收到 marked=false 且自行判定整段恰为完整块）才改写。
func alignCodexContentTexts(body []byte, kind string, heuristic bool, rewrite func(text string, marked bool) (string, bool)) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false
	}
	updated := body
	changed := false
	for i, item := range input.Array() {
		if !item.IsObject() {
			continue
		}
		content := item.Get("content")
		if !content.IsArray() {
			continue
		}
		var kinds []gjson.Result
		if kindsResult := item.Get("internal_chat_message_metadata_passthrough.content_item_kinds"); kindsResult.IsArray() {
			kinds = kindsResult.Array()
		}
		for j, part := range content.Array() {
			if !part.IsObject() || part.Get("type").String() != "input_text" {
				continue
			}
			marked := false
			if j < len(kinds) {
				if kinds[j].String() != kind {
					continue
				}
				marked = true
			} else if !heuristic {
				continue
			}
			rewritten, ok := rewrite(part.Get("text").String(), marked)
			if !ok {
				continue
			}
			next, err := sjson.SetBytes(updated, fmt.Sprintf("input.%d.content.%d.text", i, j), rewritten)
			if err != nil {
				continue
			}
			updated = next
			changed = true
		}
	}
	return updated, changed
}

// alignEnvironmentContextText 只替换 <environment_context> 块内 <timezone> 与
// <current_date> 的内部文本，其余字节（cwd/shell/network/filesystem/空白）原样保留。
// date 由调用方按目标时区“当天”计算，不按原 current_date 做跨时区换算。
func alignEnvironmentContextText(text, timezone, date string) (string, bool) {
	openIdx := strings.Index(text, codexEnvironmentContextOpenPrefix)
	if openIdx < 0 {
		return text, false
	}
	closeRel := strings.Index(text[openIdx:], codexEnvironmentContextCloseTag)
	if closeRel < 0 {
		return text, false
	}
	closeIdx := openIdx + closeRel + len(codexEnvironmentContextCloseTag)
	block := text[openIdx:closeIdx]
	updated := replaceXMLElementInnerText(block, "timezone", timezone)
	updated = replaceXMLElementInnerText(updated, "current_date", date)
	if updated == block {
		return text, false
	}
	return text[:openIdx] + updated + text[closeIdx:], true
}

func replaceXMLElementInnerText(block, tag, value string) string {
	openTag := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	start := strings.Index(block, openTag)
	if start < 0 {
		return block
	}
	innerStart := start + len(openTag)
	closeRel := strings.Index(block[innerStart:], closeTag)
	if closeRel < 0 {
		return block
	}
	innerEnd := innerStart + closeRel
	if block[innerStart:innerEnd] == value {
		return block
	}
	return block[:innerStart] + value + block[innerEnd:]
}

// isStandaloneEnvironmentContextText 判断文本去空白后是否恰好是一个完整的
// environment_context 块（无标记老客户端的启发式改写条件）。
func isStandaloneEnvironmentContextText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, codexEnvironmentContextOpenPrefix) {
		return false
	}
	if !strings.HasSuffix(trimmed, codexEnvironmentContextCloseTag) {
		return false
	}
	openEnd := strings.Index(trimmed, ">")
	if openEnd < 0 {
		return false
	}
	closeStart := len(trimmed) - len(codexEnvironmentContextCloseTag)
	if closeStart <= openEnd+1 {
		return false
	}
	return strings.TrimSpace(trimmed[openEnd+1:closeStart]) != ""
}

// alignCurrentTimeReminderText 把 <current_time_reminder> 里的 UTC 日期/时刻
// 换算成目标时区的本地日期/时刻与时区缩写，保持绝对时刻不变。
func alignCurrentTimeReminderText(text, timezone string) (string, bool) {
	openIdx := strings.Index(text, codexCurrentTimeReminderOpenTag)
	if openIdx < 0 {
		return text, false
	}
	closeRel := strings.Index(text[openIdx:], codexCurrentTimeReminderCloseTag)
	if closeRel < 0 {
		return text, false
	}
	closeIdx := openIdx + closeRel + len(codexCurrentTimeReminderCloseTag)
	block := text[openIdx:closeIdx]
	match := codexCurrentTimeReminderPattern.FindStringSubmatchIndex(block)
	if match == nil {
		return text, false
	}
	instant, err := time.Parse("2006-01-02 15:04:05", block[match[2]:match[3]]+" "+block[match[4]:match[5]])
	if err != nil {
		return text, false
	}
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil || location == nil {
		return text, false
	}
	replacement := instant.In(location).Format("2006-01-02 15:04:05 MST")
	updated := block[:match[0]] + replacement + block[match[1]:]
	if updated == block {
		return text, false
	}
	return text[:openIdx] + updated + text[closeIdx:], true
}

// isStandaloneCurrentTimeReminderText 判断文本去空白后是否恰好是一条完整的
// current_time_reminder（无标记老客户端的启发式改写条件）。
func isStandaloneCurrentTimeReminderText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, codexCurrentTimeReminderOpenTag) {
		return false
	}
	if !strings.HasSuffix(trimmed, codexCurrentTimeReminderCloseTag) {
		return false
	}
	return codexCurrentTimeReminderPattern.MatchString(trimmed)
}

// alignCodexAlphaSearchLocation 覆盖 /alpha/search 请求里已存在的
// settings.user_location 四字段（country/region/city/timezone），不新增字段。
func (s *OpenAIGatewayService) alignCodexAlphaSearchLocation(ctx context.Context, account *Account, body []byte) ([]byte, bool) {
	if s == nil || s.codexLocation == nil || len(body) == 0 {
		return body, false
	}
	if !s.isCodexLocationAlignEnabled(account) {
		return body, false
	}
	if !gjson.GetBytes(body, "settings.user_location").IsObject() {
		return body, false
	}
	location := s.codexLocation.Resolve(ctx, account)
	if !codexLocationTimezoneUsable(location.Timezone) {
		return body, false
	}
	return alignUserLocationObject(body, "settings.user_location", location)
}

// alignCodexWebSearchUserLocations 覆盖 web_search 工具里已存在的 user_location
// 四字段（country/region/city/timezone），不新增 user_location 本身。
// 覆盖顶层 tools、Realtime 风格 session.update 的 session.tools，以及 input[].tools。
// resolve 只在真正找到 web_search + user_location 目标时才被调用。
func alignCodexWebSearchUserLocations(body []byte, resolve func() (CodexLocation, string, bool)) ([]byte, bool) {
	updated, changed := alignCodexToolsArrayUserLocations(body, "tools", resolve)
	if next, sessionChanged := alignCodexToolsArrayUserLocations(updated, "session.tools", resolve); sessionChanged {
		updated = next
		changed = true
	}
	input := gjson.GetBytes(updated, "input")
	if input.IsArray() {
		for i, item := range input.Array() {
			if !item.IsObject() || !item.Get("tools").IsArray() {
				continue
			}
			next, toolsChanged := alignCodexToolsArrayUserLocations(updated, fmt.Sprintf("input.%d.tools", i), resolve)
			if toolsChanged {
				updated = next
				changed = true
			}
		}
	}
	return updated, changed
}

func alignCodexToolsArrayUserLocations(body []byte, path string, resolve func() (CodexLocation, string, bool)) ([]byte, bool) {
	tools := gjson.GetBytes(body, path)
	if !tools.IsArray() {
		return body, false
	}
	updated := body
	changed := false
	for i, tool := range tools.Array() {
		if !tool.IsObject() {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(tool.Get("type").String()), "web_search") {
			continue
		}
		if !tool.Get("user_location").IsObject() {
			continue
		}
		location, _, ok := resolve()
		if !ok {
			return updated, changed
		}
		base := fmt.Sprintf("%s.%d.user_location", path, i)
		next, locationChanged := alignUserLocationObject(updated, base, location)
		if locationChanged {
			updated = next
			changed = true
		}
	}
	return updated, changed
}

// alignUserLocationObject 覆盖指定路径上已存在的 user_location 对象字段。
func alignUserLocationObject(body []byte, base string, location CodexLocation) ([]byte, bool) {
	userLocation := gjson.GetBytes(body, base)
	if !userLocation.Exists() || !userLocation.IsObject() {
		return body, false
	}
	updated := body
	changed := false
	for _, field := range []struct{ key, value string }{
		{"type", "approximate"},
		{"country", location.Country},
		{"region", location.Region},
		{"city", location.City},
		{"timezone", location.Timezone},
	} {
		value := strings.TrimSpace(field.value)
		if value == "" {
			continue
		}
		if strings.TrimSpace(userLocation.Get(field.key).String()) == value {
			continue
		}
		next, err := sjson.SetBytes(updated, base+"."+field.key, value)
		if err != nil {
			continue
		}
		updated = next
		changed = true
	}
	return updated, changed
}
