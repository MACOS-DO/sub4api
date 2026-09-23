package repository

import (
	"context"
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
		WithArgs(int64(7), start, end, "", "all").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(`WITH events AS`).
		WithArgs(int64(7), start, end, "", "all", 20, 0).
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
