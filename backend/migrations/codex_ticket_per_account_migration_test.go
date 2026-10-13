package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration248MakesInvalidationsPerAccount(t *testing.T) {
	content, err := FS.ReadFile("248_codex_ticket_per_account_invalidation.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS codex_ticket_invalidations_ticket_generation_id_key")
	require.Contains(t, sql, "UNIQUE (account_id, ticket_generation_id)")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS codex_ticket_invalidations_generation")
	require.NotContains(t, sql, "reason_code", "per-account uniqueness leaves the reason codes alone")
	require.NotContains(t, sql, "returned_ticket")
}
