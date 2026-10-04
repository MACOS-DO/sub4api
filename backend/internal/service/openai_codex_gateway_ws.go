package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	openaiwsv2 "github.com/MACOS-DO/sub4api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// A Gateway socket is dedicated to one downstream connection. Only identity
// metadata is kept between turns; this adapter never reconstructs conversation history.
type codexGatewayFrameConn struct {
	service        *OpenAIGatewayService
	sideband       bool
	conn           *coderws.Conn
	account        *Account
	apiKeyID       int64
	identity       codexgateway.Identity
	headers        http.Header
	rawIdentity    map[string]string
	turnState      string
	handshakeModel string
	proxyURL       string
	ticketAccount  *Account
	first          bool
}

var _ openAIWSClientConn = (*codexGatewayFrameConn)(nil)
var _ openaiwsv2.FrameConn = (*codexGatewayFrameConn)(nil)

func (c *codexGatewayFrameConn) WriteJSON(ctx context.Context, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, raw)
}
func (c *codexGatewayFrameConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, body, err := c.ReadFrame(ctx)
	return body, err
}
func (c *codexGatewayFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	return c.conn.Read(ctx)
}
func (c *codexGatewayFrameConn) Ping(ctx context.Context) error { return c.conn.Ping(ctx) }
func (c *codexGatewayFrameConn) Close() error {
	defer c.conn.CloseNow()
	return c.conn.Close(coderws.StatusNormalClosure, "")
}

func (c *codexGatewayFrameConn) WriteFrame(ctx context.Context, kind coderws.MessageType, body []byte) error {
	if c.sideband || kind != coderws.MessageText {
		return c.conn.Write(ctx, kind, body)
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return errors.New("invalid WebSocket frame")
	}
	identity := c.identity
	if envelope.Type == "response.create" {
		if err := c.validateModelTicket(ctx, gjson.GetBytes(body, "model").String()); err != nil {
			return err
		}
		incoming := codexGatewayIdentityInput(nil, body)
		for _, field := range []string{"installation_id", "session_id", "thread_id"} {
			if raw := incoming[field]; raw != "" && c.rawIdentity[field] != "" && raw != c.rawIdentity[field] {
				return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "Identity changed; open a new connection", nil)
			}
		}
		if c.first {
			c.first = false
		} else {
			var err error
			identity, err = c.service.resolveGatewayIdentity(ctx, c.account, c.apiKeyID, c.headers, body)
			if err != nil {
				return err
			}
			identity.InstallationID = c.identity.InstallationID
			identity.SessionID = c.identity.SessionID
			identity.ThreadID = c.identity.ThreadID
		}
	}
	prepared, err := codexGatewayWSMessage(body, identity, c.turnState)
	if err != nil {
		return err
	}
	return c.conn.Write(ctx, kind, prepared)
}

// A different model may use the existing handshake only if it does not need
// a different ticket, Cookie or proxy. Harvesting a newer ticket for the same
// model never changes an already established connection.
func (c *codexGatewayFrameConn) validateModelTicket(ctx context.Context, model string) error {
	if model == "" || model == c.handshakeModel {
		return nil
	}
	headers := make(http.Header)
	ticket, err := c.service.applyOpenAICodexTicketWithGeneration(ctx, c.ticketAccount, model, headers)
	if err != nil {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "Model requires another ticket; open a new connection", err)
	}
	if ticket == nil {
		return nil
	}
	proxy, err := c.service.codexGatewayTicketProxy(ctx, c.ticketAccount, ticket)
	if err != nil || headers.Get(openAIWSTurnStateHeader) != c.turnState || headers.Get("Cookie") != c.headers.Get("Cookie") || proxy != c.proxyURL {
		return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "Model requires another ticket; open a new connection", err)
	}
	return nil
}

func (s *OpenAIGatewayService) dialCodexGatewayWebSocket(ctx context.Context, c *gin.Context, account *Account, headers http.Header, body []byte, ticket *openAICodexTicket) (openAIWSClientConn, int, http.Header, error) {
	if s.codexGateway == nil || !s.codexGateway.Ready() || account.Gateway == nil || !account.Gateway.Usable() {
		return nil, 0, nil, codexgateway.Unavailable()
	}
	source := codexAccountIdentitySource(c, account)
	snapshot := *source
	snapshot.Extra = maps.Clone(source.Extra)
	headers = headers.Clone()
	raw := codexGatewayIdentityInput(headers, body)
	if raw["session_id"] == "" {
		raw["session_id"] = raw["thread_id"]
		if raw["session_id"] == "" {
			raw["session_id"] = raw["prompt_cache_key"]
		}
		if raw["session_id"] == "" {
			raw["session_id"] = uuid.NewString()
		}
	}
	if raw["thread_id"] == "" {
		raw["thread_id"] = raw["session_id"]
	}
	headers.Set("session-id", raw["session_id"])
	headers.Set("thread-id", raw["thread_id"])
	identity, err := s.resolveGatewayIdentity(ctx, &snapshot, getAPIKeyIDFromContext(c), headers, body)
	if err != nil {
		return nil, 0, nil, err
	}
	transport := &codexgateway.Transport{CaptureSetCookie: true}
	transport.ProxyURL, err = s.codexGatewayTicketProxy(ctx, account, ticket)
	if err != nil {
		return nil, 0, nil, err
	}
	if cookie := headers.Get("Cookie"); cookie != "" {
		transport.CookieHeader = &cookie
	}
	conn, response, err := s.codexGateway.client.Dial(ctx, account.Gateway.BindingID, "/v1/responses", headers, identity, account.OpenAIRequestTimezone(), transport)
	status := 0
	responseHeaders := make(http.Header)
	if response != nil {
		status = response.StatusCode
		if decodeErr := codexgateway.DecodeResponse(response); decodeErr != nil {
			if conn != nil {
				_ = conn.CloseNow()
			}
			return nil, status, response.Header, decodeErr
		}
		responseHeaders = response.Header
	}
	if err != nil {
		var body []byte
		if response != nil && response.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
		}
		return nil, status, responseHeaders, &openAIWSHandshakeError{Body: body, Err: err}
	}
	frameHeaders := headers.Clone()
	for field, aliases := range codexGatewayIdentityAliases {
		if field == "installation_id" || field == "session_id" || field == "thread_id" {
			continue
		}
		for _, alias := range aliases {
			frameHeaders.Del(alias)
		}
	}
	frameHeaders.Del("x-codex-turn-metadata")
	return &codexGatewayFrameConn{service: s, conn: conn, account: &snapshot, apiKeyID: getAPIKeyIDFromContext(c), identity: identity, headers: frameHeaders, rawIdentity: raw, turnState: headers.Get(openAIWSTurnStateHeader), handshakeModel: gjson.GetBytes(body, "model").String(), proxyURL: transport.ProxyURL, ticketAccount: account, first: true}, status, responseHeaders, nil
}

func (s *OpenAIGatewayService) proxyCodexGatewayWebSocket(ctx context.Context, c *gin.Context, client *coderws.Conn, account *Account, first []byte, hooks *OpenAIWSIngressHooks) error {
	err := s.proxyResponsesWebSocketV2Passthrough(ctx, c, client, account, "", first, hooks, OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2, Reason: "codex_gateway_dedicated"})
	if err == nil {
		return nil
	}
	var clientClose *OpenAIWSClientCloseError
	if errors.As(err, &clientClose) {
		// Strip nested causes so an upstream failover can never escape this adapter.
		return NewOpenAIWSClientCloseError(clientClose.StatusCode(), clientClose.Reason(), nil)
	}
	// Do not expose a nested failover error to the handler: it would replay a
	// turn on another account after an established Gateway connection failed.
	return NewOpenAIWSClientCloseError(coderws.StatusInternalError, "Gateway connection ended; open a new connection", errors.New(strings.TrimSpace(err.Error())))
}
