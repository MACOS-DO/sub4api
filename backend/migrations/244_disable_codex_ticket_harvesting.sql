-- One-time 1.1.4 upgrade: require administrators to opt back into ticket harvesting.
-- The migration ledger prevents later restarts from overwriting their new choice.
INSERT INTO settings (key, value, updated_at)
VALUES ('openai_codex_ticket_enabled', 'false', NOW())
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = EXCLUDED.updated_at;
