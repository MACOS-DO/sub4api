package repository

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCodexTicketExtraIsSchedulerNeutral(t *testing.T) {
	require.True(t, isSchedulerNeutralExtraKey("codex_turn_ticket:gpt-6-astra"))
	// 门票续期不再开事务发 bucket 重建事件（快照仍由 UpdateExtra 单独同步）。
	require.False(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"codex_turn_ticket:gpt-6-astra": map[string]any{"state": "x"},
	}))
	// 但门票不得掩盖同批写入的真实调度变更。
	require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"codex_turn_ticket:gpt-6-astra": map[string]any{"state": "x"},
		"openai_passthrough":            true,
	}))
}
