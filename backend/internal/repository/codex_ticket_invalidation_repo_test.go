package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketInvalidationTransaction(t *testing.T) {
	for _, scenario := range []string{"current", "late response", "no shared ticket", "duplicate", "lock failure", "insert failure", "commit failure"} {
		t.Run(scenario, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			repo := &codexTicketAttemptRepository{db: db}
			returnedTicket := "new-state"
			event := &service.CodexTicketInvalidation{AccountID: 7, Model: "gpt-6-astra", TicketGenerationID: "11111111-1111-4111-8111-111111111111", OccurredAt: time.Now().UTC(), ReasonCode: service.CodexTicketInvalidationCredentialsChanged, RequestKind: "user_request", RequestRoute: "/v1/responses", ReturnedTicket: &returnedTicket}
			// The database compares the ids as UUIDs and reports whether the row holds this generation.
			current := scenario != "late response"
			mock.ExpectBegin()
			lock := mock.ExpectQuery(`SELECT generation_id = \$1::uuid FROM codex_shared_tickets WHERE id=1 FOR UPDATE`).WithArgs(event.TicketGenerationID)
			switch scenario {
			case "lock failure":
				lock.WillReturnError(errors.New("lock failed"))
				mock.ExpectRollback()
			case "no shared ticket":
				lock.WillReturnRows(sqlmock.NewRows([]string{"current"}))
			default:
				lock.WillReturnRows(sqlmock.NewRows([]string{"current"}).AddRow(current))
			}
			invalidates := scenario != "late response" && scenario != "no shared ticket"
			if scenario != "lock failure" {
				args := []driver.Value{event.AccountID, event.Model, event.TicketGenerationID, event.OccurredAt, nil, event.RequestKind, event.RequestRoute, invalidates, nil, nil, returnedTicket, nil, nil, event.ReasonCode}
				insert := mock.ExpectQuery(`INSERT INTO codex_ticket_invalidations.*VALUES \(\$1,\$2,\$3,\$4,\$14,.*ON CONFLICT \(account_id, ticket_generation_id\) DO NOTHING RETURNING id`).WithArgs(args...)
				switch scenario {
				case "insert failure":
					insert.WillReturnError(errors.New("insert failed"))
					mock.ExpectRollback()
				case "duplicate":
					// The same account reporting the same generation again changes nothing.
					insert.WillReturnRows(sqlmock.NewRows([]string{"id"}))
					mock.ExpectCommit()
				case "commit failure":
					insert.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
					expectCodexTicketVersionBump(mock)
					mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
				default:
					// The shared ticket row is never deleted: other accounts keep using it.
					insert.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
					if invalidates {
						expectCodexTicketVersionBump(mock)
					}
					mock.ExpectCommit()
				}
			}
			inserted, err := repo.Invalidate(context.Background(), event)
			switch scenario {
			case "lock failure", "insert failure", "commit failure":
				require.Error(t, err)
				require.False(t, inserted)
				require.False(t, event.InvalidatedCurrent)
			default:
				require.NoError(t, err)
				require.Equal(t, scenario != "duplicate", inserted)
				require.Equal(t, scenario == "current", event.InvalidatedCurrent)
				if inserted {
					require.Equal(t, int64(1), event.ID)
				}
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// An invalidation of the current generation changes the cached state, so it bumps the row version.
func expectCodexTicketVersionBump(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`UPDATE codex_shared_tickets SET version=version\+1 WHERE id=1`).WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestCodexTicketInvalidationRejectsIncompleteEvents(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := &codexTicketAttemptRepository{db: db}
	returned, empty := "new-state", ""
	generation := "11111111-1111-4111-8111-111111111111"
	for _, event := range []*service.CodexTicketInvalidation{nil,
		{Model: "gpt-6-astra", ReturnedTicket: &returned},
		{TicketGenerationID: generation, ReturnedTicket: &returned},
		// The returned turn-state is the evidence of the invalidation and must be present.
		{Model: "gpt-6-astra", TicketGenerationID: generation},
		{Model: "gpt-6-astra", TicketGenerationID: generation, ReturnedTicket: &empty},
	} {
		inserted, err := repo.Invalidate(context.Background(), event)
		require.Error(t, err)
		require.False(t, inserted)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}
