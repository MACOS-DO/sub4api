//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPublicSettingsDoNotExposeCodexProbeTemplate(t *testing.T) {
	repo := &settingHandlerPublicRepoStub{values: map[string]string{
		service.SettingKeyOpenAICodexTicketPromptTemplate: "private-template-text",
	}}
	h := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), "test")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings/public", nil)
	h.GetPublicSettings(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "private-template-text")
	require.NotContains(t, w.Body.String(), "openai_codex_ticket_prompt_template")
}
