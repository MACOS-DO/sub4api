package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/logger"
	"go.uber.org/zap"
)

type codexTicketRequestContextKey struct{}

func optionalCodexCredential(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (s *OpenAIGatewayService) observeCodexTicketResponse(request *http.Request, response *http.Response, account *Account) {
	if request == nil || response == nil || account == nil || s.openaiCodexTicketLifecycle == nil {
		return
	}
	ticket, ok := request.Context().Value(codexTicketRequestContextKey{}).(*openAICodexTicket)
	if !ok || ticket == nil || ticket.AccountID != account.ID {
		return
	}
	returned := strings.TrimSpace(extractOpenAICodexTurnState(response.Header))
	if returned == "" || returned == ticket.State {
		return
	}
	status := response.StatusCode
	route := request.URL.Path
	if strings.Contains(route, "/responses") {
		route = "/v1/responses"
	}
	if strings.Contains(route, "/messages") {
		route = "/v1/messages"
	}
	requestKind := "user_request"
	if isCodexTicketDiagnostic(request.Context()) {
		requestKind = "diagnostic"
	}
	event := &CodexTicketInvalidation{
		AccountID: account.ID, Model: ticket.Model, TicketGenerationID: ticket.GenerationID,
		OccurredAt: time.Now().UTC(), ReasonCode: "upstream_new_turn_state",
		ResponseHTTPStatus: &status, RequestKind: requestKind, RequestRoute: route,
		OriginalTicket: optionalCodexCredential(ticket.State), OriginalCookie: optionalCodexCredential(ticket.Cookie),
		ReturnedTicket: returned, ReturnedCookie: optionalCodexCredential(extractOpenAICodexResponseCookies(response.Header)),
		ReturnedSetCookies: response.Header.Values("Set-Cookie"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inserted, err := s.openaiCodexTicketLifecycle.Invalidate(ctx, event)
	if err != nil {
		logger.L().Error("codex ticket invalidation persist failed", zap.Int64("account_id", account.ID), zap.String("model", ticket.Model), zap.Error(err))
		return
	}
	if inserted && event.InvalidatedCurrent {
		key := openAICodexTicketKey(account.ID, ticket.Model)
		if cached, ok := s.openaiCodexTickets.Load(key); ok {
			if current, ok := cached.(*openAICodexTicket); ok && current.GenerationID == ticket.GenerationID {
				if s.openaiCodexTickets.CompareAndDelete(key, cached) {
					s.openaiCodexTicketNextAttempt.Delete(key)
				}
			}
		}
		if account.Extra != nil {
			current := parseOpenAICodexTicketFromAny(account.ID, ticket.Model, account.Extra[openAICodexTicketExtraKey(ticket.Model)])
			if current != nil && current.GenerationID == ticket.GenerationID {
				delete(account.Extra, openAICodexTicketExtraKey(ticket.Model))
			}
		}
	}
}
