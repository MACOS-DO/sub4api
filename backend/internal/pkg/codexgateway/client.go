package codexgateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/MACOS-DO/sub4api/internal/pkg/errors"
	"github.com/coder/websocket"
)

type Config struct {
	Enabled                bool
	AutoGenerateServiceKey bool
	BaseURL                string
	ServiceKeyFile         string
	AdminTimeout           time.Duration
	MaxRequestBytes        int64
}

type Client struct {
	base         *url.URL
	key          string
	admin        *http.Client
	data         *http.Client
	maxBytes     int64
	mu           sync.RWMutex
	capabilities Capabilities
	ready        bool
}

// Error contains only protocol metadata, never management request bodies or keys.
type Error struct {
	Status            int
	Origin            string
	Code              string
	Message           string
	UpstreamRequestID string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func (e *Error) Unwrap() error { return infraerrors.New(e.Status, e.Code, e.Message) }

func Unavailable() error {
	return &Error{Status: 503, Origin: "gateway", Code: "codex_gateway_unavailable", Message: "Gateway service unavailable"}
}

func New(cfg Config) (*Client, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("invalid Gateway base URL")
	}
	if cfg.AutoGenerateServiceKey {
		if err := EnsureServiceKeyFile(cfg.ServiceKeyFile); err != nil {
			return nil, err
		}
	}
	secret, err := readServiceKey(cfg.ServiceKeyFile)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 0
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if cfg.AdminTimeout <= 0 {
		cfg.AdminTimeout = 60 * time.Second
	}
	return &Client{base: base, key: secret, maxBytes: cfg.MaxRequestBytes,
		admin: &http.Client{Transport: transport, Timeout: cfg.AdminTimeout, CheckRedirect: noRedirect},
		data:  &http.Client{Transport: transport, CheckRedirect: noRedirect}}, nil
}

func (c *Client) Ready() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ready
}

func (c *Client) Check(ctx context.Context) error {
	if c == nil {
		return Unavailable()
	}
	var result Capabilities
	err := c.get(ctx, "/internal/capabilities", &result)
	if err == nil && (result.APIVersion != 4 || result.StorageMode != "postgres" || !result.SharedPGLayoutSupported || !result.PersistentAccountOperationsSupported || result.MaxRequestBytes < c.maxBytes) {
		err = &Error{Status: 503, Origin: "gateway", Code: "codex_gateway_incompatible", Message: "Gateway v4 PostgreSQL capabilities or request limit do not match"}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.mu.Lock()
	c.ready = err == nil
	c.capabilities = result
	c.mu.Unlock()
	return err
}

func (c *Client) GetAccount(ctx context.Context, id string) (*AccountStatus, error) {
	var result AccountStatus
	err := c.get(ctx, "/internal/accounts/"+url.PathEscape(id), &result)
	if err == nil && (result.ID != id || result.Revision == 0) {
		return nil, Unavailable()
	}
	return &result, err
}

func (c *Client) GetOperation(ctx context.Context, id string) (*Operation, error) {
	var result Operation
	err := c.get(ctx, "/internal/account-operations/"+url.PathEscape(id), &result)
	if err == nil && result.OperationID != id {
		return nil, Unavailable()
	}
	return &result, err
}

// Mutate deliberately performs one attempt. Timeout recovery must query the
// durable receipt; it must never transparently resubmit a rotating credential.
func (c *Client) Mutate(ctx context.Context, id, kind, key string, revision uint64, input any) (*Operation, error) {
	if c == nil {
		return nil, Unavailable()
	}
	method, path := http.MethodPut, "/internal/accounts/"+url.PathEscape(id)
	switch kind {
	case "put":
	case "patch":
		method = http.MethodPatch
	case "delete":
		method = http.MethodDelete
		path += "?expected_revision=" + strconv.FormatUint(revision, 10)
	case "refresh":
		method = http.MethodPost
		path += "/refresh"
	default:
		return nil, errors.New("invalid Gateway operation kind")
	}
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return nil, errors.New("cannot encode Gateway operation")
		}
	}
	resp, err := c.call(ctx, c.admin, method, path, bytes.NewReader(body), http.Header{"Idempotency-Key": {key}, "Content-Type": {"application/json"}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, Unavailable()
	}
	var result Operation
	if json.Unmarshal(raw, &result) == nil && result.OperationID != "" {
		if result.OperationID != key || result.AccountID != id || result.Kind != kind || result.ExpectedRevision != revision {
			return nil, Unavailable()
		}
		switch result.State {
		case "pending", "succeeded", "failed", "indeterminate":
			return &result, nil
		}
		return nil, Unavailable()
	}
	return nil, responseError(resp, raw)
}

func (c *Client) get(ctx context.Context, path string, result any) error {
	if c == nil {
		return Unavailable()
	}
	resp, err := c.call(ctx, c.admin, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return Unavailable()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp, raw)
	}
	if json.Unmarshal(raw, result) != nil {
		return Unavailable()
	}
	return nil
}

func responseError(resp *http.Response, body []byte) error {
	origin := resp.Header.Get("X-Codex4Server-Error-Origin")
	if origin != "account" && origin != "upstream" {
		origin = "gateway"
	}
	var value struct {
		Error ErrorSummary `json:"error"`
	}
	_ = json.Unmarshal(body, &value)
	code := value.Error.Code
	if code == "" || len(code) > 128 {
		code = "codex_gateway_error"
	}
	// All body details remain private to Gateway; these fixed summaries are safe
	// for bindings, cached operation results and administrator-facing errors.
	status := resp.StatusCode
	if origin == "gateway" && status >= 500 {
		status = http.StatusServiceUnavailable
	}
	// Gateway/issuer authentication is not the administrator's Sub4API login.
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		status = http.StatusBadGateway
		if origin == "gateway" {
			status = http.StatusServiceUnavailable
		}
	}
	return &Error{Status: status, Origin: origin, Code: code, Message: "Gateway operation failed", UpstreamRequestID: resp.Header.Get("X-Codex4Server-Upstream-Request-Id")}
}

func (c *Client) call(ctx context.Context, client *http.Client, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	if c == nil {
		return nil, Unavailable()
	}
	rel, err := url.Parse(path)
	if err != nil || rel.IsAbs() || rel.Host != "" || !strings.HasPrefix(path, "/") {
		return nil, Unavailable()
	}
	endpoint := c.base.ResolveReference(rel)
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, Unavailable()
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := client.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		c.mu.Lock()
		c.ready = false
		c.mu.Unlock()
		return nil, Unavailable()
	}
	return resp, nil
}

// Forward uses the streaming client with cancellation inherited from ctx.
func (c *Client) Forward(ctx context.Context, accountID, method, path string, body io.Reader, headers http.Header, identity Identity, timezone string, transport *Transport) (*http.Response, error) {
	if !c.Ready() {
		return nil, Unavailable()
	}
	prepared, err := c.headers(accountID, headers, identity, timezone, transport)
	if err != nil {
		return nil, err
	}
	return c.call(ctx, c.data, method, path, body, prepared)
}

func (c *Client) NewRequest(ctx context.Context, id, method, path string, body io.Reader, headers http.Header, identity Identity, timezone string, transport *Transport) (*http.Request, error) {
	if !c.Ready() {
		return nil, Unavailable()
	}
	prepared, err := c.headers(id, headers, identity, timezone, transport)
	if err != nil {
		return nil, err
	}
	rel, err := url.Parse(path)
	if err != nil || rel.IsAbs() || rel.Host != "" || !strings.HasPrefix(path, "/") {
		return nil, Unavailable()
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.ResolveReference(rel).String(), body)
	if err != nil {
		return nil, Unavailable()
	}
	req.Header = prepared
	req.Header.Set("Authorization", "Bearer "+c.key)
	return req, nil
}

func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if !c.Ready() {
		return nil, Unavailable()
	}
	if req.URL.Scheme != c.base.Scheme || req.URL.Host != c.base.Host {
		return nil, Unavailable()
	}
	resp, err := c.data.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		c.mu.Lock()
		c.ready = false
		c.mu.Unlock()
		return nil, Unavailable()
	}
	if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && resp.Header.Get("X-Codex4Server-Error-Origin") == "gateway" {
		c.mu.Lock()
		c.ready = false
		c.mu.Unlock()
	}
	if err = DecodeResponse(resp); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

func (c *Client) Auxiliary(ctx context.Context, id, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	if !c.Ready() {
		return nil, Unavailable()
	}
	return c.call(ctx, c.admin, method, "/internal/accounts/"+url.PathEscape(id)+"/"+path, body, cleanHeaders(headers))
}

func (c *Client) Dial(ctx context.Context, id, path string, headers http.Header, identity Identity, timezone string, transport *Transport) (*websocket.Conn, *http.Response, error) {
	if !c.Ready() {
		return nil, nil, Unavailable()
	}
	prepared, err := c.headers(id, headers, identity, timezone, transport)
	if err != nil {
		return nil, nil, err
	}
	prepared.Set("Authorization", "Bearer "+c.key)
	rel, err := url.Parse(path)
	if err != nil || rel.IsAbs() || rel.Host != "" || !strings.HasPrefix(path, "/") {
		return nil, nil, Unavailable()
	}
	u := c.base.ResolveReference(rel)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	conn, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: c.data, HTTPHeader: prepared, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, resp, Unavailable()
	}
	if c.maxBytes > 0 {
		conn.SetReadLimit(c.maxBytes)
	}
	return conn, resp, nil
}

func (c *Client) headers(id string, input http.Header, identity Identity, timezone string, transport *Transport) (http.Header, error) {
	if id == "" || identity.Version != 1 || identity.InstallationID == "" || identity.SessionID == "" || identity.ThreadID == "" {
		return nil, errors.New("incomplete Gateway identity")
	}
	output := cleanHeaders(input)
	output.Set("X-Codex4Server-Account-Id", id)
	if identity.TurnID != "" {
		output.Set("X-Codex-Turn-Id", identity.TurnID)
	}
	output.Set("X-Codex4Server-Timezone", timezone)
	raw, err := json.Marshal(identity)
	if err != nil {
		return nil, Unavailable()
	}
	output.Set("X-Codex4Server-Identity", base64.RawURLEncoding.EncodeToString(raw))
	if transport != nil {
		raw, err = json.Marshal(transport)
		if err != nil {
			return nil, Unavailable()
		}
		output.Set("X-Codex4Server-Transport", base64.RawURLEncoding.EncodeToString(raw))
	}
	return output, nil
}

func cleanHeaders(input http.Header) http.Header {
	h := input.Clone()
	if h == nil {
		h = make(http.Header)
	}
	for key := range h {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "x-codex4server-") || strings.HasPrefix(lower, "sec-websocket-") || lower == "authorization" || lower == "proxy-authorization" || lower == "cookie" || lower == "host" || lower == "content-length" || lower == "x-api-key" {
			delete(h, key)
		}
	}
	return h
}

// DecodeResponse restores issuer metadata only into the internal response.
// The public response layer must still strip all X-Codex4Server-* headers.
func DecodeResponse(resp *http.Response) error {
	if resp == nil {
		return Unavailable()
	}
	if encoded := resp.Header.Get("X-Codex4Server-Upstream-Set-Cookie"); encoded != "" {
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return errors.New("invalid Gateway cookie metadata")
		}
		var cookies []string
		if json.Unmarshal(raw, &cookies) != nil {
			return errors.New("invalid Gateway cookie metadata")
		}
		for _, cookie := range cookies {
			if strings.ContainsAny(cookie, "\r\n") {
				return errors.New("invalid Gateway cookie metadata")
			}
			resp.Header.Add("Set-Cookie", cookie)
		}
	}
	if id := resp.Header.Get("X-Codex4Server-Upstream-Request-Id"); id != "" {
		resp.Header.Set("X-Request-Id", id)
	}
	return nil
}

func StripControlHeaders(headers http.Header) {
	for key := range headers {
		if strings.HasPrefix(strings.ToLower(key), "x-codex4server-") {
			delete(headers, key)
		}
	}
}

func OperationError(operation *Operation) error {
	if operation == nil {
		return Unavailable()
	}
	if operation.Error == nil {
		return fmt.Errorf("Gateway operation %s", operation.State)
	}
	return &Error{Status: 409, Origin: "account", Code: operation.Error.Code, Message: "Gateway account operation requires attention"}
}

// ManagementResponseError never exposes an upstream 401 as a Sub4API login failure.
// Public data forwarding continues to preserve the original http.Response.
func ManagementResponseError(response *http.Response, body []byte) error {
	return responseError(response, body)
}
