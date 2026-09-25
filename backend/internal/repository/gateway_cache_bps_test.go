package repository

import (
	"context"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOpenAIBPSRedisCrossInstanceAndExpiry(t *testing.T) {
	server := miniredis.RunT(t)
	ctx := context.Background()
	connect := func() *gatewayCache {
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		return &gatewayCache{rdb: client}
	}
	first := connect()
	second := connect()
	key := "openai_bps:tenant-session:session"
	binding := []byte(`{"account_id":42,"identity":"workspace"}`)
	got, err := first.BPSBind(ctx, key, binding)
	require.NoError(t, err)
	require.Equal(t, binding, got)
	got, err = second.BPSBind(ctx, key, []byte(`{"account_id":99}`))
	require.NoError(t, err)
	require.Equal(t, binding, got)
	require.NoError(t, first.BPSPut(ctx, key+":call:1", []byte(`{"arguments":"original OfficeJS envelope"}`)))
	// Reconnect simulates a fresh process with no local state.
	restarted := connect()
	got, err = restarted.BPSGet(ctx, key+":call:1")
	require.NoError(t, err)
	require.Contains(t, string(got), "original OfficeJS envelope")
	got, err = second.BPSGet(ctx, "openai_bps:another-tenant:session")
	require.NoError(t, err)
	require.Empty(t, got)
	server.FastForward(service.OpenAIBPSStateTTL - time.Hour)
	_, err = restarted.BPSGet(ctx, key)
	require.NoError(t, err)
	server.FastForward(2 * time.Hour)
	got, err = second.BPSGet(ctx, key)
	require.NoError(t, err)
	require.Equal(t, binding, got)
	got, err = second.BPSGet(ctx, key+":call:1")
	require.NoError(t, err)
	require.Empty(t, got)
	server.FastForward(service.OpenAIBPSStateTTL + time.Second)
	got, err = first.BPSGet(ctx, key)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestOpenAIBPSSchedulerMetadataKeepsBindingAndExpiry(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	account := service.Account{ID: 42, Platform: service.PlatformOpenAIBPS, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "private-token", "refresh_token": "unused-private-token", "chatgpt_account_id": "workspace-42", "expires_at": expiry, "model_mapping": map[string]any{"gpt-6-astra": "gpt-6-astra"}}}
	metadata := buildSchedulerMetadataAccount(account)
	require.Equal(t, "workspace-42", metadata.GetCredential("chatgpt_account_id"))
	require.Equal(t, expiry, metadata.GetCredential("expires_at"))
	require.True(t, metadata.IsSchedulable())
	require.NotContains(t, metadata.Credentials, "access_token")
	require.NotContains(t, metadata.Credentials, "refresh_token")
	account.Credentials["expires_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	expiredMetadata := buildSchedulerMetadataAccount(account)
	require.False(t, expiredMetadata.IsSchedulable())
}
