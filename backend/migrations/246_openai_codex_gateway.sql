-- OpenAI Codex is an isolated, Gateway-managed platform.

ALTER TABLE user_platform_quotas DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;
ALTER TABLE user_platform_quotas ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'openai_bps', 'openai_codex', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'));

ALTER TABLE composite_model_routes DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;
ALTER TABLE composite_model_routes ADD CONSTRAINT composite_model_routes_target_platform_check
    CHECK (target_platform IN ('anthropic', 'openai', 'openai_bps', 'openai_codex', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'));

ALTER TABLE channel_monitors DROP CONSTRAINT IF EXISTS channel_monitors_provider_check;
ALTER TABLE channel_monitors ADD CONSTRAINT channel_monitors_provider_check
    CHECK (provider IN ('anthropic', 'openai', 'openai_bps', 'openai_codex', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'));

ALTER TABLE channel_monitor_request_templates DROP CONSTRAINT IF EXISTS channel_monitor_request_templates_provider_check;
ALTER TABLE channel_monitor_request_templates ADD CONSTRAINT channel_monitor_request_templates_provider_check
    CHECK (provider IN ('anthropic', 'openai', 'openai_bps', 'openai_codex', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'));

ALTER TABLE accounts ADD CONSTRAINT accounts_codex_gateway_type_check
    CHECK ((platform = 'openai_codex') = (type = 'gateway'));

CREATE TABLE public.openai_codex_account_bindings (
    account_id BIGINT PRIMARY KEY REFERENCES public.accounts(id) ON DELETE CASCADE,
    gateway_id VARCHAR(64) COLLATE "C" NOT NULL UNIQUE,
    creation_key VARCHAR(64) NOT NULL UNIQUE,
    record JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_codex_binding_account CHECK ((record->>'account_id')::bigint = account_id),
    CONSTRAINT openai_codex_binding_gateway CHECK (record->>'gateway_id' = gateway_id)
);
CREATE INDEX openai_codex_binding_sync ON public.openai_codex_account_bindings ((record->>'sync_state'));

ALTER TABLE accounts ADD CONSTRAINT accounts_codex_business_credentials_check
    CHECK (platform <> 'openai_codex' OR
      (credentials - ARRAY['model_mapping','compact_model_mapping','models','model_whitelist',
        'temp_unschedulable_enabled','temp_unschedulable_rules','header_overrides']::text[]) = '{}'::jsonb);
