package migrations

import (
	"regexp"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMigration243PreservesLegacyTicketInvalidations(t *testing.T) {
	content, err := FS.ReadFile("243_codex_ticket_oailb_invalidation.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "DROP CONSTRAINT codex_ticket_invalidations_reason_code_check")
	require.Contains(t, sql, "ADD CONSTRAINT codex_ticket_invalidations_reason_code_check")
	matches := regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(sql, -1)
	var reasons []string
	for _, match := range matches {
		reasons = append(reasons, match[1])
	}
	require.ElementsMatch(t, []string{"upstream_new_turn_state", service.CodexTicketInvalidationCredentialsChanged}, reasons)
}
