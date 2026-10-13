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

// CodexTicketInvalidationCredentialsChanged 保留历史记录的旧规则：turn-state 与 __oailb 同时变化。
const CodexTicketInvalidationCredentialsChanged = "upstream_turn_state_and_oailb_changed"

// CodexTicketInvalidationRoutingCookieChanged 表示 turn-state 与任一路由 Cookie 同时变化。
const CodexTicketInvalidationRoutingCookieChanged = "upstream_turn_state_and_routing_cookie_changed"

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

// accountID 是实际发出请求的账号：共享票被所有参与账号复用，失效事件记在观测到的账号上。
func snapshotCodexTicketRequest(ticket *openAICodexTicket, accountID int64, headers http.Header, kind, route string) *codexTicketRequestSnapshot {
	if ticket == nil || ticket.GenerationID == "" {
		return nil
	}
	state := strings.TrimSpace(extractOpenAICodexTurnState(headers))
	if state == "" || state != strings.TrimSpace(ticket.State) {
		return nil
	}
	cookie := strings.Join(headers.Values("Cookie"), "; ")
	sentCookies, sentOK := parseCodexTicketRoutingCookies(cookie)
	ticketCookies, ticketOK := parseCodexTicketRoutingCookies(ticket.Cookie)
	// A reused WS connection can replace the turn-state with its own historical
	// state. Only credentials actually sent for this generation can invalidate it.
	if !sentOK || !ticketOK || sentCookies != ticketCookies {
		return nil
	}
	if strings.Contains(route, "/responses") {
		route = "/v1/responses"
	}
	if strings.Contains(route, "/messages") {
		route = "/v1/messages"
	}
	return &codexTicketRequestSnapshot{
		accountID: accountID, model: ticket.Model, generationID: ticket.GenerationID,
		state: state, cookie: cookie, kind: kind, route: route,
	}
}

func snapshotCodexTicketHTTPRequest(request *http.Request, account *Account) *codexTicketRequestSnapshot {
	if request == nil || account == nil {
		return nil
	}
	ticket, _ := request.Context().Value(codexTicketRequestContextKey{}).(*openAICodexTicket)
	if ticket == nil {
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
	return snapshotCodexTicketRequest(ticket, account.ID, request.Header, kind, route)
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
func (s *OpenAIGatewayService) codexTicketHandshakeObserver(ctx context.Context, accountID int64, ticket *openAICodexTicket) func(http.Header, int, http.Header) {
	if ticket == nil {
		return nil
	}
	generation := *ticket
	kind := "user_request"
	if isCodexTicketDiagnostic(ctx) {
		kind = "diagnostic"
	}
	return func(sent http.Header, status int, returned http.Header) {
		s.observeCodexTicketHeaders(snapshotCodexTicketRequest(&generation, accountID, sent, kind, "/v1/responses"), status, returned)
	}
}

// observeCodexTicketHeaders 在 turn-state 变化且 __oailb 或 __cflb 至少一个变化时，
// 判定这一代票对该账号失效；其他账号照常使用，同时立即唤醒打票器。
func (s *OpenAIGatewayService) observeCodexTicketHeaders(snapshot *codexTicketRequestSnapshot, status int, headers http.Header) {
	if snapshot == nil || s.openaiCodexTicketLifecycle == nil {
		return
	}
	returned := strings.TrimSpace(extractOpenAICodexTurnState(headers))
	if snapshot.state == "" || returned == "" || returned == snapshot.state {
		return
	}
	original, ok := parseCodexTicketRoutingCookies(snapshot.cookie)
	if !ok {
		return
	}
	returnedOAILB, oailbOK := codexResponseCookieValue(headers, codexOAILBCookie)
	returnedCFLB, cflbOK := codexResponseCookieValue(headers, codexCFLBCookie)
	if !(oailbOK && returnedOAILB != original.oailb) && !(cflbOK && returnedCFLB != original.cflb) {
		return
	}
	event := &CodexTicketInvalidation{
		AccountID: snapshot.accountID, Model: snapshot.model, TicketGenerationID: snapshot.generationID,
		OccurredAt: time.Now().UTC(), ReasonCode: CodexTicketInvalidationRoutingCookieChanged,
		ResponseHTTPStatus: &status, RequestKind: snapshot.kind, RequestRoute: snapshot.route,
		OriginalTicket:     optionalCodexCredential(snapshot.state),
		OriginalCookie:     optionalCodexCredential(snapshot.cookie),
		ReturnedTicket:     optionalCodexCredential(returned),
		ReturnedCookie:     optionalCodexCredential(extractOpenAICodexResponseCookies(headers)),
		ReturnedSetCookies: append([]string(nil), headers.Values("Set-Cookie")...),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var inserted bool
	err := s.writeSharedCodexTicket(ctx, func(ctx context.Context) (bool, error) {
		var err error
		inserted, err = s.openaiCodexTicketLifecycle.Invalidate(ctx, event)
		// 只有新记录且命中当前这一代时共享状态才变化；重复上报与旧一代上报不重新发布。
		return err == nil && inserted && event.InvalidatedCurrent, err
	})
	if err != nil {
		logger.L().Error("codex ticket invalidation persist failed", zap.Int64("account_id", snapshot.accountID), zap.String("model", snapshot.model), zap.Error(err))
		return
	}
	if !inserted || !event.InvalidatedCurrent {
		return
	}
	s.clearCodexTicketRetries()
	s.notifyOpenAICodexTicketHarvester()
}
