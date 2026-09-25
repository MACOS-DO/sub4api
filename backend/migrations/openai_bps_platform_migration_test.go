package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIBPSPlatformMigration(t *testing.T) {
	data, err := FS.ReadFile("245_openai_bps_platform.sql")
	require.NoError(t, err)
	sql := string(data)
	for _, table := range []string{"user_platform_quotas", "composite_model_routes", "channel_monitors", "channel_monitor_request_templates"} {
		require.Contains(t, sql, "ALTER TABLE "+table+" DROP CONSTRAINT IF EXISTS")
		require.Contains(t, sql, "ALTER TABLE "+table+" ADD CONSTRAINT")
	}
	for _, p := range []string{"anthropic", "openai", "openai_bps", "gemini", "antigravity", "grok", "kimi", "zhipu", "deepseek", "minimax", "opencode_go"} {
		require.Equal(t, 4, strings.Count(sql, "'"+p+"'"), p)
	}
	require.NotContains(t, strings.ToUpper(sql), "UPDATE ACCOUNTS")
}
