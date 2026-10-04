package codexgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Missing v4 capability fields retain the original six modes. An explicit empty
// list is authoritative; copy before returning so callers cannot mutate readiness.
func (c *Client) Capabilities() Capabilities {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := c.capabilities
	result.SupportedCredentialTypes = slices.Clone(result.SupportedCredentialTypes)
	if result.SupportedCredentialTypes == nil {
		result.SupportedCredentialTypes = []string{"auth_json", "tokens", "refresh_token", "personal_access_token", "setup_token", "agent_identity"}
	}
	return result
}

func (c *Client) CreateOAuthSession(ctx context.Context, input OAuthSessionInput) (*OAuthSession, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, Unavailable()
	}
	resp, err := c.call(ctx, c.admin, http.MethodPost, "/internal/oauth/sessions", bytes.NewReader(raw), http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, Unavailable()
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, responseError(resp, body)
	}
	var result OAuthSession
	if json.Unmarshal(body, &result) != nil {
		return nil, Unavailable()
	}
	if _, err := uuid.Parse(result.SessionID); err != nil {
		return nil, Unavailable()
	}
	u, err := url.Parse(result.AuthURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || !result.ExpiresAt.After(time.Now()) {
		return nil, Unavailable()
	}
	return &result, nil
}

func (c *Client) CancelOAuthSession(ctx context.Context, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return Unavailable()
	}
	resp, err := c.call(ctx, c.admin, http.MethodDelete, "/internal/oauth/sessions/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Unavailable()
	}
	return responseError(resp, body)
}
