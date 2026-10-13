//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/service"
	dbmigrations "github.com/MACOS-DO/sub4api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration249PreservesHistoricalRoutingCookieReasons(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE codex_ticket_invalidations (
		id BIGSERIAL PRIMARY KEY,
		reason_code TEXT NOT NULL CONSTRAINT codex_ticket_invalidations_reason_code_check
			CHECK (reason_code IN ('upstream_new_turn_state', 'upstream_turn_state_and_oailb_changed')),
		returned_ticket TEXT NOT NULL
	) ON COMMIT DROP;
	INSERT INTO codex_ticket_invalidations(reason_code, returned_ticket) VALUES
		('upstream_new_turn_state', 'legacy-state'),
		('upstream_turn_state_and_oailb_changed', 'legacy-oailb')`)
	require.NoError(t, err)
	migration, err := dbmigrations.FS.ReadFile("249_codex_ticket_routing_cookie_invalidation.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	for _, reason := range []string{"upstream_new_turn_state", service.CodexTicketInvalidationCredentialsChanged, service.CodexTicketInvalidationRoutingCookieChanged} {
		_, err = tx.ExecContext(ctx, `INSERT INTO codex_ticket_invalidations(reason_code, returned_ticket) VALUES ($1, 'new-state')`, reason)
		require.NoError(t, err, reason)
	}
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM codex_ticket_invalidations`).Scan(&count))
	require.Equal(t, 5, count, "the migration preserves both historical events")
	for _, statement := range []string{
		`INSERT INTO codex_ticket_invalidations(reason_code, returned_ticket) VALUES ('unknown', 'state')`,
		`INSERT INTO codex_ticket_invalidations(reason_code, returned_ticket) VALUES ('upstream_turn_state_and_routing_cookie_changed', NULL)`,
	} {
		_, err = tx.ExecContext(ctx, `SAVEPOINT invalid_event`)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, statement)
		require.Error(t, err)
		_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT invalid_event`)
		require.NoError(t, err)
	}
}
