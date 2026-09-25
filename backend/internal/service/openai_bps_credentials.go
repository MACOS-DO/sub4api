package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	infraerrors "github.com/MACOS-DO/sub4api/internal/pkg/errors"
)

const OpenAIBPSResponsesURL = "https://bps.openai.com/basispoints/api/responses"

func OpenAIBPSDefaultModels() []string { return []string{"gpt-6-astra", "gpt-5.6-sol"} }

// NormalizeOpenAIBPSCredentials extracts JWT metadata, not proof of authentication.
// Only the BPS upstream can establish whether the supplied credential is usable.
func NormalizeOpenAIBPSCredentials(accountType string, incoming, existing map[string]any) (map[string]any, error) {
	if accountType != AccountTypeOAuth {
		return nil, infraerrors.BadRequest("BPS_INVALID_ACCOUNT_TYPE", "OpenAI BPS requires an Access Token account")
	}
	creds := make(map[string]any, len(existing)+len(incoming))
	for k, v := range existing {
		creds[k] = v
	}
	for k, v := range incoming {
		if k == "access_token" && strings.TrimSpace(stringValue(v)) == "" {
			continue
		}
		creds[k] = v
	}
	token := strings.TrimSpace(stringValue(creds["access_token"]))
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, infraerrors.BadRequest("BPS_TOKEN_REQUIRED", "A valid access_token is required")
	}
	creds["access_token"] = token
	var claims map[string]any
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		if data, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			_ = json.Unmarshal(data, &claims)
		}
	}
	if token != stringValue(existing["access_token"]) {
		delete(creds, "expires_at")
	}
	if exp, ok := claims["exp"].(float64); ok && exp >= -62135596800 && exp < 253402300800 {
		creds["expires_at"] = time.Unix(int64(exp), 0).UTC().Format(time.RFC3339)
	}
	id := strings.TrimSpace(stringValue(creds["chatgpt_account_id"]))
	// Explicit operator selection takes precedence over the token's default workspace.
	if id == "" {
		if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			id = strings.TrimSpace(stringValue(auth["chatgpt_account_id"]))
		}
	}
	if id == "" || len(id) > 200 || strings.ContainsAny(id, "\r\n") {
		return nil, infraerrors.BadRequest("BPS_ACCOUNT_ID_REQUIRED", "chatgpt_account_id is required when it cannot be extracted from the token")
	}
	creds["chatgpt_account_id"] = id
	delete(creds, "refresh_token")
	delete(creds, "base_url")
	delete(creds, "header_overrides")
	if _, ok := creds["model_mapping"]; !ok {
		models := map[string]any{}
		for _, m := range OpenAIBPSDefaultModels() {
			models[m] = m
		}
		creds["model_mapping"] = models
	}
	return creds, nil
}

func (a *Account) IsOpenAIBPS() bool { return a != nil && a.Platform == PlatformOpenAIBPS }
func (a *Account) bpsTokenExpired() bool {
	t := a.openAIBPSCredentialExpiry()
	return t != nil && !t.After(time.Now())
}

// A BPS account can be bound only to its own platform or a Composite group.
// Scheduling also independently checks the concrete target platform.
func (s *adminServiceImpl) validateBPSGroupBindings(ctx context.Context, platform string, groupIDs []int64) error {
	if platform != PlatformOpenAIBPS || len(groupIDs) == 0 {
		return nil
	}
	if s.groupRepo == nil {
		return infraerrors.BadRequest("BPS_GROUP_REQUIRED", "Group repository is unavailable")
	}
	for _, id := range groupIDs {
		group, err := s.groupRepo.GetByIDLite(ctx, id)
		if err != nil {
			return err
		}
		if group == nil || (group.Platform != PlatformOpenAIBPS && group.Platform != PlatformComposite) {
			return infraerrors.BadRequest("BPS_GROUP_MISMATCH", "BPS accounts require an OpenAI BPS or Composite group")
		}
	}
	return nil
}
