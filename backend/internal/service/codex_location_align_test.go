package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type stubCodexLocationResolver struct {
	location CodexLocation
	calls    int
}

func (s *stubCodexLocationResolver) Resolve(ctx context.Context, account *Account) CodexLocation {
	s.calls++
	return s.location
}

func newCodexLocationAlignService(location CodexLocation, enabled bool) (*OpenAIGatewayService, *stubCodexLocationResolver) {
	return newCodexLocationAlignServiceWithHeuristic(location, enabled, true)
}

func newCodexLocationAlignServiceWithHeuristic(location CodexLocation, enabled, heuristic bool) (*OpenAIGatewayService, *stubCodexLocationResolver) {
	resolver := &stubCodexLocationResolver{location: location}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			CodexLocation: config.GatewayCodexLocationConfig{
				Enabled:               enabled,
				MarkerAbsentHeuristic: heuristic,
			},
		}},
		codexLocation: resolver,
	}
	return svc, resolver
}

func newOpenAIAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI}
}

func TestAlignCodexLocationRewritesMarkedEnvironmentContext(t *testing.T) {
	svc, _ := newCodexLocationAlignService(CodexLocation{
		Timezone: "America/New_York",
		Country:  "US",
		Region:   "Ohio",
		City:     "Piketon",
	}, true)

	body := []byte(`{
		"model":"gpt-5",
		"input":[
			{"type":"message","role":"user","content":[
				{"type":"input_text","text":"<environment_context>\n  <cwd>/repo</cwd>\n  <shell>zsh</shell>\n  <current_date>2026-09-13</current_date>\n  <timezone>Asia/Shanghai</timezone>\n  <filesystem><file_system type=\"unrestricted\" /></filesystem>\n</environment_context>"},
				{"type":"input_text","text":"<environment_context>user pasted copy</environment_context>"}
			],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["environments.environment_context","user.text"]}},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}
		]
	}`)

	aligned, changed := svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.True(t, changed)
	text := gjson.GetBytes(aligned, "input.0.content.0.text").String()
	require.Contains(t, text, "<timezone>America/New_York</timezone>")
	require.Contains(t, text, "<current_date>"+expectedDate(t, "America/New_York")+"</current_date>")
	// 其余元素逐字节保留。
	require.Contains(t, text, "<cwd>/repo</cwd>")
	require.Contains(t, text, "<shell>zsh</shell>")
	require.Contains(t, text, "<filesystem><file_system type=\"unrestricted\" /></filesystem>")
	// 用户文本（kind=user.text）即使包含同款标签也不得改写。
	require.Contains(t, gjson.GetBytes(aligned, "input.0.content.1.text").String(), "user pasted copy")
	require.NotContains(t, gjson.GetBytes(aligned, "input.0.content.1.text").String(), "America/New_York")
}

func TestAlignCodexLocationHeuristicWithoutMarker(t *testing.T) {
	svc, _ := newCodexLocationAlignService(CodexLocation{
		Timezone: "Asia/Tokyo",
		Country:  "JP",
		Region:   "Tokyo",
		City:     "Tokyo",
	}, true)

	standalone := `<environment_context>
  <cwd>/repo</cwd>
  <current_date>2026-09-13</current_date>
  <timezone>Asia/Shanghai</timezone>
</environment_context>`
	body := []byte(fmt.Sprintf(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}]}`, standalone))

	aligned, changed := svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.True(t, changed)
	require.Contains(t, gjson.GetBytes(aligned, "input.0.content.0.text").String(), "<timezone>Asia/Tokyo</timezone>")

	prefixed := "note: " + standalone
	body = []byte(fmt.Sprintf(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}]}`, prefixed))
	_, changed = svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.False(t, changed)
}

func TestAlignCodexLocationDoesNotResolveWithoutTarget(t *testing.T) {
	location := CodexLocation{Timezone: "America/New_York", Country: "US", Region: "Ohio", City: "Piketon"}

	// 正文出现 "user_location" 字样但没有 web_search 工具：不得触发解析。
	svc, resolver := newCodexLocationAlignService(location, true)
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"please explain user_location semantics"}]}]}`)
	_, changed := svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.False(t, changed)
	require.Equal(t, 0, resolver.calls)

	// 非 web_search 工具上的 user_location 不是改写目标：不得触发解析。
	body = []byte(`{"tools":[{"type":"function","name":"lookup","user_location":{"country":"CN"}}]}`)
	_, changed = svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.False(t, changed)
	require.Equal(t, 0, resolver.calls)

	// 无标记且关闭启发式：不得改写，也不得触发解析。
	svc, resolver = newCodexLocationAlignServiceWithHeuristic(location, true, false)
	block := "<environment_context><current_date>2026-09-13</current_date><timezone>Asia/Shanghai</timezone></environment_context>"
	body = []byte(fmt.Sprintf(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}]}`, block))
	_, changed = svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.False(t, changed)
	require.Equal(t, 0, resolver.calls)

	// 有标记时仍然精确改写，且只解析一次。
	body = []byte(fmt.Sprintf(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":%q}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["environments.environment_context"]}}]}`, block))
	aligned, changed := svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.True(t, changed)
	require.Contains(t, gjson.GetBytes(aligned, "input.0.content.0.text").String(), "<timezone>America/New_York</timezone>")
	require.Equal(t, 1, resolver.calls)
}

func TestAlignCodexLocationResolvesOnlyForWebSearchUserLocation(t *testing.T) {
	location := CodexLocation{Timezone: "America/New_York", Country: "US", Region: "Ohio", City: "Piketon"}

	svc, resolver := newCodexLocationAlignService(location, true)
	body := []byte(`{"tools":[{"type":"web_search_preview","user_location":{"type":"approximate","country":"CN","region":"Shanghai","city":"Shanghai","timezone":"Asia/Shanghai"}}]}`)
	aligned, changed := svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.True(t, changed)
	require.Equal(t, 1, resolver.calls)
	require.Equal(t, "America/New_York", gjson.GetBytes(aligned, "tools.0.user_location.timezone").String())
	require.Equal(t, "US", gjson.GetBytes(aligned, "tools.0.user_location.country").String())
}

func TestAlignCodexLocationRewritesAllInputItemsAndTools(t *testing.T) {
	svc, _ := newCodexLocationAlignService(CodexLocation{
		Timezone: "America/Los_Angeles",
		Country:  "US",
		Region:   "California",
		City:     "Los Angeles",
	}, true)

	block := "<environment_context><current_date>2026-09-13</current_date><timezone>Asia/Shanghai</timezone></environment_context>"
	body := []byte(fmt.Sprintf(`{
		"input":[
			{"role":"user","content":[{"type":"input_text","text":%q}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["environments.environment_context"]}},
			{"role":"user","content":[{"type":"input_text","text":%q}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["environments.environment_context"]}}
		],
		"tools":[
			{"type":"web_search","user_location":{"type":"approximate","country":"CN","region":"Shanghai","city":"Shanghai","timezone":"Asia/Shanghai"}},
			{"type":"web_search_preview"},
			{"type":"function","name":"echo","user_location":{"country":"CN","timezone":"Asia/Shanghai"}}
		]
	}`, block, block))

	aligned, changed := svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.True(t, changed)
	require.Contains(t, gjson.GetBytes(aligned, "input.0.content.0.text").String(), "<timezone>America/Los_Angeles</timezone>")
	require.Contains(t, gjson.GetBytes(aligned, "input.1.content.0.text").String(), "<timezone>America/Los_Angeles</timezone>")
	require.Equal(t, "US", gjson.GetBytes(aligned, "tools.0.user_location.country").String())
	require.Equal(t, "California", gjson.GetBytes(aligned, "tools.0.user_location.region").String())
	require.Equal(t, "Los Angeles", gjson.GetBytes(aligned, "tools.0.user_location.city").String())
	require.Equal(t, "America/Los_Angeles", gjson.GetBytes(aligned, "tools.0.user_location.timezone").String())
	require.Equal(t, "approximate", gjson.GetBytes(aligned, "tools.0.user_location.type").String())
	// 没有 user_location 的 web_search 不新增字段。
	require.False(t, gjson.GetBytes(aligned, "tools.1.user_location").Exists())
	// 非 web_search 工具不动。
	require.Equal(t, "CN", gjson.GetBytes(aligned, "tools.2.user_location.country").String())
}

func TestAlignCodexLocationRewritesSessionToolsUserLocation(t *testing.T) {
	svc, _ := newCodexLocationAlignService(CodexLocation{
		Timezone: "America/Los_Angeles",
		Country:  "US",
		Region:   "California",
		City:     "Los Angeles",
	}, true)

	body := []byte(`{
		"type":"session.update",
		"session":{"tools":[
			{"type":"web_search","user_location":{"type":"approximate","country":"CN","region":"Shanghai","city":"Shanghai","timezone":"Asia/Shanghai"}},
			{"type":"web_search_preview"}
		]}
	}`)

	aligned, changed := svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.True(t, changed)
	require.Equal(t, "US", gjson.GetBytes(aligned, "session.tools.0.user_location.country").String())
	require.Equal(t, "California", gjson.GetBytes(aligned, "session.tools.0.user_location.region").String())
	require.Equal(t, "Los Angeles", gjson.GetBytes(aligned, "session.tools.0.user_location.city").String())
	require.Equal(t, "America/Los_Angeles", gjson.GetBytes(aligned, "session.tools.0.user_location.timezone").String())
	require.False(t, gjson.GetBytes(aligned, "session.tools.1.user_location").Exists())
}

func TestAlignCodexLocationDisabledOrNonOpenAI(t *testing.T) {
	block := "<environment_context><current_date>2026-09-13</current_date><timezone>Asia/Shanghai</timezone></environment_context>"
	body := []byte(fmt.Sprintf(`{"input":[{"role":"user","content":[{"type":"input_text","text":%q}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["environments.environment_context"]}}]}`, block))

	disabledSvc, _ := newCodexLocationAlignService(CodexLocation{Timezone: "Asia/Tokyo"}, false)
	_, changed := disabledSvc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.False(t, changed)

	enabledSvc, _ := newCodexLocationAlignService(CodexLocation{Timezone: "Asia/Tokyo"}, true)
	anthropicAccount := &Account{ID: 2, Platform: PlatformAnthropic}
	_, changed = enabledSvc.alignCodexLocationInRequestBody(context.Background(), anthropicAccount, body)
	require.False(t, changed)
}

func TestAlignCodexLocationAccountOverrideDisables(t *testing.T) {
	svc, resolver := newCodexLocationAlignService(CodexLocation{Timezone: "Asia/Tokyo"}, true)
	account := newOpenAIAccount(1)
	account.Extra = map[string]any{"codex_location_align_enabled": false}
	block := "<environment_context><current_date>2026-09-13</current_date><timezone>Asia/Shanghai</timezone></environment_context>"
	body := []byte(fmt.Sprintf(`{"input":[{"role":"user","content":[{"type":"input_text","text":%q}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["environments.environment_context"]}}]}`, block))

	_, changed := svc.alignCodexLocationInRequestBody(context.Background(), account, body)
	require.False(t, changed)
	require.Zero(t, resolver.calls)
}

func TestAlignCodexLocationRewritesCurrentTimeReminder(t *testing.T) {
	svc, _ := newCodexLocationAlignService(CodexLocation{
		Timezone: "America/New_York",
		Country:  "US",
		Region:   "Ohio",
		City:     "Piketon",
	}, true)

	reminder := "<current_time_reminder>It is 2026-09-23 01:15:30 UTC.</current_time_reminder>"
	body := []byte(fmt.Sprintf(`{"input":[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":%q}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["current_time.reminder"]}},
		{"type":"message","role":"user","content":[{"type":"input_text","text":%q}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["user.text"]}}
	]}`, reminder, reminder))

	aligned, changed := svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.True(t, changed)
	// 2026-09-23 01:15:30 UTC = 2026-09-22 21:15:30 EDT（跨日回退，绝对时刻不变）。
	require.Contains(t, gjson.GetBytes(aligned, "input.0.content.0.text").String(), "It is 2026-09-22 21:15:30 EDT.")
	// 用户粘贴的同款文本（kind=user.text）不得改写。
	require.Contains(t, gjson.GetBytes(aligned, "input.1.content.0.text").String(), "2026-09-23 01:15:30 UTC")
}

func TestAlignCodexLocationCurrentTimeReminderHeuristic(t *testing.T) {
	svc, _ := newCodexLocationAlignService(CodexLocation{
		Timezone: "Asia/Tokyo",
		Country:  "JP",
		Region:   "Tokyo",
		City:     "Tokyo",
	}, true)

	reminder := "<current_time_reminder>It is 2026-09-22 07:15:30 UTC.</current_time_reminder>"
	body := []byte(fmt.Sprintf(`{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":%q}]}]}`, reminder))
	aligned, changed := svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.True(t, changed)
	require.Contains(t, gjson.GetBytes(aligned, "input.0.content.0.text").String(), "It is 2026-09-22 16:15:30 JST.")

	prefixed := "log: " + reminder
	body = []byte(fmt.Sprintf(`{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":%q}]}]}`, prefixed))
	_, changed = svc.alignCodexLocationInRequestBody(context.Background(), newOpenAIAccount(1), body)
	require.False(t, changed)
}

func TestAlignCodexAlphaSearchLocationReplacesExistingOnly(t *testing.T) {
	svc, _ := newCodexLocationAlignService(CodexLocation{
		Timezone: "Asia/Tokyo",
		Country:  "JP",
		Region:   "Tokyo",
		City:     "Tokyo",
	}, true)

	body := []byte(`{"settings":{"user_location":{"type":"approximate","country":"CN","region":"Shanghai","city":"Shanghai","timezone":"Asia/Shanghai"}}}`)
	aligned, changed := svc.alignCodexAlphaSearchLocation(context.Background(), newOpenAIAccount(1), body)
	require.True(t, changed)
	require.Equal(t, "JP", gjson.GetBytes(aligned, "settings.user_location.country").String())
	require.Equal(t, "Asia/Tokyo", gjson.GetBytes(aligned, "settings.user_location.timezone").String())

	body = []byte(`{"settings":{"search_context_size":"high"}}`)
	_, changed = svc.alignCodexAlphaSearchLocation(context.Background(), newOpenAIAccount(1), body)
	require.False(t, changed)
	require.False(t, gjson.GetBytes(body, "settings.user_location").Exists())
}

func TestAlignEnvironmentContextTextPreservesFormatting(t *testing.T) {
	block := `<environment_context>
  <cwd>C:\windows</cwd>
  <shell>powershell</shell>
  <current_date>2026-09-22</current_date>
  <timezone>Asia/Shanghai</timezone>
  <network enabled="true"><allowed>example.com</allowed></network>
</environment_context>`
	aligned, changed := alignEnvironmentContextText(block, "America/New_York", "2026-09-22")
	require.True(t, changed)
	require.Contains(t, aligned, "<cwd>C:\\windows</cwd>")
	require.Contains(t, aligned, "<network enabled=\"true\"><allowed>example.com</allowed></network>")
	require.Contains(t, aligned, "<timezone>America/New_York</timezone>")
	require.Contains(t, aligned, "<current_date>2026-09-22</current_date>")

	_, changed = alignEnvironmentContextText("plain user text", "America/New_York", "2026-09-22")
	require.False(t, changed)
}

func expectedDate(t *testing.T, timezone string) string {
	t.Helper()
	location, err := time.LoadLocation(timezone)
	require.NoError(t, err)
	return time.Now().In(location).Format("2006-01-02")
}
