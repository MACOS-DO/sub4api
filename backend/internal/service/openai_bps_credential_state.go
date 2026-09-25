package service

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

const OpenAIBPSCredentialStateExtraKey = "openai_bps_credential_state"
const bpsCredentialRevokedMessage = "BPS access token revoked; replace access_token"
const bpsCredentialAuthFailedMessage = "BPS authentication failed; replace access_token"
const bpsCredentialExpiredMessage = "BPS access token expired; replace it manually"

// OpenAIBPSCredentialState is safe to return in both full and compact admin DTOs.
// not_expired describes only the JWT deadline, never successful authentication.
type OpenAIBPSCredentialState struct {
	Status               string     `json:"status"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	ErrorCode            string     `json:"error_code,omitempty"`
	ObservedAt           *time.Time `json:"observed_at,omitempty"`
	RequiresManualResume bool       `json:"requires_manual_resume,omitempty"`
}

type bpsCredentialSnapshotContextKey struct{}

type OpenAIBPSCredentialSnapshot struct{ AccessToken, AccountID string }
type OpenAIBPSCredentialStateRepository interface {
	SetOpenAIBPSCredentialErrorIfMatch(context.Context, int64, OpenAIBPSCredentialSnapshot, OpenAIBPSCredentialState) (bool, error)
}

func OpenAIBPSCredentialSnapshotFromAccount(a *Account) OpenAIBPSCredentialSnapshot {
	if a == nil {
		return OpenAIBPSCredentialSnapshot{}
	}
	return OpenAIBPSCredentialSnapshot{a.GetCredential("access_token"), a.GetCredential("chatgpt_account_id")}
}
func OpenAIBPSCredentialIdentity(snapshot OpenAIBPSCredentialSnapshot) string {
	return bpsDigest(snapshot.AccessToken + "\x00" + snapshot.AccountID)
}
func bpsRequestCredentialSnapshot(ctx context.Context, account *Account) OpenAIBPSCredentialSnapshot {
	if snapshot, ok := ctx.Value(bpsCredentialSnapshotContextKey{}).(OpenAIBPSCredentialSnapshot); ok {
		return snapshot
	}
	return OpenAIBPSCredentialSnapshotFromAccount(account)
}

// Preserve the latest server-owned diagnosis while the account row is locked.
// Changing AT/workspace invalidates it; a stale form cannot erase a newer 401.
func MergeOpenAIBPSCredentialStateExtra(account *Account, desired, current map[string]any, identityUnchanged bool) map[string]any {
	if !account.IsOpenAIBPS() {
		return desired
	}
	delete(desired, OpenAIBPSCredentialStateExtraKey)
	saved, ok := current[OpenAIBPSCredentialStateExtraKey].(map[string]any)
	if !ok {
		return desired
	}
	identity := stringValue(saved["credential_identity"])
	if (identity != "" && identity != OpenAIBPSCredentialIdentity(OpenAIBPSCredentialSnapshotFromAccount(account))) || (identity == "" && !identityUnchanged) {
		return desired
	}
	if desired == nil {
		desired = map[string]any{}
	}
	desired[OpenAIBPSCredentialStateExtraKey] = saved
	managed, _ := saved["managed_status_error"].(bool)
	if managed && account.Status == StatusActive {
		account.Status = StatusError
		account.ErrorMessage = OpenAIBPSCredentialErrorMessage(OpenAIBPSCredentialState{Status: stringValue(saved["status"])})
	}
	return desired
}

func OpenAIBPSCredentialErrorMessage(state OpenAIBPSCredentialState) string {
	if state.Status == "revoked" {
		return bpsCredentialRevokedMessage
	}
	return bpsCredentialAuthFailedMessage
}
func isLegacyBPSCredentialError(message string) bool {
	return message == bpsCredentialAuthFailedMessage || message == bpsCredentialExpiredMessage || message == bpsCredentialRevokedMessage
}
func isManagedBPSCredentialError(a *Account) bool {
	if a == nil || !a.IsOpenAIBPS() || a.Status != StatusError || !isLegacyBPSCredentialError(a.ErrorMessage) {
		return false
	}
	if saved, ok := a.Extra[OpenAIBPSCredentialStateExtraKey].(map[string]any); ok {
		managed, _ := saved["managed_status_error"].(bool)
		return managed
	}
	return true // Legacy errors are recognizable, but their scheduling flag is never restored.
}

func (a *Account) OpenAIBPSCredentialState(now time.Time) *OpenAIBPSCredentialState {
	if !a.IsOpenAIBPS() {
		return nil
	}
	state := &OpenAIBPSCredentialState{Status: "unknown", RequiresManualResume: a.Status == StatusActive && !a.Schedulable}
	if expires := a.openAIBPSCredentialExpiry(); expires != nil {
		state.ExpiresAt = expires
		state.Status = "not_expired"
	}
	if saved, ok := a.Extra[OpenAIBPSCredentialStateExtraKey].(map[string]any); ok {
		if status := stringValue(saved["status"]); status == "revoked" || status == "auth_failed" {
			state.Status = status
			state.ErrorCode = stringValue(saved["error_code"])
			if observed, err := time.Parse(time.RFC3339Nano, stringValue(saved["observed_at"])); err == nil {
				state.ObservedAt = &observed
			}
		}
	} else if a.Status == StatusError && isLegacyBPSCredentialError(a.ErrorMessage) {
		state.Status = "auth_failed"
		state.ErrorCode = "bps_invalid_credentials"
		if a.ErrorMessage == bpsCredentialRevokedMessage {
			state.Status = "revoked"
			state.ErrorCode = "token_revoked"
		}
		if a.ErrorMessage == bpsCredentialExpiredMessage {
			state.Status = "expired"
			state.ErrorCode = "bps_token_expired"
		}
	}
	if state.Status != "revoked" && state.ExpiresAt != nil && !state.ExpiresAt.After(now) {
		state.Status = "expired"
		if state.ErrorCode == "" {
			state.ErrorCode = "bps_token_expired"
		}
	}
	return state
}

var bpsAuthCodePattern = regexp.MustCompile(`^[a-z0-9_.-]{1,128}$`)

func newOpenAIBPSCredentialFailure(code string, now time.Time) OpenAIBPSCredentialState {
	code = strings.ToLower(strings.TrimSpace(code))
	if !bpsAuthCodePattern.MatchString(code) {
		code = "bps_invalid_credentials"
	}
	state := OpenAIBPSCredentialState{Status: "auth_failed", ErrorCode: code, ObservedAt: &now}
	if code == "token_revoked" {
		state.Status = "revoked"
	}
	return state
}
func (s *OpenAIGatewayService) recordOpenAIBPSCredentialFailure(ctx context.Context, account *Account, code string) {
	if s.accountRepo == nil {
		return
	}
	repo, ok := s.accountRepo.(OpenAIBPSCredentialStateRepository)
	if !ok {
		slog.Error("bps_credential_state_unavailable", "account_id", account.ID)
		return
	}
	stateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := repo.SetOpenAIBPSCredentialErrorIfMatch(stateCtx, account.ID, bpsRequestCredentialSnapshot(ctx, account), newOpenAIBPSCredentialFailure(code, time.Now().UTC()))
	if err != nil {
		slog.Error("bps_credential_state_update_failed", "account_id", account.ID)
	}
}

func (a *Account) openAIBPSCredentialExpiry() *time.Time {
	if a == nil {
		return nil
	}
	expires := a.GetCredentialAsTime("expires_at")
	if expires == nil {
		return nil
	}
	normalized := expires.UTC()
	if normalized.Year() > 9999 {
		normalized = time.UnixMilli(normalized.Unix()).UTC()
	}
	if normalized.Year() < 1 || normalized.Year() > 9999 {
		return nil
	}
	return &normalized
}
