package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
)

const (
	CodexSyncPending     = "syncing"
	CodexSyncReady       = "ready"
	CodexSyncReauthorize = "needs_reauthorization"
	CodexSyncUnknown     = "outcome_unknown"
	CodexSyncDeleting    = "pending_delete"
	CodexSyncMissing     = "remote_missing"
)

// CodexGatewayState is the non-sensitive projection shared by DTOs and schedulers.
// Administrative status, business cooldowns and expiration remain on Account.
type CodexGatewayState struct {
	Profile          *codexgateway.Metadata      `json:"profile,omitempty"`
	BindingID        string                      `json:"binding_id"`
	OwnerAccountID   int64                       `json:"owner_account_id"`
	Revision         uint64                      `json:"revision"`
	Authentication   string                      `json:"authentication"`
	SyncState        string                      `json:"sync_state"`
	OperationID      string                      `json:"operation_id,omitempty"`
	Error            *codexgateway.ErrorSummary  `json:"error,omitempty"`
	Snapshot         *codexgateway.AccountStatus `json:"snapshot,omitempty"`
	SyncedAt         *time.Time                  `json:"synced_at,omitempty"`
	ServiceAvailable *bool                       `json:"service_available,omitempty"`
}

func (s *CodexGatewayState) Usable() bool {
	return s != nil && s.SyncState == CodexSyncReady && s.Snapshot != nil && s.BindingID == s.Snapshot.ID && s.Snapshot.CanAcceptRequests
}

// CodexAccountBinding is stored only in Sub4API's binding table. It never
// contains CredentialInput, upstream tokens or an authenticated proxy URL.
type CodexAccountBinding struct {
	Operations          map[string]CodexOperationReference `json:"operations,omitempty"`
	Profile             *codexgateway.Metadata             `json:"profile,omitempty"`
	MutationHistory     map[string]string                  `json:"mutation_history,omitempty"`
	AccountID           int64                              `json:"account_id"`
	GatewayID           string                             `json:"gateway_id"`
	Revision            uint64                             `json:"revision"`
	ConfigVersion       int64                              `json:"config_version"`
	CreationKey         string                             `json:"creation_key"`
	CreationFingerprint string                             `json:"creation_fingerprint"`
	OperationID         string                             `json:"operation_id"`
	OperationKind       string                             `json:"operation_kind"`
	OperationRevision   uint64                             `json:"operation_revision"`
	SyncState           string                             `json:"sync_state"`
	AppliedConfigHash   string                             `json:"applied_config_hash"`
	OperationConfigHash string                             `json:"operation_config_hash"`
	Error               *codexgateway.ErrorSummary         `json:"error,omitempty"`
	Snapshot            *codexgateway.AccountStatus        `json:"snapshot,omitempty"`
	SyncedAt            *time.Time                         `json:"synced_at,omitempty"`
	DeleteRequested     bool                               `json:"delete_requested"`
}

func (b *CodexAccountBinding) Projection() *CodexGatewayState {
	if b == nil {
		return nil
	}
	mode := ""
	if b.Snapshot != nil {
		mode = b.Snapshot.Credential.Mode
	}
	state := b.SyncState
	if b.DeleteRequested {
		state = CodexSyncDeleting
	}
	return &CodexGatewayState{Profile: b.Profile, BindingID: b.GatewayID, OwnerAccountID: b.AccountID, Revision: b.Revision, Authentication: mode, SyncState: state, OperationID: b.OperationID, Error: b.Error, Snapshot: b.Snapshot, SyncedAt: b.SyncedAt}
}

// CodexBindingRepository coordinates local records without accessing Gateway tables.
// The same account lock is used by interactive writes and reconciliation workers.
type CodexBindingRepository interface {
	GetCodexBinding(context.Context, int64) (*CodexAccountBinding, error)
	FindCodexBindingByCreationKey(context.Context, string) (*CodexAccountBinding, error)
	SaveCodexBinding(context.Context, *CodexAccountBinding, int64, string) error
	ListCodexBindingAccountIDs(context.Context) ([]int64, error)
	WithCodexBindingLock(context.Context, string, func(context.Context) error) error
}

func (a *Account) IsOpenAICodex() bool {
	return a != nil && a.Platform == PlatformOpenAICodex && a.Type == AccountTypeGateway
}

// ValidateCodexBusinessCredentials is an allowlist, including recursively checked
// header policy. Secret ownership cannot depend on a growing blacklist of tokens.
func ValidateCodexBusinessCredentials(credentials map[string]any) error {
	allowed := map[string]bool{"model_mapping": true, "compact_model_mapping": true, "models": true, "model_whitelist": true, "temp_unschedulable_enabled": true, "temp_unschedulable_rules": true, "header_overrides": true}
	for key, value := range credentials {
		if !allowed[key] {
			return errors.New("OpenAI Codex credentials may contain business configuration only")
		}
		if key == "header_overrides" {
			raw, err := json.Marshal(value)
			if err != nil {
				return errors.New("invalid header overrides")
			}
			var headers map[string]any
			if json.Unmarshal(raw, &headers) != nil {
				return errors.New("invalid header overrides")
			}
			for name := range headers {
				lower := strings.ToLower(strings.TrimSpace(name))
				if strings.HasPrefix(lower, "x-codex4server-") || strings.Contains(lower, "auth") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.Contains(lower, "cookie") || strings.Contains(lower, "api-key") || strings.Contains(lower, "api_key") {
					return errors.New("OpenAI Codex header overrides cannot contain authentication")
				}
			}
		}
	}
	return nil
}
