package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadCodexGatewayAutoGenerateKeyIsOptIn(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, environment string
		want                    bool
	}{
		{name: "default"},
		{name: "yaml enabled", yaml: "true", want: true},
		{name: "environment enabled", environment: "true", want: true},
		{name: "environment overrides yaml", yaml: "true", environment: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv("GATEWAY_CODEX4SERVER_AUTO_GENERATE_SERVICE_KEY", tc.environment)
			if tc.yaml != "" {
				path := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(path, []byte("gateway:\n  codex4server:\n    auto_generate_service_key: "+tc.yaml+"\n"), 0600))
				t.Setenv("CONFIG_FILE", path)
			}
			cfg, err := Load()
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.Gateway.Codex4Server.AutoGenerateServiceKey)
		})
	}
}
