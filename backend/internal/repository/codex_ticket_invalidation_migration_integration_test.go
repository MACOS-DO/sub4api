//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"

	dbmigrations "github.com/MACOS-DO/sub4api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration243TicketInvalidationReasons(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	// Model an upgraded table with an existing legacy event, without modifying
	// other fixtures or the migration runner's already-upgraded public table.
	_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE codex_ticket_invalidations (
		id BIGSERIAL PRIMARY KEY,
		reason_code TEXT NOT NULL CHECK (reason_code = 'upstream_new_turn_state')
	) ON COMMIT DROP;
	INSERT INTO codex_ticket_invalidations(reason_code) VALUES ('upstream_new_turn_state')`)
	require.NoError(t, err)
	migration, err := dbmigrations.FS.ReadFile("243_codex_ticket_oailb_invalidation.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_ticket_invalidations(reason_code) VALUES ('upstream_turn_state_and_oailb_changed')`)
	require.NoError(t, err)
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM codex_ticket_invalidations`).Scan(&count))
	require.Equal(t, 2, count)
	_, err = tx.ExecContext(ctx, `SAVEPOINT bad_reason`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_ticket_invalidations(reason_code) VALUES ('unknown')`)
	require.Error(t, err)
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT bad_reason`)
	require.NoError(t, err)
}

func TestCodexTicketInvalidationConcurrentLifecycle(t *testing.T) {
	ctx := context.Background()
	old, fresh := uuid.NewString(), uuid.NewString()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,extra) VALUES ('ticket-lifecycle-test','openai','oauth',jsonb_build_object('codex_turn_ticket:gpt-6-astra',jsonb_build_object('generation_id',$1::text),'codex_turn_ticket:gpt-5.5',jsonb_build_object('generation_id',$2::text),'codex_allow_without_ticket',false)) RETURNING id`, old, uuid.NewString()).Scan(&id))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM codex_ticket_invalidations WHERE account_id=$1`, id)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, id)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, id)
	})
	repo := &codexTicketAttemptRepository{db: integrationDB}
	event := service.CodexTicketInvalidation{AccountID: id, Model: "gpt-6-astra", TicketGenerationID: old, OccurredAt: time.Now().UTC(), ReasonCode: service.CodexTicketInvalidationCredentialsChanged, RequestKind: "user_request", RequestRoute: "/v1/responses", ReturnedTicket: "new-state"}
	var wg sync.WaitGroup
	const requests = 8
	inserted := make([]bool, requests)
	errs := make([]error, requests)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); copy := event; inserted[i], errs[i] = repo.Invalidate(ctx, &copy) }(i)
	}
	wg.Wait()
	count := 0
	for i, value := range inserted {
		require.NoError(t, errs[i])
		if value {
			count++
		}
	}
	require.Equal(t, 1, count)
	var raw []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT extra FROM accounts WHERE id=$1`, id).Scan(&raw))
	var extra map[string]any
	require.NoError(t, json.Unmarshal(raw, &extra))
	require.NotContains(t, extra, "codex_turn_ticket:gpt-6-astra")
	require.Contains(t, extra, "codex_turn_ticket:gpt-5.5")
	require.Equal(t, false, extra["codex_allow_without_ticket"])
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM codex_ticket_invalidations WHERE account_id=$1`, id).Scan(&count))
	require.Equal(t, 1, count)
	// Install a new generation before a response from another old generation arrives.
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{codex_turn_ticket:gpt-6-astra}',jsonb_build_object('generation_id',$2::text)) WHERE id=$1`, id, fresh)
	require.NoError(t, err)
	event.TicketGenerationID = uuid.NewString()
	ok, err := repo.Invalidate(ctx, &event)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, event.InvalidatedCurrent)
	var remaining string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT extra->'codex_turn_ticket:gpt-6-astra'->>'generation_id' FROM accounts WHERE id=$1`, id).Scan(&remaining))
	require.Equal(t, fresh, remaining)
}
