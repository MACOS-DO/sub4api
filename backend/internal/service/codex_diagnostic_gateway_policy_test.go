package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexTicketLookupStub struct {
	CodexTicketLifecycleRepository
	raw   json.RawMessage
	err   error
	calls int
}

func (stub *codexTicketLookupStub) CurrentTicket(context.Context, int64, string) (json.RawMessage, error) {
	stub.calls++
	return stub.raw, stub.err
}

func TestCodexDiagnosticAndNormalRequestsUseSameTicketPolicy(t *testing.T) {
	for _, test := range []struct {
		name                string
		enabled, failClosed bool
		global              string
		accountPolicy       any
		harvestDisabled     bool
		modelDisabled       bool
		ticket              bool
		model               string
		allowed             bool
	}{
		{name: "default off and allow", allowed: true},
		{name: "global off bypasses all missing-ticket restrictions", failClosed: true, global: "false", accountPolicy: false, allowed: true},
		{name: "account excluded bypasses configured deny", enabled: true, failClosed: true, harvestDisabled: true, allowed: true},
		{name: "account excluded bypasses global deny", enabled: true, global: "false", harvestDisabled: true, allowed: true},
		{name: "account excluded bypasses explicit account deny", enabled: true, global: "false", accountPolicy: false, harvestDisabled: true, allowed: true},
		{name: "account excluded still injects saved ticket", enabled: true, failClosed: true, harvestDisabled: true, ticket: true, allowed: true},
		{name: "model excluded bypasses config deny", enabled: true, failClosed: true, modelDisabled: true, allowed: true},
		{name: "model excluded bypasses global deny", enabled: true, global: "false", modelDisabled: true, allowed: true},
		{name: "model excluded bypasses account deny", enabled: true, accountPolicy: false, modelDisabled: true, allowed: true},
		{name: "model excluded still injects saved ticket", enabled: true, failClosed: true, modelDisabled: true, ticket: true, allowed: true},
		{name: "enabled defaults allow", enabled: true, allowed: true},
		{name: "configured deny", enabled: true, failClosed: true},
		{name: "saved global allow", enabled: true, failClosed: true, global: "true", allowed: true},
		{name: "saved global deny", enabled: true, global: "false"},
		{name: "account allows over global deny", enabled: true, global: "false", accountPolicy: true, allowed: true},
		{name: "account denies over global allow", enabled: true, global: "true", accountPolicy: false},
		{name: "saved ticket still injected", enabled: true, failClosed: true, ticket: true, allowed: true},
		{name: "old model exempt", enabled: true, failClosed: true, model: "gpt-5.4", allowed: true},
	} {
		for _, advanced := range []string{"false", "true"} {
			t.Run(test.name+"/advanced="+advanced, func(t *testing.T) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				model := test.model
				if model == "" {
					model = "gpt-6-astra"
				}
				account := ticketTestAccount(41)
				account.Concurrency = 1
				account.Schedulable = true
				account.Extra = map[string]any{}
				if test.modelDisabled {
					account.Extra[codexTicketModelsEnabledKey] = map[string]bool{model: false}
				}
				if test.harvestDisabled {
					account.Extra[codexTicketAccountEnabledKey] = false
				}
				if test.accountPolicy != nil {
					account.Extra["codex_allow_without_ticket"] = test.accountPolicy
				}
				if test.ticket {
					account.Extra[openAICodexTicketExtraKey(model)] = verifiedTicket(account, model, "saved-state", "__oailb=saved")
				}
				cfg := &config.Config{}
				cfg.Gateway.OpenAICodexTicket = config.OpenAICodexTicketConfig{Enabled: test.enabled, FailClosed: test.failClosed}
				values := map[string]string{openAIAdvancedSchedulerSettingKey: advanced}
				if test.global != "" {
					values[SettingKeyOpenAICodexTicketAllowWithoutTicket] = test.global
				}
				svc := &OpenAIGatewayService{cfg: cfg, accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{*account}}, cache: &schedulerTestGatewayCache{}, concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
					settingService: NewSettingService(&codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: values}}, cfg)}
				// Exercise the production lifecycle lookup, including SQL-normalized JSON null.
				raw, err := json.Marshal(account.Extra[openAICodexTicketExtraKey(model)])
				require.NoError(t, err)
				lookup := &codexTicketLookupStub{raw: raw}
				svc.openaiCodexTicketLifecycle = lookup
				for _, ctx := range []context.Context{context.Background(), WithCodexTicketDiagnostic(context.Background(), account.ID)} {
					selection, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "", model, nil, OpenAIUpstreamTransportAny, false)
					if !test.allowed {
						require.Error(t, err)
						require.Nil(t, selection)
					} else {
						require.NoError(t, err)
						require.Equal(t, account.ID, selection.Account.ID)
						if selection.ReleaseFunc != nil {
							selection.ReleaseFunc()
						}
					}
					headers := http.Header{}
					err = svc.applyOpenAICodexTicket(ctx, account, model, headers)
					if test.allowed {
						require.NoError(t, err)
					} else {
						require.ErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
					}
					if test.ticket {
						require.Equal(t, "saved-state", headers.Get(openAICodexTurnStateHeader))
						require.Equal(t, "__oailb=saved", headers.Get("Cookie"))
					}
				}
				if test.enabled && svc.openAICodexTicketGatedModel(model) {
					require.Equal(t, 2, lookup.calls)
				} else {
					require.Zero(t, lookup.calls)
				}
			})
		}
	}
}

func TestCodexDiagnosticCannotFailOverToAnotherAccount(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	target, other := ticketTestAccount(41), ticketTestAccount(42)
	for _, account := range []*Account{target, other} {
		account.Schedulable = true
		account.Concurrency = 1
	}
	target.Extra = map[string]any{"codex_allow_without_ticket": false}
	other.Extra = map[string]any{"codex_allow_without_ticket": true}
	svc := newOpenAICompactionSchedulerTestService([]Account{*target, *other}, false)
	svc.cfg.Gateway.OpenAICodexTicket = config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}
	selection, _, err := svc.SelectAccountWithScheduler(WithCodexTicketDiagnostic(context.Background(), target.ID), nil, "", "", "gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
	require.Error(t, err)
	require.Nil(t, selection)
	selection, _, err = svc.SelectAccountWithScheduler(context.Background(), nil, "", "", "gpt-6-astra", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, other.ID, selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestCodexTicketLookupErrorDoesNotBecomeNoTicket(t *testing.T) {
	account := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: false}, nil)
	lookup := &codexTicketLookupStub{err: errors.New("database unavailable")}
	svc.openaiCodexTicketLifecycle = lookup
	for _, ctx := range []context.Context{context.Background(), WithCodexTicketDiagnostic(context.Background(), account.ID)} {
		err := svc.applyOpenAICodexTicket(ctx, account, "gpt-6-astra", http.Header{})
		require.ErrorIs(t, err, lookup.err)
		require.NotErrorIs(t, err, ErrOpenAICodexTicketUnavailable)
	}
	require.Equal(t, 2, lookup.calls)
}
