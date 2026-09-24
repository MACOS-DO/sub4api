package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/DATA-DOG/go-sqlmock"
	dbmigrations "github.com/MACOS-DO/sub4api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration244TicketDisableRunsOnce(t *testing.T) {
	const name = "244_disable_codex_ticket_harvesting.sql"
	data, err := dbmigrations.FS.ReadFile(name)
	require.NoError(t, err)
	content := strings.TrimSpace(string(data))
	sum := sha256.Sum256([]byte(content))
	checksum := hex.EncodeToString(sum[:])
	fsys := fstest.MapFS{name: &fstest.MapFile{Data: data}}
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	for _, applied := range []bool{false, true} {
		prepareMigrationsBootstrapExpectations(mock)
		query := mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE filename = $1")).WithArgs(name)
		if applied {
			// A later startup must not run the reset SQL, even after an admin re-enables harvesting.
			query.WillReturnRows(sqlmock.NewRows([]string{"checksum"}).AddRow(checksum))
		} else {
			query.WillReturnError(sql.ErrNoRows)
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta(content)).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)")).
				WithArgs(name, checksum).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()
		}
		mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
			WithArgs(migrationsAdvisoryLockID).WillReturnResult(sqlmock.NewResult(0, 1))
		require.NoError(t, applyMigrationsFS(context.Background(), db, fsys))
		require.NoError(t, mock.ExpectationsWereMet())
	}
}

func TestMigration244TicketDisableRollsBackWithoutLedgerRecord(t *testing.T) {
	const name = "244_disable_codex_ticket_harvesting.sql"
	data, err := dbmigrations.FS.ReadFile(name)
	require.NoError(t, err)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	prepareMigrationsBootstrapExpectations(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE filename = $1")).
		WithArgs(name).WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(strings.TrimSpace(string(data)))).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)")).
		WithArgs(name, sqlmock.AnyArg()).WillReturnError(errors.New("ledger write failed"))
	mock.ExpectRollback()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(migrationsAdvisoryLockID).WillReturnResult(sqlmock.NewResult(0, 1))
	err = applyMigrationsFS(context.Background(), db, fstest.MapFS{name: &fstest.MapFile{Data: data}})
	require.ErrorContains(t, err, "ledger write failed")
	require.NoError(t, mock.ExpectationsWereMet())
}
