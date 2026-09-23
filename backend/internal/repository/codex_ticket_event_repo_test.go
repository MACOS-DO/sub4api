package repository

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketEventsExposeMetadataOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := &codexTicketAttemptRepository{db: db}
	end := time.Now().UTC()
	start := end.Add(-90 * 24 * time.Hour)
	mock.ExpectQuery(`SELECT\s+\(SELECT COUNT\(\*\) FROM codex_ticket_attempts`).
		WithArgs(int64(7), start, end, "").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(`WITH events AS`).
		WithArgs(int64(7), start, end, "", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow(`{"id":3,"kind":"invalidation","model":"gpt-6-astra","original_ticket_present":true,"returned_cookie_present":true}`))
	items, total, err := repo.ListEvents(context.Background(), 7, "", "all", start, end, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	require.Equal(t, true, items[0]["original_ticket_present"])
	require.NotContains(t, items[0], "original_ticket")
	require.NotContains(t, items[0], "returned_ticket")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCodexTicketLatestEventsBatched(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := &codexTicketAttemptRepository{db: db}
	events, err := repo.LatestEvents(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, events)
	occurred := time.Now().UTC()
	mock.ExpectQuery(`SELECT DISTINCT ON \(account_id\)`).WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "model", "kind", "occurred_at"}).
			AddRow(int64(7), "gpt-6-astra", "invalidation", occurred))
	events, err = repo.LatestEvents(context.Background(), []int64{7, 8})
	require.NoError(t, err)
	require.Equal(t, "invalidation", events[7].Kind)
	require.True(t, events[7].OccurredAt.Equal(occurred))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCodexTicketAttemptEventsExcludeInvalidations(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := &codexTicketAttemptRepository{db: db}
	end := time.Now().UTC()
	start := end.Add(-90 * 24 * time.Hour)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM codex_ticket_attempts WHERE account_id=\$1`).
		WithArgs(int64(7), start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT to_jsonb\(a\).*FROM \(\s*SELECT \* FROM codex_ticket_attempts`).
		WithArgs(int64(7), start, end, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow(`{"id":4,"kind":"miss","model":"gpt-5.6-sol"}`))
	items, total, err := repo.ListEvents(context.Background(), 7, "", "attempts", start, end, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	require.Equal(t, "miss", items[0]["kind"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCodexTicketFilteredEventsModelAndOutcome(t *testing.T) {
	for _, test := range []struct {
		filter    string
		model     string
		condition string
		args      []driver.Value
	}{
		{filter: "success", model: "gpt-6-astra", condition: `AND model=\$4 AND outcome='success'`, args: []driver.Value{"gpt-6-astra"}},
		{filter: "failure", condition: `AND outcome IN \('miss','error'\)`},
	} {
		t.Run(test.filter, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			repo := &codexTicketAttemptRepository{db: db}
			end := time.Now().UTC()
			start := end.Add(-90 * 24 * time.Hour)
			args := []driver.Value{int64(7), start, end}
			args = append(args, test.args...)
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM codex_ticket_attempts WHERE .*` + test.condition).
				WithArgs(args...).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
			pageArgs := append(append([]driver.Value(nil), args...), 20, 20)
			mock.ExpectQuery(`SELECT to_jsonb\(a\).*FROM \(\s*SELECT \* FROM codex_ticket_attempts WHERE .*` + test.condition).
				WithArgs(pageArgs...).WillReturnRows(sqlmock.NewRows([]string{"payload"}))
			items, total, err := repo.ListEvents(context.Background(), 7, test.model, test.filter, start, end, 2, 20)
			require.NoError(t, err)
			require.Zero(t, total)
			require.Empty(t, items)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCodexTicketInvalidationEventsDoNotSelectCredentialValues(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repo := &codexTicketAttemptRepository{db: db}
	end := time.Now().UTC()
	start := end.Add(-90 * 24 * time.Hour)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM codex_ticket_invalidations WHERE account_id=\$1`).
		WithArgs(int64(7), start, end, "gpt-6-astra").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT jsonb_build_object\('id',i.id.*FROM \(\s*SELECT \* FROM codex_ticket_invalidations`).
		WithArgs(int64(7), start, end, "gpt-6-astra", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow(`{"id":3,"kind":"invalidation","original_ticket_present":true}`))
	items, total, err := repo.ListEvents(context.Background(), 7, "gpt-6-astra", "invalidation", start, end, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, true, items[0]["original_ticket_present"])
	require.NotContains(t, items[0], "original_ticket")
	require.NotContains(t, items[0], "returned_ticket")
	require.NoError(t, mock.ExpectationsWereMet())
}
