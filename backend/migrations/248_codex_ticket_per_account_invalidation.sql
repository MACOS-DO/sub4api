-- A shared ticket is now invalidated per account: each account records its own
-- invalidation of a generation while the other accounts keep using it.
ALTER TABLE codex_ticket_invalidations
    DROP CONSTRAINT IF EXISTS codex_ticket_invalidations_ticket_generation_id_key;
ALTER TABLE codex_ticket_invalidations
    ADD CONSTRAINT codex_ticket_invalidations_account_generation_key UNIQUE (account_id, ticket_generation_id);
CREATE INDEX IF NOT EXISTS codex_ticket_invalidations_generation
    ON codex_ticket_invalidations (ticket_generation_id);
