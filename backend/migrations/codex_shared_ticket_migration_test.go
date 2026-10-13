package migrations

import (
	"regexp"
	"strings"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMigration247CreatesSharedTickets(t *testing.T) {
	content, err := FS.ReadFile("247_codex_shared_ticket.sql")
	require.NoError(t, err)
	sql := string(content)

	table := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS codex_shared_tickets \((.*?)\);`).FindStringSubmatch(sql)
	require.Len(t, table, 2, "the shared ticket table must be created idempotently")
	columns := table[1]
	require.NotContains(t, columns, "model", "one global ticket serves every model")
	for _, column := range []string{
		`id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1)`,
		`generation_id UUID NOT NULL`,
		`source_account_id BIGINT NOT NULL`,
		`ticket JSONB NOT NULL`,
		`captured_at TIMESTAMPTZ NOT NULL`,
		`version BIGINT NOT NULL DEFAULT 1`,
		`updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`,
	} {
		require.Contains(t, columns, column)
	}

	// Invalidations keep the reason codes and NOT NULL returned turn-state from migration 243.
	require.NotContains(t, sql, "codex_ticket_invalidations")
	require.NotContains(t, sql, "reason_code")
	require.NotContains(t, sql, "returned_ticket")
	require.NotContains(t, sql, "DROP NOT NULL")
}

func TestMigration247RetiresPerAccountTickets(t *testing.T) {
	content, err := FS.ReadFile("247_codex_shared_ticket.sql")
	require.NoError(t, err)
	sql := string(content)
	cleanup := regexp.MustCompile(`(?s)UPDATE accounts\s+SET extra = (.*?);`).FindStringSubmatch(sql)
	require.Len(t, cleanup, 2, "per-account ticket keys must be removed")
	statement := cleanup[1]
	require.Contains(t, statement, "(extra - 'codex_ticket_harvest_models')\n    - ARRAY(SELECT key FROM jsonb_object_keys(extra) AS key WHERE key LIKE 'codex_turn_ticket:%')")
	require.Contains(t, statement, "jsonb_typeof(extra) = 'object'", "non-object extra values must not break the migration")
	// The row filter must not call jsonb_object_keys: Postgres may evaluate it before the type guard.
	require.Contains(t, statement, `extra::text LIKE '%"codex_turn_ticket:%' OR extra::text LIKE '%"codex_ticket_harvest_models"%'`,
		"only accounts that still hold tickets or per-model switches are rewritten")
	require.NotContains(t, statement, "EXISTS (")
	require.True(t, service.IsOpenAICodexTicketExtraKey("codex_turn_ticket:gpt-6-astra"))
	// The retired per-model switches go too; account-level participation and policy stay untouched.
	for _, kept := range []string{"codex_ticket_harvest_enabled", "codex_allow_without_ticket"} {
		require.NotContains(t, statement, kept)
	}
}

func TestMigration247SeedsSharedTicketsFromNewestValidAccountTicket(t *testing.T) {
	content, err := FS.ReadFile("247_codex_shared_ticket.sql")
	require.NoError(t, err)
	sql := string(content)
	seed := regexp.MustCompile(`(?s)INSERT INTO codex_shared_tickets \(id, generation_id, source_account_id, ticket, captured_at, version\)(.*?ON CONFLICT \(id\) DO NOTHING);`).FindStringSubmatch(sql)
	require.Len(t, seed, 2, "old per-account tickets must seed the shared table")
	statement := seed[1]
	cleanupAt := strings.Index(sql, "UPDATE accounts")
	require.Positive(t, cleanupAt)
	require.Less(t, strings.Index(sql, seed[0]), cleanupAt, "tickets are copied before the per-account keys are removed")

	// Only the newest ticket of any model survives, and an existing global ticket is never overwritten.
	require.Contains(t, statement, "ORDER BY candidate.captured_at DESC, candidate.account_id\nLIMIT 1")
	require.Contains(t, statement, "ON CONFLICT (id) DO NOTHING")
	require.NotContains(t, statement, "DISTINCT ON")
	// The generation id is stored lower-cased in both the column and the ticket JSON.
	require.Contains(t, statement, "jsonb_build_object('account_id', candidate.account_id, 'model', candidate.model, 'generation_id', candidate.generation_id)")
	require.Contains(t, statement, "THEN lower(e.value ->> 'generation_id') END AS generation_id")
	require.Contains(t, statement, "substr(e.key, length('codex_turn_ticket:') + 1) AS model")

	// Structural validity: a ticket needs harvest metadata, a turn-state and an __oailb cookie.
	for _, rule := range []string{
		"a.deleted_at IS NULL",
		"e.key LIKE 'codex_turn_ticket:_%'",
		"jsonb_typeof(e.value) = 'object'",
		"e.value ->> 'verification_method' = 'modeltrace_v1'",
		"btrim(e.value ->> 'fingerprint_commit') <> ''",
		"AND jsonb_typeof(e.value -> 'state') = 'string' AND btrim(e.value ->> 'state') <> ''",
		"AND jsonb_typeof(e.value -> 'cookie') = 'string' AND e.value ->> 'cookie' ~ '(^|;)\\s*__oailb=[^;]'",
		"candidate.generation_id IS NOT NULL",
		"candidate.captured_at IS NOT NULL",
	} {
		require.Contains(t, statement, rule)
	}

	// Malformed rows must be skipped, never abort the migration: extra is only expanded when it is an
	// object, the uuid cast only sees regex-validated values, and captured_at is parsed defensively.
	require.Contains(t, statement, "jsonb_each(CASE WHEN jsonb_typeof(a.extra) = 'object' THEN a.extra ELSE '{}'::jsonb END)")
	require.Regexp(t, `(?s)CASE WHEN jsonb_typeof\(e\.value -> 'generation_id'\) = 'string'\s+AND e\.value ->> 'generation_id' ~ '\^\[0-9A-Fa-f\]\{8\}`, statement)
	require.Contains(t, statement, "pg_temp.codex_shared_ticket_try_timestamptz(e.value ->> 'captured_at')")
	require.NotContains(t, statement, "::timestamptz", "captured_at is never cast outside the guarded helper")
	helper := regexp.MustCompile(`(?s)CREATE FUNCTION pg_temp\.codex_shared_ticket_try_timestamptz\(value TEXT\) RETURNS TIMESTAMPTZ(.*?)\$\$;`).FindStringSubmatch(sql)
	require.Len(t, helper, 2)
	require.Regexp(t, `EXCEPTION WHEN others THEN\s+RETURN NULL;`, helper[1], "impossible dates such as 2026-02-30 are skipped")
	require.Contains(t, helper[1], `value !~ '^\d{4}-\d{2}-\d{2}[T ]`, "special inputs such as 'now' or 'infinity' are rejected")
	require.Contains(t, sql, "DROP FUNCTION pg_temp.codex_shared_ticket_try_timestamptz(TEXT);")
}
