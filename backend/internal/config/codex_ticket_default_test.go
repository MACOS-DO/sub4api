package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadCodexTicketDefaultsAndExplicitPolicy(t *testing.T) {
	for _, mode := range []string{"default", "yaml", "environment"} {
		t.Run(mode, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv("GATEWAY_OPENAI_CODEX_TICKET_FAIL_CLOSED", "")
			if mode == "yaml" {
				path := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte("gateway:\n  openai_codex_ticket:\n    fail_closed: true\n"), 0600))
				t.Setenv("CONFIG_FILE", path)
			}
			if mode == "environment" {
				t.Setenv("GATEWAY_OPENAI_CODEX_TICKET_FAIL_CLOSED", "true")
			}
			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, mode != "default", cfg.Gateway.OpenAICodexTicket.FailClosed)
		})
	}
}

func TestLoadCodexTicketHarvestIsOptIn(t *testing.T) {
	for _, test := range []struct {
		name, yaml, environment string
		enabled                 bool
	}{
		{name: "default"},
		{name: "yaml enabled", yaml: "true", enabled: true},
		{name: "yaml disabled", yaml: "false"},
		{name: "environment enabled", environment: "true", enabled: true},
		{name: "environment disabled", yaml: "true", environment: "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv("GATEWAY_OPENAI_CODEX_TICKET_ENABLED", test.environment)
			t.Setenv("GATEWAY_OPENAI_CODEX_TICKET_FAIL_CLOSED", "")
			if test.yaml != "" {
				path := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte("gateway:\n  openai_codex_ticket:\n    enabled: "+test.yaml+"\n"), 0600))
				t.Setenv("CONFIG_FILE", path)
			}
			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, test.enabled, cfg.Gateway.OpenAICodexTicket.Enabled)
			require.False(t, cfg.Gateway.OpenAICodexTicket.FailClosed)
		})
	}
}
