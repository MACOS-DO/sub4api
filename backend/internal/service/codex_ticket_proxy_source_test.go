package service

import (
	"context"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketProxySourceDefaultsToPool(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	settings := &SettingService{settingRepo: repo, proxyRepo: &challengeHarvestProxyRepo{}}
	require.Equal(t, CodexTicketProxySourcePool, settings.GetCodexTicketProxySource(context.Background()))

	repo.values[codexTicketProxySourceSettingKey] = "bogus"
	require.Equal(t, CodexTicketProxySourcePool, settings.GetCodexTicketProxySource(context.Background()))

	_, err := settings.SetCodexTicketProxySource(context.Background(), "bogus")
	require.Error(t, err)
	source, err := settings.SetCodexTicketProxySource(context.Background(), " account ")
	require.NoError(t, err)
	require.Equal(t, CodexTicketProxySourceAccount, source)
	require.Equal(t, CodexTicketProxySourceAccount, settings.GetCodexTicketProxySource(context.Background()))
}

func TestChooseCodexTicketProxyHonorsSource(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.settingService = &SettingService{settingRepo: repo, proxyRepo: &challengeHarvestProxyRepo{}}

	accountProxyID := int64(7)
	account := ticketTestAccount(41)
	account.ProxyID = &accountProxyID
	account.Proxy = &Proxy{ID: accountProxyID, Name: "account-ip", Protocol: "socks5h", Host: "account.example.com", Port: 1080, Status: StatusActive}

	url, proxy, err := svc.chooseCodexTicketProxy(context.Background(), account, "gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, int64(1), proxy.ID, "pool mode rotates the ticket proxy pool")
	require.Equal(t, "http://proxy.example.com:8080", url)

	repo.values[codexTicketProxySourceSettingKey] = CodexTicketProxySourceAccount
	url, proxy, err = svc.chooseCodexTicketProxy(context.Background(), account, "gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, accountProxyID, proxy.ID)
	require.Equal(t, "socks5h://account.example.com:1080", url)

	direct := ticketTestAccount(42)
	url, proxy, err = svc.chooseCodexTicketProxy(context.Background(), direct, "gpt-6-astra")
	require.NoError(t, err)
	require.Nil(t, proxy, "account without proxy harvests directly, like its business traffic")
	require.Empty(t, url)
	require.True(t, svc.settingService.CodexTicketManualProxyAvailable(context.Background()))
}
