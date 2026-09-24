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

const CodexTicketInvalidationCredentialsChanged = "upstream_turn_state_and_oailb_changed"

// Capture before dispatch: neither a later header rewrite nor a newly harvested
// generation may change the credentials against which this response is checked.
type codexTicketRequestSnapshot struct {
	accountID    int64
	model        string
	generationID string
	state        string
	cookie       string
	kind         string
	route        string
}

func optionalCodexCredential(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func snapshotCodexTicketRequest(ticket *openAICodexTicket, headers http.Header, kind, route string) *codexTicketRequestSnapshot {
	if ticket == nil || ticket.GenerationID == "" {
		return nil
	}
	state := strings.TrimSpace(extractOpenAICodexTurnState(headers))
	if state == "" || state != strings.TrimSpace(ticket.State) {
		return nil
	}
	cookie := strings.Join(headers.Values("Cookie"), "; ")
	sentCookies, err := http.ParseCookie(cookie)
	if err != nil {
		return nil
	}
	ticketCookies, err := http.ParseCookie(ticket.Cookie)
	if err != nil {
		return nil
	}
	sentOAILB, sentOK := codexOAILBValue(sentCookies)
	ticketOAILB, ticketOK := codexOAILBValue(ticketCookies)
	// A reused WS connection can replace the turn-state with its own historical
	// state. Only credentials actually sent for this generation can invalidate it.
	if !sentOK || !ticketOK || sentOAILB != ticketOAILB {
		return nil
	}
	if strings.Contains(route, "/responses") {
		route = "/v1/responses"
	}
	if strings.Contains(route, "/messages") {
		route = "/v1/messages"
	}
	return &codexTicketRequestSnapshot{
		accountID: ticket.AccountID, model: ticket.Model, generationID: ticket.GenerationID,
		state: state, cookie: cookie, kind: kind, route: route,
	}
}

func snapshotCodexTicketHTTPRequest(request *http.Request, account *Account) *codexTicketRequestSnapshot {
	if request == nil || account == nil {
		return nil
	}
	ticket, _ := request.Context().Value(codexTicketRequestContextKey{}).(*openAICodexTicket)
	if ticket == nil || ticket.AccountID != account.ID {
		return nil
	}
	kind := "user_request"
	if isCodexTicketDiagnostic(request.Context()) {
		kind = "diagnostic"
	}
	route := ""
	if request.URL != nil {
		route = request.URL.Path
	}
	return snapshotCodexTicketRequest(ticket, request.Header, kind, route)
}

// Only a single unambiguous __oailb value counts. Missing or malformed cookies
// are not evidence of a change; attributes and unrelated cookies are ignored.
func codexOAILBValue(cookies []*http.Cookie) (string, bool) {
	value, found := "", false
	for _, cookie := range cookies {
		if cookie.Name != "__oailb" {
			continue
		}
		if found && value != cookie.Value {
			return "", false
		}
		value, found = cookie.Value, true
	}
	return value, found
}

func codexResponseOAILB(headers http.Header) (string, bool) {
	var cookies []*http.Cookie
	for _, line := range headers.Values("Set-Cookie") {
		name, _, _ := strings.Cut(line, "=")
		if strings.TrimSpace(name) != "__oailb" {
			continue
		}
		cookie, err := http.ParseSetCookie(line)
		if err != nil {
			return "", false
		}
		cookies = append(cookies, cookie)
	}
	return codexOAILBValue(cookies)
}

func (s *OpenAIGatewayService) observeCodexTicketResponse(request *http.Request, response *http.Response, account *Account) {
	s.observeCodexTicketHTTPResponse(snapshotCodexTicketHTTPRequest(request, account), response)
}

func (s *OpenAIGatewayService) observeCodexTicketHTTPResponse(snapshot *codexTicketRequestSnapshot, response *http.Response) {
	if response != nil {
		s.observeCodexTicketHeaders(snapshot, response.StatusCode, response.Header)
	}
}

// The pool invokes this callback only after a real dial, including failed
// handshakes. Reusing a connection must not replay its historical response.
func (s *OpenAIGatewayService) codexTicketHandshakeObserver(ctx context.Context, ticket *openAICodexTicket) func(http.Header, int, http.Header) {
	if ticket == nil {
		return nil
	}
	generation := *ticket
	kind := "user_request"
	if isCodexTicketDiagnostic(ctx) {
		kind = "diagnostic"
	}
	return func(sent http.Header, status int, returned http.Header) {
		s.observeCodexTicketHeaders(snapshotCodexTicketRequest(&generation, sent, kind, "/v1/responses"), status, returned)
	}
}

func (s *OpenAIGatewayService) observeCodexTicketHeaders(snapshot *codexTicketRequestSnapshot, status int, headers http.Header) {
	if snapshot == nil || s.openaiCodexTicketLifecycle == nil {
		return
	}
	returned := strings.TrimSpace(extractOpenAICodexTurnState(headers))
	if snapshot.state == "" || returned == "" || returned == snapshot.state {
		return
	}
	cookies, err := http.ParseCookie(snapshot.cookie)
	if err != nil {
		return
	}
	originalOAILB, originalOK := codexOAILBValue(cookies)
	returnedOAILB, returnedOK := codexResponseOAILB(headers)
	if !originalOK || !returnedOK || originalOAILB == returnedOAILB {
		return
	}
	event := &CodexTicketInvalidation{
		AccountID: snapshot.accountID, Model: snapshot.model, TicketGenerationID: snapshot.generationID,
		OccurredAt: time.Now().UTC(), ReasonCode: CodexTicketInvalidationCredentialsChanged,
		ResponseHTTPStatus: &status, RequestKind: snapshot.kind, RequestRoute: snapshot.route,
		OriginalTicket: optionalCodexCredential(snapshot.state), OriginalCookie: optionalCodexCredential(snapshot.cookie),
		ReturnedTicket: returned, ReturnedCookie: optionalCodexCredential(extractOpenAICodexResponseCookies(headers)),
		ReturnedSetCookies: append([]string(nil), headers.Values("Set-Cookie")...),
	}
	key := openAICodexTicketKey(snapshot.accountID, snapshot.model)
	retry, hadRetry := s.openaiCodexTicketNextAttempt.Load(key)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inserted, err := s.openaiCodexTicketLifecycle.Invalidate(ctx, event)
	if err != nil {
		logger.L().Error("codex ticket invalidation persist failed", zap.Int64("account_id", snapshot.accountID), zap.String("model", snapshot.model), zap.Error(err))
		return
	}
	if !inserted || !event.InvalidatedCurrent {
		return
	}
	// The database transaction already removed exactly this generation. Account
	// snapshots may be shared by other requests, so refresh rather than mutate them.
	newerCached := false
	if cached, ok := s.openaiCodexTickets.Load(key); ok {
		if current, ok := cached.(*openAICodexTicket); ok {
			newerCached = current.GenerationID != snapshot.generationID
			if !newerCached {
				s.openaiCodexTickets.CompareAndDelete(key, cached)
			}
		}
	}
	if hadRetry && !newerCached {
		s.openaiCodexTicketNextAttempt.CompareAndDelete(key, retry)
	}
	if refresher, ok := s.accountRepo.(interface{ RefreshSchedulerAccount(context.Context, int64) }); ok {
		refresher.RefreshSchedulerAccount(ctx, snapshot.accountID)
	}
	s.notifyOpenAICodexTicketHarvester()
}
