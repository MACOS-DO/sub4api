package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func localeTestBody(texts []string, kinds []string) []byte {
	content := make([]any, 0, len(texts))
	for _, text := range texts {
		content = append(content, map[string]any{"type": "input_text", "text": text})
	}
	body, _ := json.Marshal(map[string]any{
		"input": []any{map[string]any{
			"role": "user", "content": content,
			"internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": kinds},
		}},
		"tools": []any{map[string]any{"type": "web_search", "user_location": map[string]any{
			"country": "US", "city": "New York", "timezone": "America/New_York",
		}}},
	})
	return body
}

func TestOpenAIRequestTimezoneCatalogAndValidation(t *testing.T) {
	options := OpenAIRequestTimezoneOptions()
	require.ElementsMatch(t, []string{
		"Africa/Cairo", "Africa/Johannesburg",
		"America/Argentina/Buenos_Aires", "America/Chicago", "America/Denver",
		"America/Los_Angeles", "America/Mexico_City", "America/New_York",
		"America/Sao_Paulo", "America/Toronto", "America/Vancouver",
		"Asia/Bangkok", "Asia/Dubai", "Asia/Ho_Chi_Minh", "Asia/Jakarta",
		"Asia/Kolkata", "Asia/Kuala_Lumpur", "Asia/Manila", "Asia/Seoul",
		"Asia/Singapore", "Asia/Tokyo", "Australia/Perth", "Australia/Sydney",
		"Europe/Berlin", "Europe/Istanbul", "Europe/London", "Europe/Moscow",
		"Europe/Paris", "Pacific/Auckland", "Pacific/Honolulu",
	}, options)
	require.Len(t, options, 30)
	for _, forbidden := range []string{"Asia/Shanghai", "Asia/Urumqi", "Asia/Hong_Kong", "Asia/Macau", "Asia/Taipei", "Asia/Chongqing", "Asia/Macao", "Hongkong", "PRC", "ROC"} {
		require.NotContains(t, options, forbidden)
		require.Error(t, ValidateOpenAIRequestTimezoneExtra(PlatformOpenAI, map[string]any{openAIRequestTimezoneExtraKey: forbidden}))
	}
	for _, retired := range []string{"Europe/Oslo", "Africa/Accra", "Asia/Kathmandu"} {
		require.Error(t, ValidateOpenAIRequestTimezoneExtra(PlatformOpenAI, map[string]any{openAIRequestTimezoneExtraKey: retired}))
		require.Equal(t, DefaultOpenAIRequestTimezone, (&Account{Platform: PlatformOpenAI, Extra: map[string]any{openAIRequestTimezoneExtraKey: retired}}).OpenAIRequestTimezone())
	}
	require.NoError(t, ValidateOpenAIRequestTimezoneExtra(PlatformOpenAI, map[string]any{openAIRequestTimezoneExtraKey: "Asia/Tokyo"}))
	require.NoError(t, ValidateOpenAIRequestTimezoneExtra(PlatformOpenAI, map[string]any{openAIRequestTimezoneExtraKey: ""}))
	require.Error(t, ValidateOpenAIRequestTimezoneExtra(PlatformOpenAI, map[string]any{openAIRequestTimezoneExtraKey: 8}))
	require.Error(t, ValidateOpenAIRequestTimezoneExtra(PlatformAnthropic, map[string]any{openAIRequestTimezoneExtraKey: "Asia/Singapore"}))
	require.Equal(t, DefaultOpenAIRequestTimezone, (&Account{Platform: PlatformOpenAI}).OpenAIRequestTimezone())
	require.Equal(t, DefaultOpenAIRequestTimezone, (&Account{Platform: PlatformOpenAI, Extra: map[string]any{openAIRequestTimezoneExtraKey: "Asia/Shanghai"}}).OpenAIRequestTimezone())
	require.Equal(t, "America/New_York", (&Account{Platform: PlatformOpenAI, Extra: map[string]any{openAIRequestTimezoneExtraKey: "America/New_York"}}).OpenAIRequestTimezone())
}

func TestNormalizeOpenAIRequestLocaleAndDebugLog(t *testing.T) {
	first := "<environment_context>\n  <current_date>2026-09-22</current_date>\n  <timezone>Asia/Shanghai</timezone>\n  <cwd>/app</cwd>\n</environment_context>"
	second := "<environment_context><timezone>Asia/Singapore</timezone></environment_context>"
	body := localeTestBody([]string{first, second, first}, []string{"environments.environment_context", "environments.environment_context", "user.text"})
	core, observed := observer.New(zap.DebugLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	out := normalizeOpenAIRequestLocale(ctx, &Account{ID: 42, Platform: PlatformOpenAI}, body, "http", time.Date(2026, 9, 22, 17, 0, 0, 0, time.UTC))
	require.Contains(t, gjson.GetBytes(out, "input.0.content.0.text").String(), "<current_date>2026-09-23</current_date>")
	require.Contains(t, gjson.GetBytes(out, "input.0.content.0.text").String(), "<timezone>Asia/Singapore</timezone>")
	require.Contains(t, gjson.GetBytes(out, "input.0.content.0.text").String(), "<cwd>/app</cwd>")
	require.Equal(t, second, gjson.GetBytes(out, "input.0.content.1.text").String())
	require.Equal(t, first, gjson.GetBytes(out, "input.0.content.2.text").String())
	require.Equal(t, "US", gjson.GetBytes(out, "tools.0.user_location.country").String())
	require.Equal(t, "Asia/Singapore", gjson.GetBytes(out, "tools.0.user_location.timezone").String())
	require.Len(t, observed.All(), 1)
	fields, err := json.Marshal(observed.All()[0].ContextMap())
	require.NoError(t, err)
	require.Contains(t, string(fields), `"timezone_replaced":true`)
	require.Contains(t, string(fields), `"matched_count":2`)
	require.Contains(t, string(fields), `"replaced_count":2`)
	require.Contains(t, string(fields), `"web_search_timezone_before":["America/New_York"]`)
	require.Contains(t, string(fields), `"timezone_before":["Asia/Shanghai","Asia/Singapore"]`)
	require.NotContains(t, string(fields), "/app")
}

func TestNormalizeOpenAIRequestLocaleNoReplacementStillLogs(t *testing.T) {
	cases := []struct{ name, text, kind, reason string }{
		{"already target", "<environment_context><timezone>Asia/Singapore</timezone></environment_context>", "environments.environment_context", "already_target"},
		{"no timezone", "<environment_context><current_date>old</current_date></environment_context>", "environments.environment_context", "no_timezone"},
		{"invalid xml", "<environment_context><timezone><nested/></timezone></environment_context>", "environments.environment_context", "invalid_xml"},
		{"self closing", "<environment_context><timezone/></environment_context>", "environments.environment_context", "invalid_xml"},
		{"unmarked", "<environment_context><timezone>Asia/Shanghai</timezone></environment_context>", "user.text", "no_marked_context"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			core, observed := observer.New(zap.DebugLevel)
			body := localeTestBody([]string{item.text}, []string{item.kind})
			body, err := sjson.SetBytes(body, "tools.0.user_location.timezone", "Asia/Singapore")
			require.NoError(t, err)
			out := normalizeOpenAIRequestLocale(logger.IntoContext(context.Background(), zap.New(core)), &Account{Platform: PlatformOpenAI}, body, "ws", time.Now())
			require.Len(t, observed.All(), 1)
			require.Equal(t, item.reason, observed.All()[0].ContextMap()["reason"])
			require.Equal(t, false, observed.All()[0].ContextMap()["timezone_replaced"])
			if item.reason != "no_timezone" {
				require.Equal(t, item.text, gjson.GetBytes(out, "input.0.content.0.text").String())
			}
		})
	}
	core, observed := observer.New(zap.InfoLevel)
	normalizeOpenAIRequestLocale(logger.IntoContext(context.Background(), zap.New(core)), &Account{Platform: PlatformOpenAI}, localeTestBody(nil, nil), "http", time.Now())
	require.Empty(t, observed.All())
}

func TestNormalizeOpenAIRequestLocaleWebSearchOnlyAndDST(t *testing.T) {
	text := "<environment_context><current_date>old</current_date><timezone>Asia/Singapore</timezone></environment_context>"
	account := &Account{Platform: PlatformOpenAI, Extra: map[string]any{openAIRequestTimezoneExtraKey: "America/New_York"}}
	core, observed := observer.New(zap.DebugLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	out := normalizeOpenAIRequestLocale(ctx, &Account{Platform: PlatformOpenAI}, localeTestBody([]string{text}, []string{"user.text"}), "ws", time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC))
	require.Equal(t, text, gjson.GetBytes(out, "input.0.content.0.text").String())
	require.Equal(t, "Asia/Singapore", gjson.GetBytes(out, "tools.0.user_location.timezone").String())
	require.Len(t, observed.All(), 1)
	require.Equal(t, true, observed.All()[0].ContextMap()["timezone_replaced"])
	require.Equal(t, int64(1), observed.All()[0].ContextMap()["replaced_count"])

	marked := localeTestBody([]string{text}, []string{"environments.environment_context"})
	next := normalizeOpenAIRequestLocale(context.Background(), account, marked, "ws", time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC))
	require.Contains(t, gjson.GetBytes(next, "input.0.content.0.text").String(), "<current_date>2026-03-07</current_date>")
	require.Contains(t, gjson.GetBytes(next, "input.0.content.0.text").String(), "<timezone>America/New_York</timezone>")
}

func TestNormalizeOpenAIRequestAcceptLanguage(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI}
	headers := http.Header{"accept-language": {"zh-CN", "zh"}, "Accept-Language": {"fr-FR"}}
	normalizeOpenAIRequestAcceptLanguage(account, headers)
	require.Equal(t, []string{"en-US,en;q=0.9"}, headers.Values("Accept-Language"))
	require.Len(t, headers, 1)
	absent := http.Header{"Content-Type": {"application/json"}}
	normalizeOpenAIRequestAcceptLanguage(account, absent)
	require.Empty(t, absent.Get("Accept-Language"))
	other := http.Header{"Accept-Language": {"zh-CN"}}
	normalizeOpenAIRequestAcceptLanguage(&Account{Platform: PlatformAnthropic}, other)
	require.Equal(t, "zh-CN", other.Get("Accept-Language"))
}

func TestForwardOpenAIRequestLocaleUsesFormalPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	client, _ := gin.CreateTestContext(recorder)
	environment := "<environment_context><current_date>old</current_date><timezone>Asia/Shanghai</timezone></environment_context>"
	body := localeTestBody([]string{environment, environment}, []string{"environments.environment_context", "user.text"})
	body, _ = sjson.SetBytes(body, "model", "gpt-5.4")
	body, _ = sjson.SetBytes(body, "stream", false)
	body, _ = sjson.SetBytes(body, "instructions", "test")
	body, _ = sjson.SetBytes(body, "input.0.type", "message")
	body, _ = sjson.DeleteBytes(body, "tools")
	client.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	client.Request.Header.Set("Content-Type", "application/json")
	client.Request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader("{\"id\":\"resp_locale\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")),
	}}
	service := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{ID: 100, Name: "openai-oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Concurrency: 1, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"},
	}
	result, err := service.Forward(context.Background(), client, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "en-US,en;q=0.9", upstream.lastReq.Header.Get("Accept-Language"))
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String(), "<timezone>Asia/Singapore</timezone>")
	require.Equal(t, environment, gjson.GetBytes(upstream.lastBody, "input.0.content.1.text").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input.0.internal_chat_message_metadata_passthrough").Exists())
}
