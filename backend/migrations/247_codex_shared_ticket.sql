-- Codex tickets become one global turn-state + cookie ticket shared by every
-- participating account and injected on every gated model.
CREATE TABLE IF NOT EXISTS codex_shared_tickets (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    generation_id UUID NOT NULL,
    source_account_id BIGINT NOT NULL,
    ticket JSONB NOT NULL,
    captured_at TIMESTAMPTZ NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed the global ticket from the newest per-account ticket of any model that carries both
-- a turn-state and an __oailb cookie, so fail-closed routing keeps working until the
-- harvester's next cycle. Malformed values are
-- skipped rather than cast: every cast is guarded so no single row can abort the migration.
CREATE FUNCTION pg_temp.codex_shared_ticket_try_timestamptz(value TEXT) RETURNS TIMESTAMPTZ
LANGUAGE plpgsql STABLE AS $$
BEGIN
    IF value IS NULL OR value !~ '^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}(:?\d{2})?)?$' THEN
        RETURN NULL;
    END IF;
    RETURN value::timestamptz;
EXCEPTION WHEN others THEN
    RETURN NULL;
END
$$;

INSERT INTO codex_shared_tickets (id, generation_id, source_account_id, ticket, captured_at, version)
SELECT
    1,
    candidate.generation_id::uuid,
    candidate.account_id,
    candidate.ticket || jsonb_build_object('account_id', candidate.account_id, 'model', candidate.model, 'generation_id', candidate.generation_id),
    candidate.captured_at,
    1
FROM (
    SELECT
        a.id AS account_id,
        substr(e.key, length('codex_turn_ticket:') + 1) AS model,
        e.value AS ticket,
        CASE WHEN jsonb_typeof(e.value -> 'generation_id') = 'string'
                  AND e.value ->> 'generation_id' ~ '^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$'
             THEN lower(e.value ->> 'generation_id') END AS generation_id,
        CASE WHEN jsonb_typeof(e.value -> 'captured_at') = 'string'
             THEN pg_temp.codex_shared_ticket_try_timestamptz(e.value ->> 'captured_at') END AS captured_at
    FROM accounts a
    CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(a.extra) = 'object' THEN a.extra ELSE '{}'::jsonb END) AS e(key, value)
    WHERE a.deleted_at IS NULL
      AND e.key LIKE 'codex_turn_ticket:_%'
      AND jsonb_typeof(e.value) = 'object'
      AND e.value ->> 'verification_method' = 'modeltrace_v1'
      AND jsonb_typeof(e.value -> 'fingerprint_commit') = 'string'
      AND btrim(e.value ->> 'fingerprint_commit') <> ''
      AND jsonb_typeof(e.value -> 'state') = 'string' AND btrim(e.value ->> 'state') <> ''
      AND jsonb_typeof(e.value -> 'cookie') = 'string' AND e.value ->> 'cookie' ~ '(^|;)\s*__oailb=[^;]'
) AS candidate
WHERE candidate.generation_id IS NOT NULL
  AND candidate.captured_at IS NOT NULL
ORDER BY candidate.captured_at DESC, candidate.account_id
LIMIT 1
ON CONFLICT (id) DO NOTHING;

DROP FUNCTION pg_temp.codex_shared_ticket_try_timestamptz(TEXT);

-- Retire per-account tickets now that their newest valid copy seeds the global ticket, and
-- the per-model participation switches: participation is an account-level choice only.
UPDATE accounts
SET extra = (extra - 'codex_ticket_harvest_models')
    - ARRAY(SELECT key FROM jsonb_object_keys(extra) AS key WHERE key LIKE 'codex_turn_ticket:%')
WHERE extra IS NOT NULL
  AND jsonb_typeof(extra) = 'object'
  AND (extra::text LIKE '%"codex_turn_ticket:%' OR extra::text LIKE '%"codex_ticket_harvest_models"%');
