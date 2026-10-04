-- Accounts are soft-deleted, so the binding foreign-key cascade never runs.
-- Release Gateway IDs and creation keys still held by deleted accounts.
DELETE FROM public.openai_codex_account_bindings b
USING public.accounts a
WHERE a.id = b.account_id AND a.deleted_at IS NOT NULL;
