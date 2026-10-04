package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	"github.com/MACOS-DO/sub4api/internal/pkg/response"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
)

type codexGatewayAdmin interface {
	CodexGatewayCapabilities(context.Context) (*codexgateway.Capabilities, error)
	CreateCodexOAuthSession(context.Context, string, service.CodexOAuthSessionRequest) (*codexgateway.OAuthSession, error)
	CancelCodexOAuthSession(context.Context, string, string) error
	GetCodexGatewayOperation(context.Context, string, string) (*service.CodexOperationResult, error)
}

func (h *AccountHandler) codexAdmin(c *gin.Context) codexGatewayAdmin {
	s, ok := h.adminService.(codexGatewayAdmin)
	if !ok {
		response.ErrorFrom(c, codexgateway.Unavailable())
		return nil
	}
	c.Header("Cache-Control", "no-store")
	return s
}

func (h *AccountHandler) CodexCapabilities(c *gin.Context) {
	s := h.codexAdmin(c)
	if s == nil {
		return
	}
	result, err := s.CodexGatewayCapabilities(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) CreateCodexOAuthSession(c *gin.Context) {
	s := h.codexAdmin(c)
	if s == nil {
		return
	}
	var input service.CodexOAuthSessionRequest
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid authorization session request")
		return
	}
	result, err := s.CreateCodexOAuthSession(c.Request.Context(), adminActorScope(c), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) CancelCodexOAuthSession(c *gin.Context) {
	s := h.codexAdmin(c)
	if s == nil {
		return
	}
	if err := s.CancelCodexOAuthSession(c.Request.Context(), adminActorScope(c), c.Param("session_id")); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *AccountHandler) GetCodexOperation(c *gin.Context) {
	s := h.codexAdmin(c)
	if s == nil {
		return
	}
	result, err := s.GetCodexGatewayOperation(c.Request.Context(), adminActorScope(c), c.Param("operation_key"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) SyncCodexGateway(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	syncer, ok := h.adminService.(interface {
		SyncCodexGatewayAccount(context.Context, int64) (*service.Account, error)
	})
	if !ok {
		response.ErrorFrom(c, service.ErrAccountNotFound)
		return
	}
	account, err := syncer.SyncCodexGatewayAccount(c.Request.Context(), id)
	if err != nil {
		var failure *codexgateway.Error
		if errors.As(err, &failure) {
			c.JSON(failure.Status, gin.H{"code": failure.Code, "message": failure.Message})
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.buildAccountResponseWithRuntime(c.Request.Context(), account))
}
