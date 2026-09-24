package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetOpenAIRequestTimezones(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &AccountHandler{}
	router.GET("/api/v1/admin/accounts/openai-request-timezones", handler.GetOpenAIRequestTimezones)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/openai-request-timezones", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Data struct {
			Default   string   `json:"default"`
			Timezones []string `json:"timezones"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, service.DefaultOpenAIRequestTimezone, payload.Data.Default)
	require.Len(t, payload.Data.Timezones, 30)
	require.Contains(t, payload.Data.Timezones, "Europe/London")
	require.NotContains(t, payload.Data.Timezones, "Europe/Oslo")
	require.NotContains(t, payload.Data.Timezones, "Asia/Shanghai")
}
