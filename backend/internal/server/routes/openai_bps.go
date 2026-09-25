package routes

import (
	"net/http"
	"strings"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
)

// Applied after composite resolution so an explicit BPS route has the same
// endpoint boundary as a standalone BPS group.
func openAIBPSEndpointGuard(c *gin.Context) {
	if getGroupPlatform(c) != service.PlatformOpenAIBPS {
		c.Next()
		return
	}
	path := strings.TrimSuffix(c.Request.URL.Path, "/")
	allowed := c.Request.Method == http.MethodPost && (strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/responses/compact"))
	allowed = allowed || (c.Request.Method == http.MethodGet && (strings.HasSuffix(path, "/models") || strings.Contains(path, "/models/") || path == "/v1/usage"))
	if allowed {
		c.Next()
		return
	}
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "code": "bps_unsupported_endpoint", "message": "OpenAI BPS supports HTTP Responses, compaction and model discovery only"}})
}
