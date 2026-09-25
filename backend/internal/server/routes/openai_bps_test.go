package routes

import (
	"net/http/httptest"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIBPSEndpointBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{{"POST", "/v1/responses", true}, {"POST", "/responses/compact", true}, {"POST", "/backend-api/codex/responses", true}, {"GET", "/v1/models", true}, {"GET", "/responses", false}, {"POST", "/v1/chat/completions", false}, {"POST", "/v1/messages", false}, {"POST", "/v1/responses/input_tokens", false}, {"POST", "/v1/images/generations", false}} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set("api_key", &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAIBPS}})
			}, openAIBPSEndpointGuard)
			r.Handle(tc.method, tc.path, func(c *gin.Context) { c.Status(204) })
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if tc.allowed {
				require.Equal(t, 204, rec.Code)
			} else {
				require.Equal(t, 400, rec.Code)
				require.Contains(t, rec.Body.String(), "bps_unsupported_endpoint")
			}
		})
	}
}
