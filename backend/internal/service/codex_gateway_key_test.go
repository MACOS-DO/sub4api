package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexGatewayBadServiceKeyDoesNotPreventServiceConstruction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service_key")
	require.NoError(t, os.WriteFile(path, []byte("invalid"), 0600))
	cfg := &config.Config{}
	cfg.Gateway.Codex4Server = config.Codex4ServerConfig{Enabled: true, BaseURL: "http://gateway:8787", ServiceKeyFile: path, AutoGenerateServiceKey: true}
	s := NewCodexGatewayService(cfg, &codexRecoveryRepository{})
	require.NotNil(t, s)
	require.False(t, s.Ready())
	require.NotPanics(t, func() { s.Start(); s.Stop() })
	legacy := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
	require.True(t, legacy.IsSchedulable())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "invalid", string(raw))
}
