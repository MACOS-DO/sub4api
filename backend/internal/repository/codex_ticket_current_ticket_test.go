package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketCurrentTicket(t *testing.T) {
	queryErr := errors.New("database unavailable")
	for _, test := range []struct {
		name    string
		raw     string
		noRows  bool
		wantErr error
	}{
		{name: "missing key normalized by SQL", raw: "null"},
		{name: "stored ticket", raw: `{"generation_id":"test-generation","state":"saved-state"}`},
		{name: "account not found", noRows: true},
		{name: "database error", wantErr: queryErr},
		{name: "canceled query", wantErr: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			query := mock.ExpectQuery(`SELECT COALESCE\(extra->\$2, 'null'::jsonb\) FROM accounts`).
				WithArgs(int64(7), "codex_turn_ticket:gpt-6-astra")
			if test.wantErr != nil {
				query.WillReturnError(test.wantErr)
			} else {
				rows := sqlmock.NewRows([]string{"ticket"})
				if !test.noRows {
					rows.AddRow([]byte(test.raw))
				}
				query.WillReturnRows(rows)
			}
			repo := &codexTicketAttemptRepository{db: db}
			raw, err := repo.CurrentTicket(context.Background(), 7, "gpt-6-astra")
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, test.raw, string(raw))
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
