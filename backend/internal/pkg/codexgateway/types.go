// Package codexgateway implements the service-key-protected Gateway v4 contract.
// Upstream credentials are accepted only as transient management inputs.
package codexgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

type CredentialInput struct {
	Type             string          `json:"type"`
	AuthJSON         json.RawMessage `json:"auth_json,omitempty"`
	AccessToken      string          `json:"access_token,omitempty"`
	RefreshToken     string          `json:"refresh_token,omitempty"`
	ChatGPTAccountID string          `json:"chatgpt_account_id,omitempty"`
	ChatGPTPlanType  string          `json:"chatgpt_plan_type,omitempty"`
	AgentRuntimeID   string          `json:"agent_runtime_id,omitempty"`
	AgentPrivateKey  string          `json:"agent_private_key,omitempty"`
	TaskID           string          `json:"task_id,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	Code             string          `json:"code,omitempty"`
	State            string          `json:"state,omitempty"`
}

// ParseCredentials rejects cross-mode fields as well as unknown fields. Its errors
// never include input bytes, since callers may persist an operation error summary.
func ParseCredentials(raw json.RawMessage) (*CredentialInput, error) {
	invalid := errors.New("invalid gateway_credentials")
	var input CredentialInput
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		return nil, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, invalid
	}
	allowed := map[string]bool{"type": true}
	var required []string
	switch input.Type {
	case "oauth_code":
		required = []string{"session_id", "code", "state"}
		allowed["chatgpt_account_id"] = true
	case "auth_json":
		required = []string{"auth_json"}
	case "tokens":
		required = []string{"access_token", "chatgpt_account_id"}
		allowed["refresh_token"] = true
		allowed["chatgpt_plan_type"] = true
	case "refresh_token":
		required = []string{"refresh_token"}
		allowed["chatgpt_account_id"] = true
	case "personal_access_token":
		required = []string{"access_token"}
	case "setup_token":
		required = []string{"access_token"}
		allowed["chatgpt_account_id"] = true
	case "agent_identity":
		required = []string{"agent_runtime_id", "agent_private_key"}
		allowed["task_id"] = true
		allowed["chatgpt_account_id"] = true
	default:
		return nil, invalid
	}
	for _, key := range required {
		allowed[key] = true
		if len(fields[key]) == 0 {
			return nil, invalid
		}
	}
	for key, value := range fields {
		if !allowed[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, invalid
		}
		if key == "auth_json" {
			var object map[string]any
			if json.Unmarshal(value, &object) != nil || object == nil {
				return nil, invalid
			}
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil || strings.TrimSpace(text) == "" {
			return nil, invalid
		}
	}
	return &input, nil
}

type Capabilities struct {
	SupportedCredentialTypes             []string `json:"supported_credential_types"`
	PreserveRefreshTokenSupported        bool     `json:"preserve_refresh_token_supported"`
	APIVersion                           int      `json:"api_version"`
	StorageMode                          string   `json:"storage_mode"`
	SharedPGLayoutSupported              bool     `json:"shared_pg_layout_supported"`
	PersistentAccountOperationsSupported bool     `json:"persistent_account_operations_supported"`
	MaxRequestBytes                      int64    `json:"max_request_bytes"`
}

type Metadata struct {
	Email                 *string `json:"email"`
	ChatGPTUserID         *string `json:"chatgpt_user_id"`
	PlanType              *string `json:"plan_type"`
	SubscriptionExpiresAt *string `json:"subscription_expires_at"`
}

type CredentialStatus struct {
	Mode                 string     `json:"mode"`
	AccessTokenExpiresAt *time.Time `json:"access_token_expires_at"`
	LastRefreshAt        *time.Time `json:"last_refresh_at"`
	NextRetryAt          *time.Time `json:"next_retry_at"`
	RefreshBlocked       bool       `json:"refresh_blocked"`
	RefreshRejected      bool       `json:"refresh_rejected"`
}

type ErrorSummary struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type AccountStatus struct {
	OAuthClientID     *string          `json:"oauth_client_id"`
	ID                string           `json:"id"`
	DisplayName       string           `json:"display_name"`
	Revision          uint64           `json:"revision"`
	ChatGPTAccountID  *string          `json:"chatgpt_account_id"`
	Metadata          Metadata         `json:"metadata"`
	Status            string           `json:"status"`
	CanAcceptRequests bool             `json:"can_accept_requests"`
	Credential        CredentialStatus `json:"credential"`
	LastError         *ErrorSummary    `json:"last_error"`
}

type Operation struct {
	OperationID      string        `json:"operation_id"`
	AccountID        string        `json:"account_id"`
	Kind             string        `json:"kind"`
	State            string        `json:"state"`
	ExpectedRevision uint64        `json:"expected_revision"`
	ResultRevision   *uint64       `json:"result_revision"`
	Error            *ErrorSummary `json:"error"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

type Proxy struct {
	Mode string `json:"mode"`
	URL  string `json:"url,omitempty"`
}

type PutAccount struct {
	OAuthClientID        string           `json:"oauth_client_id,omitempty"`
	PreserveRefreshToken bool             `json:"preserve_refresh_token,omitempty"`
	DisplayName          string           `json:"display_name"`
	ExpectedRevision     uint64           `json:"expected_revision"`
	Credentials          *CredentialInput `json:"credentials"`
	OutboundProxy        Proxy            `json:"outbound_proxy"`
}

// PutOptions are transient management intentions, never persisted credentials.
type PutOptions struct {
	OAuthClientID        string
	PreserveRefreshToken bool
}

type OAuthSessionInput struct {
	OAuthClientID string `json:"oauth_client_id,omitempty"`
	RedirectURI   string `json:"redirect_uri,omitempty"`
	OutboundProxy Proxy  `json:"outbound_proxy"`
}

type OAuthSession struct {
	SessionID string    `json:"session_id"`
	AuthURL   string    `json:"auth_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type PatchAccount struct {
	ExpectedRevision uint64 `json:"expected_revision"`
	DisplayName      string `json:"display_name"`
	OutboundProxy    Proxy  `json:"outbound_proxy"`
}

type Identity struct {
	Version            int    `json:"version"`
	InstallationID     string `json:"installation_id"`
	SessionID          string `json:"session_id"`
	ThreadID           string `json:"thread_id"`
	PromptCacheKey     string `json:"prompt_cache_key,omitempty"`
	TurnID             string `json:"turn_id,omitempty"`
	WindowID           string `json:"window_id,omitempty"`
	ContextWindowID    string `json:"context_window_id,omitempty"`
	ParentThreadID     string `json:"parent_thread_id,omitempty"`
	ForkedFromThreadID string `json:"forked_from_thread_id,omitempty"`
	ParentTurnID       string `json:"parent_turn_id,omitempty"`
	RootTurnID         string `json:"root_turn_id,omitempty"`
	Traceparent        string `json:"traceparent,omitempty"`
	Tracestate         string `json:"tracestate,omitempty"`
}

type Transport struct {
	ProxyURL         string  `json:"proxy_url,omitempty"`
	CookieHeader     *string `json:"cookie_header,omitempty"`
	CaptureSetCookie bool    `json:"capture_set_cookie"`
}
