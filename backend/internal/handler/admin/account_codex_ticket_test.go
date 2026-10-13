package admin

import (
	"testing"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountResponseCodexTicketUsesConfiguredPolicy(t *testing.T) {
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive}
	h := &AccountHandler{cfg: &config.Config{}}
	initial := h.accountResponseFromService(account).CodexTicket
	require.NotNil(t, initial)
	require.False(t, initial.HarvestEnabled)
	h.cfg.Gateway.OpenAICodexTicket = config.OpenAICodexTicketConfig{Enabled: true, FailClosed: false}
	status := h.accountListResponseFromService(account).CodexTicket
	require.Equal(t, "gpt-6-sol", status.Model, "the global ticket is harvested with gpt-6-sol")
	require.True(t, status.HarvestEnabled)
	require.False(t, status.Blocked)
	h.cfg.Gateway.OpenAICodexTicket.FailClosed = true
	require.True(t, h.accountResponseFromService(account).CodexTicket.Blocked)

	other := &service.Account{ID: 42, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive}
	require.Nil(t, h.accountResponseFromService(other).CodexTicket)
}

func TestAccountResponseCodexTicketReadsLiveSettingsAfterRestart(t *testing.T) {
	cfg := &config.Config{}
	repo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyOpenAICodexTicketEnabled: "true"}}
	settings := service.NewSettingService(repo, cfg)
	h := &AccountHandler{cfg: cfg}
	h.SetCodexTicketSettings(settings)
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeSetupToken, Status: service.StatusActive}
	require.True(t, h.accountListResponseFromService(account).CodexTicket.HarvestEnabled)
	require.False(t, cfg.Gateway.OpenAICodexTicket.Enabled)
	repo.values[service.SettingKeyOpenAICodexTicketEnabled] = "false"
	settings.InvalidateOpenAICodexTicketEnabledCache()
	require.False(t, h.accountResponseFromService(account).CodexTicket.HarvestEnabled)
}
