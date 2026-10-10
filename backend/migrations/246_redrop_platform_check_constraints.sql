-- Sub4API: 245_openai_bps_platform.sql 按文件名排序晚于上游的
-- 242_drop_platform_check_constraints.sql，会在新库上重新加回平台 CHECK 约束。
-- 平台白名单已改由应用层平台清单（internal/domain/platforms.go）统一校验，
-- 此处再次删除，与 242 保持一致。
--
-- channel_monitors / channel_monitor_request_templates 的 provider CHECK 保留不动。
--
-- DROP ... IF EXISTS 保证可重入。

ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE composite_model_routes
    DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;
