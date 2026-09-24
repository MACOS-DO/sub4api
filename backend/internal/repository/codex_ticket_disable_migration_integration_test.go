//go:build integration

package repository

import (
	"context"
	"testing"

	dbmigrations "github.com/MACOS-DO/sub4api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration244DisablesOnlyGlobalTicketHarvesting(t *testing.T) {
	data, err := dbmigrations.FS.ReadFile("244_disable_codex_ticket_harvesting.sql")
	require.NoError(t, err)
	for _, before := range []string{"missing", "true", "false"} {
		t.Run(before, func(t *testing.T) {
			tx := testTx(t)
			ctx := context.Background()
			_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE settings (
                key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
            ) ON COMMIT DROP;
            CREATE TEMP TABLE accounts (id BIGINT PRIMARY KEY, extra JSONB NOT NULL) ON COMMIT DROP;
            INSERT INTO settings(key, value) VALUES
                ('openai_codex_ticket_allow_without_ticket', 'false'),
                ('openai_codex_ticket_prompt_template', 'custom template');
            INSERT INTO accounts(id, extra) VALUES (1, '{"codex_ticket_harvest_enabled":true,"codex_ticket_harvest_models":{"gpt-6-astra":false},"codex_allow_without_ticket":false}');`)
			require.NoError(t, err)
			if before != "missing" {
				_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES ('openai_codex_ticket_enabled',$1)`, before)
				require.NoError(t, err)
			}
			_, err = tx.ExecContext(ctx, string(data))
			require.NoError(t, err)
			var enabled, policy, template, extra string
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='openai_codex_ticket_enabled'`).Scan(&enabled))
			require.Equal(t, "false", enabled)
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='openai_codex_ticket_allow_without_ticket'`).Scan(&policy))
			require.Equal(t, "false", policy)
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='openai_codex_ticket_prompt_template'`).Scan(&template))
			require.Equal(t, "custom template", template)
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT extra::text FROM accounts WHERE id=1`).Scan(&extra))
			require.JSONEq(t, `{"codex_ticket_harvest_enabled":true,"codex_ticket_harvest_models":{"gpt-6-astra":false},"codex_allow_without_ticket":false}`, extra)
		})
	}
}
