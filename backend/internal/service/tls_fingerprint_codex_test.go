package service

import (
	"testing"

	"github.com/MACOS-DO/sub4api/internal/model"
	"github.com/stretchr/testify/require"
)

func newCodexTLSProfileTestService() *TLSFingerprintProfileService {
	return &TLSFingerprintProfileService{localCache: map[int64]*model.TLSFingerprintProfile{}}
}

func TestResolveTLSProfileForCodexOAuthDefaultsToOfficialFingerprints(t *testing.T) {
	svc := newCodexTLSProfileTestService()
	account := ticketTestAccount(41) // OpenAI OAuth

	httpProfile := svc.ResolveTLSProfileForTransport(account, TLSFingerprintTransportHTTP)
	require.NotNil(t, httpProfile)
	require.Contains(t, httpProfile.Name, "OpenSSL")

	wsProfile := svc.ResolveTLSProfileForTransport(account, TLSFingerprintTransportWebSocket)
	require.NotNil(t, wsProfile)
	require.Contains(t, wsProfile.Name, "rustls")

	// ResolveTLSProfile keeps HTTP semantics for existing callers.
	require.Equal(t, httpProfile.Name, svc.ResolveTLSProfile(account).Name)

	// 显式关闭：两条链路都不再伪装。
	account.Extra = map[string]any{"enable_tls_fingerprint": false}
	require.Nil(t, svc.ResolveTLSProfileForTransport(account, TLSFingerprintTransportHTTP))
	require.Nil(t, svc.ResolveTLSProfileForTransport(account, TLSFingerprintTransportWebSocket))
}

func TestResolveTLSProfileForCodexOAuthHonorsBoundProfile(t *testing.T) {
	svc := newCodexTLSProfileTestService()
	svc.localCache[7] = &model.TLSFingerprintProfile{ID: 7, Name: "custom"}
	account := ticketTestAccount(42)
	account.Extra = map[string]any{"tls_fingerprint_profile_id": float64(7)}

	require.Equal(t, "custom", svc.ResolveTLSProfileForTransport(account, TLSFingerprintTransportHTTP).Name)
	require.Equal(t, "custom", svc.ResolveTLSProfileForTransport(account, TLSFingerprintTransportWebSocket).Name)
}

func TestResolveTLSProfileSkipsNonCodexAccounts(t *testing.T) {
	svc := newCodexTLSProfileTestService()

	apiKey := ticketTestAccount(43)
	apiKey.Type = AccountTypeAPIKey
	require.Nil(t, svc.ResolveTLSProfileForTransport(apiKey, TLSFingerprintTransportHTTP))
	require.Nil(t, svc.ResolveTLSProfileForTransport(apiKey, TLSFingerprintTransportWebSocket))

	anthropic := &Account{ID: 44, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	require.Nil(t, svc.ResolveTLSProfileForTransport(anthropic, TLSFingerprintTransportHTTP))
}
