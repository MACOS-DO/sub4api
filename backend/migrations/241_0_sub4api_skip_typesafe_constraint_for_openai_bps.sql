-- Sub4API: 上游 241_add_typesafe_platform.sql 会重建不含 openai_bps 的平台 CHECK 约束，
-- 1.1.5–1.1.9 的存量库若已有 openai_bps 行（245_openai_bps_platform.sql），
-- 首次执行 241 时约束校验会失败。上游迁移不可修改，故在 241 之前处理。
--
-- 241 重建的两个约束随后会被 242_drop_platform_check_constraints.sql /
-- 246_redrop_platform_check_constraints.sql 删除，跳过 241 与执行它的最终结构一致，
-- 因此仅在存在 openai_bps 行时将 241 登记为已应用（使用上游 checksum）。
--
-- 241 已应用（含 1.1.10/1.1.11 的历史 checksum）时 ON CONFLICT 不做任何事；
-- 无 openai_bps 行（含新库）时不登记，上游 241 正常执行。

INSERT INTO schema_migrations (filename, checksum)
SELECT '241_add_typesafe_platform.sql',
       'b6559525bf8d0b5d7c617e7415944f0d1fae8c408e9131ace84139795f66dcd2'
WHERE EXISTS (SELECT 1 FROM user_platform_quotas WHERE platform = 'openai_bps')
   OR EXISTS (SELECT 1 FROM composite_model_routes WHERE target_platform = 'openai_bps')
ON CONFLICT (filename) DO NOTHING;
