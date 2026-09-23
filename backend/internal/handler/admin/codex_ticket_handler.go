package admin

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/response"
	"github.com/MACOS-DO/sub4api/internal/server/middleware"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/gin-gonic/gin"
)

func codexTicketAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	return id, true
}

func codexTicketError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrCodexTicketBusy):
		response.ErrorWithDetails(c, http.StatusConflict, "Ticket attempt already running", "CODEX_TICKET_BUSY", nil)
	case errors.Is(err, service.ErrCodexTicketNoProxy):
		response.ErrorWithDetails(c, http.StatusUnprocessableEntity, "No available ticket proxy", "CODEX_TICKET_NO_PROXY", nil)
	case errors.Is(err, service.ErrCodexTicketModel):
		response.ErrorWithDetails(c, http.StatusBadRequest, "Unsupported ticket model", "CODEX_TICKET_MODEL", nil)
	case errors.Is(err, service.ErrCodexTicketUnavailable):
		response.ErrorWithDetails(c, http.StatusUnprocessableEntity, "Ticket harvesting disabled or account ineligible", "CODEX_TICKET_UNAVAILABLE", nil)
	default:
		response.ErrorFrom(c, err)
	}
}

func (h *AccountHandler) GetCodexTicketHistory(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	model := c.Query("model")
	filter := c.DefaultQuery("filter", "all")
	if filter != "all" && filter != "success" {
		response.BadRequest(c, "filter must be all or success")
		return
	}
	page, size := response.ParsePagination(c)
	if size > 100 {
		size = 100
	}
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	rows, total, status, err := h.codexTicketGateway.CodexTicketHistory(c.Request.Context(), id, model, filter == "success", page, size)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	available, reason := status.HarvestEnabled, ""
	if !available {
		reason = "disabled"
	}
	if available && h.codexTicketSettings != nil {
		proxies, err := h.codexTicketSettings.AvailableCodexTicketProxies(c.Request.Context())
		if err != nil || len(proxies) == 0 {
			available, reason = false, "no_proxy"
		}
	}
	response.Success(c, gin.H{"items": rows, "total": total, "page": page, "page_size": size,
		"ticket_status": status, "manual_available": available, "manual_unavailable_reason": reason})
}

func (h *AccountHandler) GetCodexTicketEvents(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	filter := c.DefaultQuery("filter", "all")
	if filter != "all" && filter != "success" && filter != "failure" && filter != "invalidation" {
		response.BadRequest(c, "Invalid event filter")
		return
	}
	now := time.Now().UTC()
	end := now.Add(time.Second)
	start := now.Add(-90 * 24 * time.Hour)
	if value := c.Query("start_time"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			response.BadRequest(c, "Invalid start_time")
			return
		}
		if parsed.After(start) {
			start = parsed
		}
	}
	if value := c.Query("end_time"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			response.BadRequest(c, "Invalid end_time")
			return
		}
		if parsed.Before(end) {
			end = parsed
		}
	}
	if !end.After(start) {
		response.BadRequest(c, "Invalid time range")
		return
	}
	page, size := response.ParsePagination(c)
	if size > 100 {
		size = 100
	}
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	items, total, err := h.codexTicketGateway.CodexTicketEvents(c.Request.Context(), id, c.Query("model"), filter, start, end, page, size)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

func (h *AccountHandler) HarvestCodexTicket(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	var input struct {
		Model string `json:"model" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "model is required")
		return
	}
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	result, err := h.codexTicketGateway.ManualCodexTicketHarvest(c.Request.Context(), id, input.Model)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) SetCodexTicketParticipation(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	var input struct {
		Enabled *bool           `json:"enabled" binding:"required"`
		Models  map[string]bool `json:"models" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "enabled and models are required")
		return
	}
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	if err := h.codexTicketGateway.SetCodexTicketParticipation(c.Request.Context(), id, *input.Enabled, input.Models); err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, gin.H{"enabled": *input.Enabled, "models": input.Models})
}

func (h *ProxyHandler) GetCodexTicketPool(c *gin.Context) {
	if h.codexTicketSettings == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	pool, err := h.codexTicketSettings.GetCodexTicketPool(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, pool)
}

func (h *ProxyHandler) UpdateCodexTicketPool(c *gin.Context) {
	if h.codexTicketSettings == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	var pool service.CodexTicketPool
	if err := c.ShouldBindJSON(&pool); err != nil {
		response.BadRequest(c, "Invalid proxy pool")
		return
	}
	if err := h.codexTicketSettings.SetCodexTicketPool(c.Request.Context(), pool); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	saved, err := h.codexTicketSettings.GetCodexTicketPool(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, saved)
}

func (h *AccountHandler) ListCodexTicketInvalidations(c *gin.Context) {
	id, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	end := time.Now().UTC().Add(time.Second)
	start := end.Add(-90 * 24 * time.Hour)
	if value := c.Query("start_time"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			response.BadRequest(c, "Invalid start_time")
			return
		}
		if parsed.After(start) {
			start = parsed
		}
	}
	if value := c.Query("end_time"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			response.BadRequest(c, "Invalid end_time")
			return
		}
		if parsed.Before(end) {
			end = parsed
		}
	}
	if !end.After(start) {
		response.BadRequest(c, "Invalid time range")
		return
	}
	page, size := response.ParsePagination(c)
	if size > 100 {
		size = 100
	}
	items, total, err := h.codexTicketGateway.ListCodexTicketInvalidations(c.Request.Context(), id, c.Query("model"), start, end, page, size)
	if err != nil {
		codexTicketError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

func (h *AccountHandler) GetCodexTicketInvalidation(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store, max-age=0")
	c.Header("Pragma", "no-cache")
	id, ok := codexTicketAccountID(c)
	if !ok {
		return
	}
	eventID, err := strconv.ParseInt(c.Param("event_id"), 10, 64)
	if err != nil || eventID <= 0 {
		response.BadRequest(c, "Invalid event ID")
		return
	}
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	item, err := h.codexTicketGateway.GetCodexTicketInvalidation(c.Request.Context(), id, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		response.NotFound(c, "Ticket invalidation not found")
		return
	}
	if err != nil {
		codexTicketError(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"event_id": eventID})
	response.Success(c, item)
}

func (h *AccountHandler) GetCodexFingerprintVersion(c *gin.Context) {
	models, err := service.ModelTraceGPTModels()
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"commit": service.ModelTraceBankCommit(), "models": models})
}

func (h *AccountHandler) RefreshCodexFingerprint(c *gin.Context) {
	if h.codexTicketSettings == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	commit, err := h.codexTicketSettings.RefreshModelTraceBank(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	models, err := service.ModelTraceGPTModels()
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"commit": commit, "models": models})
}

func (h *AccountHandler) GetCodexTicketCadence(c *gin.Context) {
	if h.codexTicketGateway == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	response.Success(c, h.codexTicketGateway.CodexTicketCadence(c.Request.Context()))
}

func (h *AccountHandler) UpdateCodexTicketCadence(c *gin.Context) {
	if h.codexTicketSettings == nil {
		codexTicketError(c, service.ErrCodexTicketUnavailable)
		return
	}
	var value service.CodexTicketCadence
	if err := c.ShouldBindJSON(&value); err != nil {
		response.BadRequest(c, "Invalid cadence")
		return
	}
	if err := h.codexTicketSettings.SetCodexTicketCadence(c.Request.Context(), value); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, value)
}
