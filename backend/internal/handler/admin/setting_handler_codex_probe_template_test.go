package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSettingsCodexProbeTemplateSaveResetAndPartialUpdate(t *testing.T) {
	key := service.SettingKeyOpenAICodexTicketPromptTemplate
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	before, err := h.settingService.GetCodexProbeTemplate(context.Background())
	require.NoError(t, err)
	custom := strings.Replace(service.DefaultCodexProbeTemplate(), "anonymous workspace", "custom workspace", 1)
	response := doUpdateSettings(t, h, map[string]any{key: custom}, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, custom, repo.values[key])
	require.Equal(t, custom, gjson.GetBytes(response.Body.Bytes(), "data."+key).String())
	require.Equal(t, service.DefaultCodexProbeTemplate(), gjson.GetBytes(response.Body.Bytes(), "data."+key+"_default").String())
	after, err := h.settingService.GetCodexProbeTemplate(context.Background())
	require.NoError(t, err)
	require.NotSame(t, before, after, "save must immediately invalidate the cached template")
	response = doUpdateSettings(t, h, map[string]any{"site_name": "Changed title"}, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, custom, repo.values[key])
	get := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(get)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, custom, gjson.GetBytes(get.Body.Bytes(), "data."+key).String())
	response = doUpdateSettings(t, h, map[string]any{key: ""}, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Empty(t, repo.values[key])
	require.Equal(t, service.DefaultCodexProbeTemplate(), gjson.GetBytes(response.Body.Bytes(), "data."+key).String())
}

func TestSettingsCodexProbeTemplateInvalidWriteIsAtomicAndPrivate(t *testing.T) {
	key := service.SettingKeyOpenAICodexTicketPromptTemplate
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{key: service.DefaultCodexProbeTemplate(), "site_name": "Original title"})
	response := doUpdateSettings(t, h, map[string]any{key: "{private-secret", "site_name": "Changed title"}, nil)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "line 1")
	require.NotContains(t, response.Body.String(), "private-secret")
	require.Equal(t, service.DefaultCodexProbeTemplate(), repo.values[key])
	require.Equal(t, "Original title", repo.values["site_name"])
	changed := diffSettings(&service.SystemSettings{}, &service.SystemSettings{OpenAICodexTicketPromptTemplate: "private-secret"}, nil, nil, UpdateSettingsRequest{})
	require.Contains(t, changed, key)
	require.NotContains(t, strings.Join(changed, ","), "private-secret")
}
