-- Preserve historical turn-state-only events; new events require both credentials to change.
ALTER TABLE codex_ticket_invalidations
    DROP CONSTRAINT codex_ticket_invalidations_reason_code_check;
ALTER TABLE codex_ticket_invalidations
    ADD CONSTRAINT codex_ticket_invalidations_reason_code_check
    CHECK (reason_code IN ('upstream_new_turn_state', 'upstream_turn_state_and_oailb_changed'));
