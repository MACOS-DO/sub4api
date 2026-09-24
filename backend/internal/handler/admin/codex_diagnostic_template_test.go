package admin

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexDiagnosticUsesSharedTemplateAndGateway(t *testing.T) {
	custom := strings.Replace(service.DefaultCodexProbeTemplate(), "anonymous workspace", "shared custom workspace", 1)
	repo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyOpenAICodexTicketPromptTemplate: custom}}
	settings := service.NewSettingService(repo, &config.Config{})
	calls := 0
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/v1/responses", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"), "diagnostics must still enter the billing gateway with the selected API key")
		require.Equal(t, "text/event-stream", r.Header.Get("Accept"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, "gpt-5.4", gjson.GetBytes(body, "model").String(), "older models also use the shared template")
		require.Len(t, gjson.GetBytes(body, "input").Array(), 5)
		require.Contains(t, gjson.GetBytes(body, "input.0.content.0.text").String(), "shared custom workspace")
		require.Contains(t, gjson.GetBytes(body, "input.3.content.0.text").String(), "<timezone>Asia/Singapore</timezone>")
		require.Equal(t, "selected challenge", gjson.GetBytes(body, "input.4.content.0.text").String())
		require.NotEmpty(t, gjson.GetBytes(body, "instructions").String())
		require.Equal(t, r.Header.Get("session_id"), gjson.GetBytes(body, "prompt_cache_key").String())
		require.Equal(t, r.Header.Get("x-codex-turn-metadata"), gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String())
		_, err = io.WriteString(w, `{"status":"completed","output_text":"1"}`)
		require.NoError(t, err)
	})
	h, c, writer := newCodexChallengeDiagnosticHandler(t, []string{"gpt-5.4"}, router, settings)
	h.diagnoseCodexModels(c, func() (service.ModelTraceChallenge, error) {
		return service.ModelTraceChallenge{Prompt: "selected challenge", ExpectedCount: 332}, nil
	})
	require.Equal(t, 1, calls)
	require.Equal(t, http.StatusOK, writer.Code)
	require.Equal(t, "insufficient_numbers", gjson.GetBytes(writer.Body.Bytes(), "data.items.0.reason").String())
}

func TestCodexDiagnosticInvalidTemplateSkipsGatewayAndHarvest(t *testing.T) {
	repo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyOpenAICodexTicketPromptTemplate: "{private-secret"}}
	settings := service.NewSettingService(repo, &config.Config{})
	h, c, writer := newCodexChallengeDiagnosticHandler(t, []string{"gpt-5.4", "gpt-6-astra"}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid template must not enter the gateway")
	}), settings)
	h.diagnoseCodexModels(c, func() (service.ModelTraceChallenge, error) {
		t.Fatal("validate before generating challenges or attempting harvest")
		return service.ModelTraceChallenge{}, nil
	})
	require.Equal(t, http.StatusOK, writer.Code)
	require.NotContains(t, writer.Body.String(), "private-secret")
	items := gjson.GetBytes(writer.Body.Bytes(), "data.items").Array()
	require.Len(t, items, 2)
	for _, item := range items {
		require.Equal(t, "failed", item.Get("status").String())
		require.Equal(t, "template_invalid", item.Get("reason").String())
		require.False(t, item.Get("http_status").Exists())
		require.False(t, item.Get("harvest").Exists())
	}
}

func TestCodexDiagnosticNextModelReadsNewTemplate(t *testing.T) {
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	settings := service.NewSettingService(repo, &config.Config{})
	calls := 0
	h, c, writer := newCodexChallengeDiagnosticHandler(t, []string{"gpt-5.4", "gpt-5.5"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		wanted := "anonymous workspace"
		if calls == 2 {
			wanted = "updated workspace"
		}
		require.Contains(t, gjson.GetBytes(body, "input.0.content.0.text").String(), wanted)
		repo.values[service.SettingKeyOpenAICodexTicketPromptTemplate] = strings.Replace(service.DefaultCodexProbeTemplate(), "anonymous workspace", "updated workspace", 1)
		settings.InvalidateCodexProbeTemplateCache()
		_, err = io.WriteString(w, `{"status":"completed","output_text":"1"}`)
		require.NoError(t, err)
	}), settings)
	h.DiagnoseCodexModels(c)
	require.Equal(t, http.StatusOK, writer.Code)
	require.Equal(t, 2, calls)
}

func TestCodexDiagnosticCanceledBeforeFirstModelDoesNotSend(t *testing.T) {
	h, c, writer := newCodexChallengeDiagnosticHandler(t, []string{"gpt-5.4"}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("canceled diagnostic must not enter the gateway")
	}))
	ctx, cancel := context.WithCancel(c.Request.Context())
	cancel()
	c.Request = c.Request.WithContext(ctx)
	h.DiagnoseCodexModels(c)
	require.True(t, gjson.GetBytes(writer.Body.Bytes(), "data.canceled").Bool())
	require.Empty(t, gjson.GetBytes(writer.Body.Bytes(), "data.items").Array())
}
