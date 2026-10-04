package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	infraerrors "github.com/MACOS-DO/sub4api/internal/pkg/errors"
	"github.com/MACOS-DO/sub4api/internal/pkg/openai"
	"github.com/google/uuid"
)

type CodexOAuthSessionStore interface {
	Set(context.Context, string, any) error
	Get(context.Context, string, any) (bool, error)
	Delete(context.Context, string) error
}

type codexActorKey struct{}

func WithCodexGatewayActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, codexActorKey{}, actor)
}

type CodexOAuthSessionRequest struct {
	Purpose       string `json:"purpose"`
	AccountID     int64  `json:"account_id,omitempty"`
	ProxyID       *int64 `json:"proxy_id,omitempty"`
	OAuthClientID string `json:"oauth_client_id,omitempty"`
	RedirectURI   string `json:"redirect_uri,omitempty"`
}

// Only ownership and non-secret configuration digests live in shared Redis.
type codexOAuthSessionBinding struct {
	Actor     string    `json:"actor"`
	AccountID int64     `json:"account_id"`
	Purpose   string    `json:"purpose"`
	ClientID  string    `json:"client_id"`
	ProxyHash string    `json:"proxy_hash"`
	ExpiresAt time.Time `json:"expires_at"`
}

func codexDigest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
func codexProxyDigest(proxy codexgateway.Proxy) string {
	raw, _ := json.Marshal(proxy)
	return codexDigest(string(raw))
}

func (s *adminServiceImpl) CodexGatewayCapabilities(ctx context.Context) (*codexgateway.Capabilities, error) {
	if s.codexGateway == nil {
		return nil, codexgateway.Unavailable()
	}
	if err := s.codexGateway.Check(ctx); err != nil {
		return nil, err
	}
	result := s.codexGateway.client.Capabilities()
	return &result, nil
}

func (s *adminServiceImpl) CreateCodexOAuthSession(ctx context.Context, actor string, input CodexOAuthSessionRequest) (*codexgateway.OAuthSession, error) {
	capabilities, err := s.CodexGatewayCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(capabilities.SupportedCredentialTypes, "oauth_code") {
		return nil, infraerrors.BadRequest("CODEX_OAUTH_UNSUPPORTED", "Gateway does not support browser authorization")
	}
	if actor == "" || actor == "admin:0" || s.codexGateway.sessions == nil {
		return nil, infraerrors.ServiceUnavailable("CODEX_SESSION_STORE_UNAVAILABLE", "Authorization session storage is unavailable")
	}
	if (input.Purpose != "create" && input.Purpose != "reauthorize") || (input.Purpose == "create" && input.AccountID != 0) || (input.Purpose == "reauthorize" && input.AccountID <= 0) {
		return nil, infraerrors.BadRequest("CODEX_SESSION_PURPOSE", "Invalid authorization purpose")
	}
	if input.Purpose == "reauthorize" {
		a, err := s.accountRepo.GetByID(ctx, input.AccountID)
		if err != nil {
			return nil, err
		}
		if !a.IsOpenAICodex() || a.IsShadow() {
			return nil, infraerrors.BadRequest("CODEX_PARENT_REQUIRED", "Reauthorize an OpenAI Codex parent account")
		}
	}
	proxy := codexgateway.Proxy{Mode: "direct"}
	if input.ProxyID != nil && *input.ProxyID > 0 {
		p, err := s.proxyRepo.GetByID(ctx, *input.ProxyID)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, infraerrors.BadRequest("CODEX_PROXY_MISSING", "Proxy not found")
		}
		proxy = codexgateway.Proxy{Mode: "proxy", URL: p.URL()}
	}
	client := strings.TrimSpace(input.OAuthClientID)
	if client == "" {
		client = openai.ClientID
	}
	result, err := s.codexGateway.client.CreateOAuthSession(ctx, codexgateway.OAuthSessionInput{OAuthClientID: client, RedirectURI: input.RedirectURI, OutboundProxy: proxy})
	if err != nil {
		return nil, err
	}
	binding := codexOAuthSessionBinding{Actor: actor, AccountID: input.AccountID, Purpose: input.Purpose, ClientID: client, ProxyHash: codexProxyDigest(proxy), ExpiresAt: result.ExpiresAt}
	if err := s.codexGateway.sessions.Set(ctx, codexDigest(result.SessionID), binding); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.codexGateway.client.CancelOAuthSession(cleanup, result.SessionID)
		return nil, infraerrors.ServiceUnavailable("CODEX_SESSION_STORE_UNAVAILABLE", "Authorization session storage is unavailable")
	}
	return result, nil
}

func (s *adminServiceImpl) CancelCodexOAuthSession(ctx context.Context, actor, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return infraerrors.BadRequest("CODEX_SESSION_INVALID", "Invalid authorization session")
	}
	if s.codexGateway == nil || s.codexGateway.sessions == nil || s.codexGateway.client == nil {
		return codexgateway.Unavailable()
	}
	var binding codexOAuthSessionBinding
	ok, err := s.codexGateway.sessions.Get(ctx, codexDigest(id), &binding)
	if err != nil {
		return infraerrors.ServiceUnavailable("CODEX_SESSION_STORE_UNAVAILABLE", "Authorization session storage is unavailable")
	}
	if !ok {
		return nil
	}
	if actor == "" || binding.Actor != actor {
		return infraerrors.Forbidden("CODEX_SESSION_OWNER", "Authorization session belongs to another administrator")
	}
	if err := s.codexGateway.client.CancelOAuthSession(ctx, id); err != nil {
		return err
	}
	return s.codexGateway.sessions.Delete(ctx, codexDigest(id))
}

func (s *CodexGatewayService) validateCredentialWrite(ctx context.Context, credentials *codexgateway.CredentialInput, options *codexgateway.PutOptions, account *Account, targetID int64) error {
	if credentials == nil {
		return nil
	}
	if options.PreserveRefreshToken && (credentials.Type != "tokens" || credentials.RefreshToken != "" || targetID == 0) {
		return infraerrors.BadRequest("CODEX_PRESERVE_RT_INVALID", "Preserving a refresh token requires an AT-only update")
	}
	if credentials.Type != "oauth_code" && !options.PreserveRefreshToken {
		return nil
	}
	if err := s.Check(ctx); err != nil {
		return err
	}
	caps := s.client.Capabilities()
	if options.PreserveRefreshToken && !caps.PreserveRefreshTokenSupported {
		return infraerrors.BadRequest("CODEX_PRESERVE_RT_UNSUPPORTED", "Upgrade Gateway to preserve refresh tokens")
	}
	if credentials.Type != "oauth_code" {
		return nil
	}
	if !slices.Contains(caps.SupportedCredentialTypes, "oauth_code") {
		return infraerrors.BadRequest("CODEX_OAUTH_UNSUPPORTED", "Upgrade Gateway to use browser authorization")
	}
	if s.sessions == nil {
		return codexgateway.Unavailable()
	}
	var binding codexOAuthSessionBinding
	ok, err := s.sessions.Get(ctx, codexDigest(credentials.SessionID), &binding)
	if err != nil {
		return infraerrors.ServiceUnavailable("CODEX_SESSION_STORE_UNAVAILABLE", "Authorization session storage is unavailable")
	}
	actor, _ := ctx.Value(codexActorKey{}).(string)
	if !ok || actor == "" || actor != binding.Actor || binding.AccountID != targetID {
		return infraerrors.BadRequest("CODEX_SESSION_INVALID", "Authorization session is unavailable or belongs to another request")
	}
	if !binding.ExpiresAt.After(time.Now()) {
		return infraerrors.BadRequest("CODEX_SESSION_EXPIRED", "Authorization session expired; authorize again")
	}
	if binding.ProxyHash != codexProxyDigest(codexDesiredProxy(account)) || (options.OAuthClientID != "" && options.OAuthClientID != binding.ClientID) {
		return infraerrors.Conflict("CODEX_SESSION_PARAMETERS_CHANGED", "Proxy or OAuth client changed; authorize again")
	}
	options.OAuthClientID = binding.ClientID
	return nil
}

type CodexOperationReference struct {
	OperationID string                     `json:"operation_id"`
	Revision    uint64                     `json:"revision"`
	Kind        string                     `json:"kind"`
	State       string                     `json:"state"`
	Index       int                        `json:"index"`
	Error       *codexgateway.ErrorSummary `json:"error,omitempty"`
}

func (b *CodexAccountBinding) rememberOperation(key, lookup string, index int) {
	if lookup == "" {
		lookup = key
	}
	if b.Operations == nil {
		b.Operations = map[string]CodexOperationReference{}
	}
	b.Operations[codexDigest(lookup)] = CodexOperationReference{OperationID: b.OperationID, Revision: b.OperationRevision, Kind: b.OperationKind, State: "pending", Index: index}
}

type CodexOperationItem struct {
	CodexOperationReference
	AccountID int64 `json:"account_id"`
}
type CodexOperationResult struct {
	Items []CodexOperationItem `json:"items"`
}

func (s *adminServiceImpl) GetCodexGatewayOperation(ctx context.Context, actor, key string) (*CodexOperationResult, error) {
	if actor == "" || actor == "admin:0" {
		return nil, infraerrors.Forbidden("CODEX_OPERATION_OWNER", "Administrator identity required")
	}
	if _, err := uuid.Parse(key); err != nil {
		return nil, infraerrors.BadRequest("CODEX_OPERATION_KEY", "Invalid operation key")
	}
	repo, ok := s.accountRepo.(interface {
		FindCodexBindingsByOperation(context.Context, string) ([]*CodexAccountBinding, error)
	})
	if !ok || s.codexGateway == nil || s.codexGateway.client == nil {
		return nil, codexgateway.Unavailable()
	}
	hash := codexDigest(actor + ":" + key)
	bindings, err := repo.FindCodexBindingsByOperation(ctx, hash)
	if err != nil {
		return nil, err
	}
	result := &CodexOperationResult{Items: []CodexOperationItem{}}
	for _, binding := range bindings {
		ref := binding.Operations[hash]
		if ref.State == "pending" || ref.State == "indeterminate" {
			op, err := s.codexGateway.client.GetOperation(ctx, ref.OperationID)
			if err == nil && op.AccountID == binding.GatewayID && op.Kind == ref.Kind && op.ExpectedRevision == ref.Revision {
				ref.State, ref.Error = op.State, op.Error
			} else if err != nil {
				ref.Error = &codexgateway.ErrorSummary{Code: "CODEX_OPERATION_UNCONFIRMED", Message: "Query the original operation again; do not resubmit credentials"}
			}
		}
		result.Items = append(result.Items, CodexOperationItem{CodexOperationReference: ref, AccountID: binding.AccountID})
	}
	if len(result.Items) == 0 {
		return nil, infraerrors.NotFound("CODEX_OPERATION_UNCONFIRMED", "Operation not found; this does not prove credentials were not exchanged")
	}
	return result, nil
}
