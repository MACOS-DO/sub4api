//go:build integration

package repository

import (
	"context"
	"testing"

	dbent "github.com/MACOS-DO/sub4api/ent"
	dbmigrations "github.com/MACOS-DO/sub4api/migrations"
	"github.com/stretchr/testify/require"
)

const (
	migration241Name             = "241_add_typesafe_platform.sql"
	migration241UpstreamChecksum = "b6559525bf8d0b5d7c617e7415944f0d1fae8c408e9131ace84139795f66dcd2"
	migration241Sub4APIChecksum  = "6034ab0415266a5299a64d49f56b52dab8c61a3c5b2eaa82327c81cc5698a871"
)

func execMigrationFile(t *testing.T, ctx context.Context, client *dbent.Client, name string) error {
	t.Helper()
	content, err := dbmigrations.FS.ReadFile(name)
	require.NoError(t, err)
	_, err = client.ExecContext(ctx, string(content))
	return err
}

func migration241Checksum(t *testing.T, ctx context.Context, client *dbent.Client) (string, bool) {
	t.Helper()
	rows, err := client.QueryContext(ctx, `SELECT checksum FROM schema_migrations WHERE filename = $1`, migration241Name)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		require.NoError(t, rows.Err())
		return "", false
	}
	var checksum string
	require.NoError(t, rows.Scan(&checksum))
	return checksum, true
}

// 还原 1.1.5–1.1.9 的状态：241 未应用，平台约束为 245 版本（含 openai_bps、不含 typesafe）。
func restorePre241Sub4APIState(t *testing.T, ctx context.Context, client *dbent.Client) {
	t.Helper()
	for _, stmt := range []string{
		`DELETE FROM schema_migrations WHERE filename = '241_add_typesafe_platform.sql'`,
		`ALTER TABLE user_platform_quotas DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check`,
		`ALTER TABLE user_platform_quotas ADD CONSTRAINT user_platform_quotas_platform_check
			CHECK (platform IN ('anthropic', 'openai', 'openai_bps', 'gemini', 'antigravity', 'grok',
			                    'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'))`,
		`ALTER TABLE composite_model_routes DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check`,
		`ALTER TABLE composite_model_routes ADD CONSTRAINT composite_model_routes_target_platform_check
			CHECK (target_platform IN ('anthropic', 'openai', 'openai_bps', 'gemini', 'antigravity', 'grok',
			                           'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'))`,
	} {
		_, err := client.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}
}

// 1.1.5–1.1.9 且已有 openai_bps 行：上游 241 会校验失败，241_0 将其登记为已应用，
// 随后 242/246 删除约束，openai_bps 数据保留。
func TestSub4API241_SkipsUpstream241WhenOpenAIBPSRowsExist(t *testing.T) {
	ctx := context.Background()
	client := testEntTx(t).Client()
	restorePre241Sub4APIState(t, ctx, client)

	userID := mustCreateUserForQuota(t, client)
	_, err := client.ExecContext(ctx, `
INSERT INTO user_platform_quotas (user_id, platform, daily_limit_usd, daily_usage_usd, weekly_usage_usd, monthly_usage_usd, created_at, updated_at)
VALUES ($1, 'openai_bps', 1, 0, 0, 0, NOW(), NOW())`, userID)
	require.NoError(t, err)

	// 不做处理时上游 241 确实会失败。
	_, err = client.ExecContext(ctx, `SAVEPOINT before_241`)
	require.NoError(t, err)
	require.Error(t, execMigrationFile(t, ctx, client, migration241Name))
	_, err = client.ExecContext(ctx, `ROLLBACK TO SAVEPOINT before_241`)
	require.NoError(t, err)

	require.NoError(t, execMigrationFile(t, ctx, client, "241_0_sub4api_skip_typesafe_constraint_for_openai_bps.sql"))
	checksum, ok := migration241Checksum(t, ctx, client)
	require.True(t, ok, "241 must be recorded so the runner skips it")
	require.Equal(t, migration241UpstreamChecksum, checksum)

	require.NoError(t, execMigrationFile(t, ctx, client, "242_drop_platform_check_constraints.sql"))
	require.NoError(t, execMigrationFile(t, ctx, client, "246_redrop_platform_check_constraints.sql"))
	require.NoError(t, execMigrationFile(t, ctx, client, "250_sub4api_normalize_241_checksum.sql"))
	require.False(t, platformConstraintExists(t, ctx, client, "user_platform_quotas", "user_platform_quotas_platform_check"))
	require.False(t, platformConstraintExists(t, ctx, client, "composite_model_routes", "composite_model_routes_target_platform_check"))

	rows, err := client.QueryContext(ctx,
		`SELECT COUNT(*) FROM user_platform_quotas WHERE user_id = $1 AND platform = 'openai_bps'`, userID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next())
	var count int
	require.NoError(t, rows.Scan(&count))
	require.Equal(t, 1, count)
}

// 1.1.5–1.1.9 但没有 openai_bps 行（以及新库）：241_0 不登记，上游 241 正常执行。
func TestSub4API241_RunsUpstream241WithoutOpenAIBPSRows(t *testing.T) {
	ctx := context.Background()
	client := testEntTx(t).Client()
	restorePre241Sub4APIState(t, ctx, client)

	require.NoError(t, execMigrationFile(t, ctx, client, "241_0_sub4api_skip_typesafe_constraint_for_openai_bps.sql"))
	_, ok := migration241Checksum(t, ctx, client)
	require.False(t, ok)

	require.NoError(t, execMigrationFile(t, ctx, client, migration241Name))
	require.True(t, platformConstraintExists(t, ctx, client, "user_platform_quotas", "user_platform_quotas_platform_check"))
}

// 1.1.10/1.1.11：241 已以 Sub4API 改动版 checksum 应用；241_0 不做任何事，250 归一为上游 checksum 且可重入。
func TestSub4API241_NormalizesHistoricalChecksum(t *testing.T) {
	ctx := context.Background()
	client := testEntTx(t).Client()
	_, err := client.ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = $1 WHERE filename = $2`, migration241Sub4APIChecksum, migration241Name)
	require.NoError(t, err)

	require.NoError(t, execMigrationFile(t, ctx, client, "241_0_sub4api_skip_typesafe_constraint_for_openai_bps.sql"))
	checksum, ok := migration241Checksum(t, ctx, client)
	require.True(t, ok)
	require.Equal(t, migration241Sub4APIChecksum, checksum)

	for range 2 {
		require.NoError(t, execMigrationFile(t, ctx, client, "250_sub4api_normalize_241_checksum.sql"))
		checksum, ok = migration241Checksum(t, ctx, client)
		require.True(t, ok)
		require.Equal(t, migration241UpstreamChecksum, checksum)
	}
}

// 端到端：1.1.10/1.1.11 的库（241 记录为 Sub4API 改动版 checksum，241_0 / 250 未应用）
// 经真实 runner 启动不报 checksum mismatch，并归一为上游 checksum。
// 直接操作共享库，结束时状态与初始迁移后一致。
func TestSub4API241_ApplyMigrationsUpgradesFrom1110(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `
DELETE FROM schema_migrations WHERE filename IN (
    '241_0_sub4api_skip_typesafe_constraint_for_openai_bps.sql',
    '250_sub4api_normalize_241_checksum.sql')`)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = $1 WHERE filename = $2`, migration241Sub4APIChecksum, migration241Name)
	require.NoError(t, err)

	require.NoError(t, ApplyMigrations(ctx, integrationDB))

	var checksum string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT checksum FROM schema_migrations WHERE filename = $1`, migration241Name).Scan(&checksum))
	require.Equal(t, migration241UpstreamChecksum, checksum)
	require.NoError(t, ApplyMigrations(ctx, integrationDB))
}
