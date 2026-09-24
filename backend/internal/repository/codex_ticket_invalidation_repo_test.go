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
	for _, scenario := range []string{"current", "late response", "duplicate", "insert failure", "delete failure", "commit failure"} {
		t.Run(scenario, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			repo := &codexTicketAttemptRepository{db: db}
			event := &service.CodexTicketInvalidation{AccountID: 7, Model: "gpt-6-astra", TicketGenerationID: "11111111-1111-4111-8111-111111111111", OccurredAt: time.Now().UTC(), ReasonCode: service.CodexTicketInvalidationCredentialsChanged, RequestKind: "user_request", RequestRoute: "/v1/responses", ReturnedTicket: "new-state"}
			current := event.TicketGenerationID
			if scenario == "late response" {
				current = "22222222-2222-4222-8222-222222222222"
			}
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT extra->\$2->>'generation_id'.*FOR UPDATE`).WithArgs(event.AccountID, "codex_turn_ticket:"+event.Model).WillReturnRows(sqlmock.NewRows([]string{"generation"}).AddRow(current))
			args := []driver.Value{event.AccountID, event.Model, event.TicketGenerationID, event.OccurredAt, nil, event.RequestKind, event.RequestRoute, current == event.TicketGenerationID, nil, nil, event.ReturnedTicket, nil, nil, event.ReasonCode}
			insert := mock.ExpectQuery(`INSERT INTO codex_ticket_invalidations.*VALUES \(\$1,\$2,\$3,\$4,\$14,.*ON CONFLICT \(ticket_generation_id\) DO NOTHING RETURNING id`).WithArgs(args...)
			switch scenario {
			case "insert failure":
				insert.WillReturnError(errors.New("insert failed"))
				mock.ExpectRollback()
			case "duplicate":
				insert.WillReturnRows(sqlmock.NewRows([]string{"id"}))
				mock.ExpectExec(`UPDATE codex_ticket_invalidations SET.*WHERE ticket_generation_id=\$1 AND occurred_at>\$2`).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectCommit()
			default:
				insert.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
				if scenario != "late response" {
					deleteTicket := mock.ExpectExec(`UPDATE accounts SET extra=COALESCE\(extra,'\{\}'::jsonb\)-\$2, updated_at=NOW\(\) WHERE id=\$1`).WithArgs(event.AccountID, "codex_turn_ticket:"+event.Model)
					if scenario == "delete failure" {
						deleteTicket.WillReturnError(errors.New("delete failed"))
					} else {
						deleteTicket.WillReturnResult(sqlmock.NewResult(0, 1))
					}
				}
				if scenario == "delete failure" {
					mock.ExpectRollback()
				} else if scenario == "commit failure" {
					mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
				} else {
					mock.ExpectCommit()
				}
			}
			inserted, err := repo.Invalidate(context.Background(), event)
			if scenario == "insert failure" || scenario == "delete failure" || scenario == "commit failure" {
				require.Error(t, err)
				require.False(t, inserted)
				require.False(t, event.InvalidatedCurrent)
			} else {
				require.NoError(t, err)
				require.Equal(t, scenario != "duplicate", inserted)
				require.Equal(t, scenario == "current", event.InvalidatedCurrent)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
