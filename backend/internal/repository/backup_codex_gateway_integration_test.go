//go:build integration

package repository

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newContainerRestoreDumper() *PgDumper {
	return &PgDumper{cfg: &config.DatabaseConfig{Host: "127.0.0.1", Port: 5432, User: "postgres", Password: "postgres", DBName: "sub2api_test", SSLMode: "disable"}, db: integrationDB,
		commandContext: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			prefix := []string{"exec", "-i", "-e", "PGPASSWORD=postgres", integrationPGContainerID, name}
			return exec.CommandContext(ctx, "docker", append(prefix, args...)...)
		}}
}

func TestCodexGatewayRestoreRefusesEitherWriterLock(t *testing.T) {
	ctx := context.Background()
	d := newContainerRestoreDumper()
	for _, id := range []int64{7161981693156876665, migrationsAdvisoryLockID} {
		conn, err := integrationDB.Conn(ctx)
		require.NoError(t, err)
		_, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", id)
		require.NoError(t, err)
		require.Error(t, d.Restore(ctx, strings.NewReader("SELECT 1;")))
		_, err = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", id)
		require.NoError(t, err)
		require.NoError(t, conn.Close())
	}
}

func TestCodexGatewayRestoreHoldsLocksUntilSQLFinishes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := newContainerRestoreDumper()
	reader, err := d.Dump(ctx)
	require.NoError(t, err)
	backup, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	done := make(chan error, 1)
	go func() { done <- d.Restore(ctx, strings.NewReader("SELECT pg_sleep(2);\n"+string(backup))) }()
	conn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	require.Eventually(t, func() bool {
		var acquired bool
		if conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", int64(7161981693156876665)).Scan(&acquired) != nil {
			return false
		}
		if acquired {
			_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", int64(7161981693156876665))
		}
		return !acquired
	}, time.Second, 20*time.Millisecond)
	var available bool
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", migrationsAdvisoryLockID).Scan(&available))
	if available {
		_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationsAdvisoryLockID)
	}
	require.False(t, available, "migration lock must remain held during restore")
	require.NoError(t, <-done)
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", int64(7161981693156876665)).Scan(&available))
	require.True(t, available)
	_, err = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", int64(7161981693156876665))
	require.NoError(t, err)
}

func TestCodexGatewayRestoreSQLFailureRollsBackAndDoesNotExposeData(t *testing.T) {
	name := "restore_probe_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	data := fmt.Sprintf("CREATE TABLE %s(id int); SELECT 'sensitive_restore_value'::int;", name)
	err := newContainerRestoreDumper().Restore(context.Background(), strings.NewReader(data))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "sensitive_restore_value")
	var exists bool
	require.NoError(t, integrationDB.QueryRow("SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists))
	require.False(t, exists, "the restore must be atomic on SQL errors")
}
