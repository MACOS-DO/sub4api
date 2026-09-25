package dto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/MACOS-DO/sub4api/internal/service"
)

func TestAccountFromServiceShallow_RedactsSensitiveCredentials(t *testing.T) {
	src := &service.Account{
		ID:       42,
		Name:     "demo",
		Platform: "anthropic",
		Type:     "oauth",
		Credentials: map[string]any{
			"access_token":  "at-secret",
			"refresh_token": "rt-secret",
			"id_token":      "id-secret",
			"api_key":       "sk-secret",
			"base_url":      "https://api.example.com",
			"model_mapping": map[string]any{"foo": "bar"},
		},
	}

	got := AccountFromServiceShallow(src)
	require.NotNil(t, got)

	// 敏感键不在 Credentials 里
	require.NotContains(t, got.Credentials, "access_token")
	require.NotContains(t, got.Credentials, "refresh_token")
	require.NotContains(t, got.Credentials, "id_token")
	require.NotContains(t, got.Credentials, "api_key")
	// 非敏感键保留
	require.Equal(t, "https://api.example.com", got.Credentials["base_url"])
	require.Equal(t, map[string]any{"foo": "bar"}, got.Credentials["model_mapping"])

	// 状态 map 标记敏感键存在
	require.True(t, got.CredentialsStatus["has_access_token"])
	require.True(t, got.CredentialsStatus["has_refresh_token"])
	require.True(t, got.CredentialsStatus["has_id_token"])
	require.True(t, got.CredentialsStatus["has_api_key"])

	// JSON 序列化校验：响应体里不会出现敏感子串
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "rt-secret")
	require.NotContains(t, string(raw), "at-secret")
	require.NotContains(t, string(raw), "sk-secret")
	require.NotContains(t, string(raw), "id-secret")
	// 状态标识应序列化进 JSON
	require.Contains(t, string(raw), "credentials_status")
	require.Contains(t, string(raw), "has_refresh_token")

	// 原始 service.Account 不应被改动
	require.Equal(t, "rt-secret", src.Credentials["refresh_token"])
}

func TestAccountFromServiceShallow_RedactsOllamaCloudManagedExtra(t *testing.T) {
	snapshot := map[string]any{
		"status":          service.OllamaCloudUsageStatusOK,
		"last_attempt_at": "2026-07-22T12:00:00Z",
		"next_refresh_at": "2026-07-22T13:00:00Z",
		"data":            map[string]any{"plan": "Pro"},
	}
	src := &service.Account{
		ID: 9, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://ollama.com", "api_key": "secret-key"},
		Extra: map[string]any{
			service.OllamaCloudUsageSessionExtraKey:     "ciphertext-secret",
			service.OllamaCloudUsageAutoRefreshExtraKey: true,
			service.OllamaCloudUsageSnapshotExtraKey:    snapshot,
			"ordinary":                                  "kept",
		},
	}

	got := AccountFromServiceShallow(src)
	require.NotContains(t, got.Extra, service.OllamaCloudUsageSessionExtraKey)
	require.NotContains(t, got.Extra, service.OllamaCloudUsageAutoRefreshExtraKey)
	require.NotContains(t, got.Extra, service.OllamaCloudUsageSnapshotExtraKey)
	require.Equal(t, "kept", got.Extra["ordinary"])
	require.NotNil(t, got.OllamaCloudUsage)
	require.True(t, got.OllamaCloudUsage.Configured)
	require.True(t, got.OllamaCloudUsage.AutoRefreshEnabled)
	require.Equal(t, "Pro", got.OllamaCloudUsage.Snapshot.Data.Plan)

	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "ciphertext-secret")
	require.NotContains(t, string(raw), "secret-key")
	require.Contains(t, src.Extra, service.OllamaCloudUsageSessionExtraKey)
}

func TestAccountFromServiceShallow_RedactsCodexTurnTicketState(t *testing.T) {
	blob := "gAAAAA" + strings.Repeat("B", 286)
	src := &service.Account{
		ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Extra: map[string]any{
			"codex_harvest_proxy_url": "http://user:legacy-proxy-secret@proxy.example.com:8080",
			"codex_turn_ticket:gpt-6-astra": map[string]any{
				"state":       blob,
				"length":      292,
				"model":       "gpt-6-astra",
				"captured_at": time.Now().Add(-time.Minute),
				"expires_at":  time.Now().Add(time.Hour),
				"attempts":    3,
			},
		},
	}
	got := AccountFromServiceShallow(src)
	require.NotContains(t, got.Extra, "codex_turn_ticket:gpt-6-astra")
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), blob)
	require.NotContains(t, string(raw), "legacy-proxy-secret")
	require.NotContains(t, got.Extra, "codex_harvest_proxy_url")
	require.Contains(t, src.Extra, "codex_harvest_proxy_url")
}

func TestAccountFromServiceShallow_NilCredentialsOmitsStatus(t *testing.T) {
	src := &service.Account{ID: 1, Name: "n", Platform: "anthropic", Type: "oauth"}
	got := AccountFromServiceShallow(src)
	require.NotNil(t, got)
	require.Nil(t, got.Credentials)
	require.Nil(t, got.CredentialsStatus)
}

func TestOpenAIBPSAccountDTORedactsTokenKeepsExpiry(t *testing.T) {
	src := &service.Account{ID: 42, Platform: service.PlatformOpenAIBPS, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "bps-test-secret", "chatgpt_account_id": "workspace", "expires_at": "2026-10-01T00:00:00Z"}}
	dto := AccountFromServiceShallow(src)
	require.NotContains(t, dto.Credentials, "access_token")
	require.True(t, dto.CredentialsStatus["has_access_token"])
	require.Equal(t, "workspace", dto.Credentials["chatgpt_account_id"])
	require.Equal(t, "2026-10-01T00:00:00Z", dto.Credentials["expires_at"])
	data, err := json.Marshal(dto)
	require.NoError(t, err)
	require.NotContains(t, string(data), "bps-test-secret")
}

func TestAccountFromServiceBPSCredentialStateFullAndLite(t *testing.T) {
	now := time.Now().UTC()
	source := &service.Account{ID: 42, Platform: service.PlatformOpenAIBPS, Type: service.AccountTypeOAuth, Status: service.StatusError, Schedulable: true, Credentials: map[string]any{"access_token": "private-bps-token", "chatgpt_account_id": "workspace", "expires_at": now.Add(time.Hour).Format(time.RFC3339)}, Extra: map[string]any{service.OpenAIBPSCredentialStateExtraKey: map[string]any{"status": "revoked", "error_code": "token_revoked", "observed_at": now.Format(time.RFC3339Nano), "managed_status_error": true, "credential_identity": "private-fingerprint"}}}
	full := AccountFromService(source)
	lite := AccountListItemFromAccount(full)
	require.Equal(t, "revoked", full.BPSCredentialState.Status)
	require.Equal(t, full.BPSCredentialState, lite.BPSCredentialState)
	require.True(t, full.CredentialsStatus["has_access_token"])
	require.NotContains(t, full.Credentials, "access_token")
	require.NotContains(t, full.Extra, service.OpenAIBPSCredentialStateExtraKey)
	data, err := json.Marshal(lite)
	require.NoError(t, err)
	require.NotContains(t, string(data), "private-bps-token")
	require.NotContains(t, string(data), "private-fingerprint")
	source.Extra = nil
	source.Status = service.StatusActive
	source.Credentials["expires_at"] = now.Add(-time.Hour).Format(time.RFC3339)
	require.Equal(t, "expired", AccountFromService(source).BPSCredentialState.Status)
	require.Equal(t, service.StatusActive, source.Status)
}
