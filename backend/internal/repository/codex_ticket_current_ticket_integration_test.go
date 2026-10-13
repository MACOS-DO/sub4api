//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// resetCodexSharedTicket clears the single global ticket row for a test and again afterwards.
func resetCodexSharedTicket(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `DELETE FROM codex_shared_tickets`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM codex_shared_tickets`)
		require.NoError(t, err)
	})
}

func TestCodexTicketCurrentTicketPostgres(t *testing.T) {
	ctx := context.Background()
	resetCodexSharedTicket(t)
	repo := &codexTicketAttemptRepository{db: integrationDB}

	state, err := repo.CurrentTicket(ctx)
	require.NoError(t, err, "a missing ticket must reach the gateway's no-ticket policy")
	require.Empty(t, state.Ticket)

	first := json.RawMessage(`{"account_id":7,"generation_id":"` + uuid.NewString() + `","cookie":"__oailb=first"}`)
	var firstTicket struct {
		GenerationID string `json:"generation_id"`
	}
	require.NoError(t, json.Unmarshal(first, &firstTicket))
	require.NoError(t, repo.StoreTicket(ctx, firstTicket.GenerationID, 7, time.Now().UTC(), first))
	state, err = repo.CurrentTicket(ctx)
	require.NoError(t, err)
	require.JSONEq(t, string(first), string(state.Ticket))
	require.Empty(t, state.InvalidatedAccountIDs)

	secondGeneration := uuid.NewString()
	second := json.RawMessage(`{"account_id":8,"generation_id":"` + secondGeneration + `","cookie":"__oailb=second"}`)
	require.NoError(t, repo.StoreTicket(ctx, secondGeneration, 8, time.Now().UTC(), second))
	state, err = repo.CurrentTicket(ctx)
	require.NoError(t, err)
	require.JSONEq(t, string(second), string(state.Ticket), "the latest harvest replaces the global ticket")
	var source int64
	var generation string
	var rows int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*), max(source_account_id), max(generation_id::text) FROM codex_shared_tickets`).Scan(&rows, &source, &generation))
	require.Equal(t, 1, rows, "there is only one global ticket")
	require.Equal(t, int64(8), source)
	require.Equal(t, secondGeneration, generation)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO codex_shared_tickets (id,generation_id,source_account_id,ticket,captured_at) VALUES (2,$1,1,'{}',NOW())`, uuid.NewString())
	require.Error(t, err, "the table holds a single row")
}

// Generation ids are compared as UUIDs: a ticket whose JSON carries an upper-case id
// still invalidates its current generation.
func TestCodexTicketInvalidationMatchesGenerationCaseInsensitively(t *testing.T) {
	ctx := context.Background()
	resetCodexSharedTicket(t)
	repo := &codexTicketAttemptRepository{db: integrationDB}
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,extra) VALUES ('ticket-case-test','openai','oauth','{}'::jsonb) RETURNING id`).Scan(&id))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM codex_ticket_invalidations WHERE account_id=$1`, id)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, id)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, id)
	})
	upper := strings.ToUpper(uuid.NewString())
	raw, err := json.Marshal(map[string]any{"account_id": id, "generation_id": upper, "state": "s", "cookie": "__oailb=x"})
	require.NoError(t, err)
	require.NoError(t, repo.StoreTicket(ctx, upper, id, time.Now().UTC(), raw))
	returned := "returned-state"
	event := &service.CodexTicketInvalidation{AccountID: id, Model: "gpt-6-astra", TicketGenerationID: upper, OccurredAt: time.Now().UTC(),
		ReasonCode: service.CodexTicketInvalidationCredentialsChanged, RequestKind: "user_request", RequestRoute: "/v1/responses", ReturnedTicket: &returned}
	inserted, err := repo.Invalidate(ctx, event)
	require.NoError(t, err)
	require.True(t, inserted)
	require.True(t, event.InvalidatedCurrent)
	state, err := repo.CurrentTicket(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{id}, state.InvalidatedAccountIDs)
	require.Equal(t, int64(2), state.Version, "invalidating the current generation bumps the version")
}
