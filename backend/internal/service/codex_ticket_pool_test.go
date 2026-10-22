package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexTicketPoolProxyRepo struct {
	ProxyRepository
	proxies []Proxy
}

func (r *codexTicketPoolProxyRepo) ListActive(context.Context) ([]Proxy, error) {
	active := make([]Proxy, 0, len(r.proxies))
	for _, proxy := range r.proxies {
		if proxy.IsActive() {
			active = append(active, proxy)
		}
	}
	return active, nil
}

func (r *codexTicketPoolProxyRepo) ListByIDs(_ context.Context, ids []int64) ([]Proxy, error) {
	found := make([]Proxy, 0, len(ids))
	for _, proxy := range r.proxies {
		for _, id := range ids {
			if proxy.ID == id {
				found = append(found, proxy)
			}
		}
	}
	return found, nil
}

func codexTicketPoolFixture(t *testing.T) (*SettingService, *runtimeSettingRepoStub, *codexTicketPoolProxyRepo) {
	t.Helper()
	repo := newRuntimeSettingRepoStub()
	proxies := &codexTicketPoolProxyRepo{proxies: []Proxy{
		{ID: 1, Name: "A", Protocol: "http", Host: "a.example.com", Port: 8001, Status: StatusActive},
		{ID: 2, Name: "B", Protocol: "http", Host: "b.example.com", Port: 8002, Status: StatusActive},
		{ID: 3, Name: "C", Protocol: "http", Host: "c.example.com", Port: 8003, Status: StatusActive},
	}}
	settings := &SettingService{settingRepo: repo, proxyRepo: proxies}
	require.NoError(t, settings.SetCodexTicketPool(context.Background(), CodexTicketPool{Mode: "custom", ProxyIDs: []int64{2, 1}}))
	return settings, repo, proxies
}

func TestCodexTicketCustomPoolNeverUsesUnselectedProxy(t *testing.T) {
	settings, _, proxies := codexTicketPoolFixture(t)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	svc.settingService = settings
	account := ticketTestAccount(41)
	accountProxyID := int64(3)
	account.ProxyID, account.Proxy = &accountProxyID, &proxies.proxies[2]

	used := map[string]int{}
	for range 20 {
		_, proxy, err := svc.chooseCodexTicketProxy(context.Background(), account, "gpt-6-astra")
		require.NoError(t, err)
		used[proxy.Name]++
	}
	require.Equal(t, map[string]int{"A": 10, "B": 10}, used, "the account's own proxy C must not leak into pool mode")

	past := time.Now().Add(-time.Hour)
	proxies.proxies[0].ExpiresAt = &past
	for range 5 {
		_, proxy, err := svc.chooseCodexTicketProxy(context.Background(), account, "gpt-6-astra")
		require.NoError(t, err)
		require.Equal(t, "B", proxy.Name)
	}
}

func TestDescribeCodexTicketPoolReportsEffectiveAndUnavailable(t *testing.T) {
	settings, repo, proxies := codexTicketPoolFixture(t)
	view, err := settings.DescribeCodexTicketPool(context.Background())
	require.NoError(t, err)
	require.Equal(t, CodexTicketProxySourcePool, view.Source)
	require.Equal(t, []CodexTicketPoolProxyRef{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}}, view.Effective)
	require.Empty(t, view.Unavailable)

	past := time.Now().Add(-time.Hour)
	proxies.proxies[0].ExpiresAt = &past
	proxies.proxies[1].Status = "disabled"
	raw, err := json.Marshal(CodexTicketPool{Mode: "custom", ProxyIDs: []int64{1, 2, 9}})
	require.NoError(t, err)
	repo.values[codexTicketPoolSettingKey] = string(raw)
	repo.values[codexTicketProxySourceSettingKey] = CodexTicketProxySourceAccount
	view, err = settings.DescribeCodexTicketPool(context.Background())
	require.NoError(t, err)
	require.Equal(t, CodexTicketProxySourceAccount, view.Source)
	require.Empty(t, view.Effective)
	require.Equal(t, []CodexTicketPoolProxyRef{
		{ID: 1, Name: "A", Reason: "expired"},
		{ID: 2, Name: "B", Reason: "disabled"},
		{ID: 9, Reason: "deleted"},
	}, view.Unavailable)

	require.NoError(t, settings.SetCodexTicketPool(context.Background(), CodexTicketPool{Mode: "all"}))
	view, err = settings.DescribeCodexTicketPool(context.Background())
	require.NoError(t, err)
	require.Equal(t, []CodexTicketPoolProxyRef{{ID: 3, Name: "C"}}, view.Effective)
}
