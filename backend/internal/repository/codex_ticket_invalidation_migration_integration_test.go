//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	dbmigrations "github.com/MACOS-DO/sub4api/migrations"
	"github.com/google/uuid"
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

func TestMigration247SharedTicketsSeedAndCleanup(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	// Shadow the upgraded public tables with temporary copies so the migration's
	// UPDATE statements only touch fixtures inside this transaction.
	_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE codex_ticket_invalidations (
		id BIGSERIAL PRIMARY KEY,
		reason_code TEXT NOT NULL CHECK (reason_code IN ('upstream_new_turn_state', 'upstream_turn_state_and_oailb_changed')),
		returned_ticket TEXT NOT NULL
	) ON COMMIT DROP;
	INSERT INTO codex_ticket_invalidations(reason_code, returned_ticket) VALUES ('upstream_turn_state_and_oailb_changed', 'legacy-state');
	CREATE TEMP TABLE codex_shared_tickets (LIKE public.codex_shared_tickets INCLUDING ALL) ON COMMIT DROP;
	CREATE TEMP TABLE accounts (id BIGINT PRIMARY KEY, extra JSONB, deleted_at TIMESTAMPTZ) ON COMMIT DROP;
	INSERT INTO accounts(id, extra) VALUES
		(1, '{"codex_turn_ticket:gpt-6-astra":{"state":"s"},"codex_turn_ticket:gpt-5.6-sol":{"state":"t"},"codex_allow_without_ticket":false,"codex_ticket_harvest_models":{"gpt-6-astra":false}}'),
		(2, '{"custom":true}'),
		(3, '[]'),
		(4, NULL),
		(5, '{"codex_turn_ticket:migration-seed-model":{"generation_id":"55555555-5555-4555-8555-555555555555","verification_method":"modeltrace_v1","fingerprint_commit":"c","cookie":"__oailb=kept","captured_at":"2026-01-02T03:04:05Z"}}'),
		(6, '{"codex_turn_ticket:migration-state-model":{"generation_id":"66666666-6666-4666-8666-666666666666","verification_method":"modeltrace_v1","fingerprint_commit":"c","state":"turn-state-only","captured_at":"2026-01-02T03:04:05Z"}}'),
		(7, '{"codex_turn_ticket:migration-empty-model":{"generation_id":"77777777-7777-4777-8777-777777777777","verification_method":"modeltrace_v1","fingerprint_commit":"c","state":" ","cookie":"","captured_at":"2026-01-02T03:04:05Z"}}'),
		(8, '{"codex_turn_ticket:gpt-6-astra":{"generation_id":"88888888-8888-4888-8888-AAAAAAAAAAAA","verification_method":"modeltrace_v1","fingerprint_commit":"c","state":"newest-state","cookie":"route=a; __oailb=newest","captured_at":"2026-01-02T03:04:05Z"}}'),
		(9, '{"codex_turn_ticket:gpt-5.6-sol":{"generation_id":"99999999-9999-4999-8999-999999999999","verification_method":"modeltrace_v1","fingerprint_commit":"c","state":"older-state","cookie":"__oailb=older","captured_at":"2026-01-01T03:04:05Z"}}'),
		(10, '{"codex_ticket_harvest_models":{"gpt-6-sol":false},"custom":1}')`)
	require.NoError(t, err)
	migration, err := dbmigrations.FS.ReadFile("247_codex_shared_ticket.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)

	// The invalidation table is untouched: the reason check and NOT NULL returned_ticket stay.
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_ticket_invalidations(reason_code, returned_ticket) VALUES ($1, 'new-state')`, service.CodexTicketInvalidationCredentialsChanged)
	require.NoError(t, err)
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM codex_ticket_invalidations`).Scan(&count))
	require.Equal(t, 2, count)
	for name, statement := range map[string]string{
		"bad_reason":     `INSERT INTO codex_ticket_invalidations(reason_code, returned_ticket) VALUES ('unknown', 'state')`,
		"null_returned":  `INSERT INTO codex_ticket_invalidations(reason_code, returned_ticket) VALUES ('upstream_turn_state_and_oailb_changed', NULL)`,
		"removed_reason": `INSERT INTO codex_ticket_invalidations(reason_code, returned_ticket) VALUES ('upstream_oailb_changed', 'state')`,
	} {
		_, err = tx.ExecContext(ctx, `SAVEPOINT `+name)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, statement)
		require.Error(t, err, name)
		_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT `+name)
		require.NoError(t, err)
	}

	extras := map[int64]string{}
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(extra::text, 'NULL') FROM accounts ORDER BY id`)
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		var extra string
		require.NoError(t, rows.Scan(&id, &extra))
		extras[id] = extra
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.JSONEq(t, `{"codex_allow_without_ticket":false}`, extras[1], "retired per-model switches are removed")
	require.JSONEq(t, `{"custom":true}`, extras[2])
	require.JSONEq(t, `[]`, extras[3])
	require.Equal(t, "NULL", extras[4])
	require.JSONEq(t, `{}`, extras[5])
	require.JSONEq(t, `{}`, extras[6])
	require.JSONEq(t, `{}`, extras[7])
	require.JSONEq(t, `{}`, extras[8])
	require.JSONEq(t, `{}`, extras[9])
	require.JSONEq(t, `{"custom":1}`, extras[10])

	// The newest old ticket of any model carrying both a turn-state and an __oailb cookie
	// becomes the single global ticket; cookie-only (5) and turn-state-only (6) tickets do not.
	var seededGeneration, ticketGeneration, seededState string
	var seededSource, seededVersion int64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM codex_shared_tickets`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT generation_id::text, ticket ->> 'generation_id', source_account_id, version, ticket ->> 'state' FROM codex_shared_tickets WHERE id=1`).
		Scan(&seededGeneration, &ticketGeneration, &seededSource, &seededVersion, &seededState))
	require.Equal(t, "88888888-8888-4888-8888-aaaaaaaaaaaa", seededGeneration)
	require.Equal(t, seededGeneration, ticketGeneration, "the ticket JSON carries the lower-cased generation id")
	require.Equal(t, int64(8), seededSource)
	require.Equal(t, int64(1), seededVersion)
	require.Equal(t, "newest-state", seededState)

	var exists bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT to_regclass('public.codex_shared_tickets') IS NOT NULL`).Scan(&exists))
	require.True(t, exists)
}

func TestMigration248PerAccountInvalidationUniqueness(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE codex_ticket_invalidations (
		id BIGSERIAL PRIMARY KEY,
		account_id BIGINT NOT NULL,
		ticket_generation_id UUID NOT NULL UNIQUE
	) ON COMMIT DROP`)
	require.NoError(t, err)
	migration, err := dbmigrations.FS.ReadFile("248_codex_ticket_per_account_invalidation.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	generation := uuid.NewString()
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_ticket_invalidations(account_id, ticket_generation_id) VALUES (1, $1), (2, $1)`, generation)
	require.NoError(t, err, "different accounts may invalidate the same generation")
	_, err = tx.ExecContext(ctx, `SAVEPOINT duplicate`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_ticket_invalidations(account_id, ticket_generation_id) VALUES (1, $1)`, generation)
	require.Error(t, err, "one account invalidates a generation once")
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT duplicate`)
	require.NoError(t, err)
}

func TestCodexTicketInvalidationConcurrentLifecycle(t *testing.T) {
	ctx := context.Background()
	model := "gpt-6-astra"
	resetCodexSharedTicket(t)
	old, fresh := uuid.NewString(), uuid.NewString()
	var id, peer int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,extra) VALUES ('ticket-lifecycle-test','openai','oauth','{"codex_allow_without_ticket":false}'::jsonb) RETURNING id`).Scan(&id))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,extra) VALUES ('ticket-lifecycle-peer','openai','oauth','{}'::jsonb) RETURNING id`).Scan(&peer))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM codex_ticket_invalidations WHERE account_id IN ($1,$2)`, id, peer)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id IN ($1,$2)`, id, peer)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id IN ($1,$2)`, id, peer)
	})
	repo := &codexTicketAttemptRepository{db: integrationDB}
	store := func(generation string) {
		raw, err := json.Marshal(map[string]any{"account_id": id, "model": model, "generation_id": generation, "state": "state-" + generation, "cookie": "__oailb=" + generation})
		require.NoError(t, err)
		require.NoError(t, repo.StoreTicket(ctx, generation, id, time.Now().UTC(), raw))
	}
	store(old)
	originalTicket, returnedTicket := "state-"+old, "returned-state"
	event := service.CodexTicketInvalidation{AccountID: id, Model: model, TicketGenerationID: old, OccurredAt: time.Now().UTC(), ReasonCode: service.CodexTicketInvalidationCredentialsChanged, RequestKind: "user_request", RequestRoute: "/v1/responses",
		OriginalTicket: &originalTicket, ReturnedTicket: &returnedTicket}
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
	require.Equal(t, 1, count, "one account records a generation once")
	state, err := repo.CurrentTicket(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, state.Ticket, "the shared ticket survives for other accounts")
	require.Equal(t, []int64{id}, state.InvalidatedAccountIDs)
	var returned sql.NullString
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*), max(returned_ticket) FROM codex_ticket_invalidations WHERE account_id=$1`, id).Scan(&count, &returned))
	require.Equal(t, 1, count)
	require.True(t, returned.Valid)
	require.Equal(t, returnedTicket, returned.String)
	var extra []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT extra FROM accounts WHERE id=$1`, id).Scan(&extra))
	var settings map[string]any
	require.NoError(t, json.Unmarshal(extra, &settings))
	require.Equal(t, false, settings["codex_allow_without_ticket"], "account settings are untouched")

	// A second account invalidating the same generation gets its own record.
	peerEvent := event
	peerEvent.AccountID = peer
	ok, err := repo.Invalidate(ctx, &peerEvent)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, peerEvent.InvalidatedCurrent)
	state, err = repo.CurrentTicket(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{id, peer}, state.InvalidatedAccountIDs)

	// A new generation starts with no invalidations; a late response for an older one is only recorded.
	store(fresh)
	state, err = repo.CurrentTicket(ctx)
	require.NoError(t, err)
	require.Empty(t, state.InvalidatedAccountIDs)
	event.TicketGenerationID = uuid.NewString()
	ok, err = repo.Invalidate(ctx, &event)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, event.InvalidatedCurrent)
	var remaining string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT generation_id::text FROM codex_shared_tickets WHERE id=1`).Scan(&remaining))
	require.Equal(t, fresh, remaining)
}
