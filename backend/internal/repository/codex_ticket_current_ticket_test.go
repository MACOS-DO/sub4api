package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketCurrentTicket(t *testing.T) {
	queryErr := errors.New("database unavailable")
	for _, test := range []struct {
		name        string
		raw         string
		invalidated string
		want        []int64
		noRows      bool
		wantErr     error
	}{
		{name: "stored ticket", raw: `{"generation_id":"test-generation","cookie":"__oailb=saved"}`, invalidated: "{}", want: []int64{}},
		{name: "per-account invalidations", raw: `{"generation_id":"test-generation","cookie":"__oailb=saved"}`, invalidated: "{3,9}", want: []int64{3, 9}},
		{name: "no shared ticket", noRows: true},
		{name: "database error", wantErr: queryErr},
		{name: "canceled query", wantErr: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			query := mock.ExpectQuery(`SELECT t.ticket,\s+ARRAY\(SELECT i.account_id FROM codex_ticket_invalidations i WHERE i.ticket_generation_id=t.generation_id ORDER BY i.account_id\),\s+t.version\s+FROM codex_shared_tickets t WHERE t.id=1`)
			if test.wantErr != nil {
				query.WillReturnError(test.wantErr)
			} else {
				rows := sqlmock.NewRows([]string{"ticket", "invalidated", "version"})
				if !test.noRows {
					rows.AddRow([]byte(test.raw), test.invalidated, int64(4))
				}
				query.WillReturnRows(rows)
			}
			repo := &codexTicketAttemptRepository{db: db}
			state, err := repo.CurrentTicket(context.Background())
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
				require.NotNil(t, state)
				if test.noRows {
					require.Empty(t, state.Ticket)
					require.Empty(t, state.InvalidatedAccountIDs)
					require.Zero(t, state.Version)
				} else {
					require.Equal(t, int64(4), state.Version)
					require.Equal(t, test.raw, string(state.Ticket))
					require.Equal(t, test.want, state.InvalidatedAccountIDs)
				}
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCodexTicketStoreTicketUpsertsSharedRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := &codexTicketAttemptRepository{db: db}
	capturedAt := time.Now().UTC()
	raw := json.RawMessage(`{"generation_id":"11111111-1111-4111-8111-111111111111","cookie":"__oailb=saved"}`)
	mock.ExpectExec(`INSERT INTO codex_shared_tickets \(id,generation_id,source_account_id,ticket,captured_at,updated_at\).*VALUES \(1,.*ON CONFLICT \(id\) DO UPDATE SET generation_id=EXCLUDED.generation_id, source_account_id=EXCLUDED.source_account_id.*version=codex_shared_tickets.version\+1`).
		WithArgs("11111111-1111-4111-8111-111111111111", int64(7), []byte(raw), capturedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.StoreTicket(context.Background(), "11111111-1111-4111-8111-111111111111", 7, capturedAt, raw))

	storeErr := errors.New("write failed")
	mock.ExpectExec(`INSERT INTO codex_shared_tickets`).WillReturnError(storeErr)
	require.ErrorIs(t, repo.StoreTicket(context.Background(), "11111111-1111-4111-8111-111111111111", 7, capturedAt, raw), storeErr)
	require.NoError(t, mock.ExpectationsWereMet())

	for _, invalid := range []struct {
		generation string
		raw        json.RawMessage
	}{{"", raw}, {"generation", nil}} {
		require.Error(t, repo.StoreTicket(context.Background(), invalid.generation, 7, capturedAt, invalid.raw))
	}
	require.NoError(t, mock.ExpectationsWereMet(), "invalid tickets never reach the database")
}
