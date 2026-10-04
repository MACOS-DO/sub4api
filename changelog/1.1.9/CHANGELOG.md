# Sub4API 1.1.9

## 更新内容

- 新增 OpenAI Codex 平台，通过独立 Gateway v4 托管凭据、OAuth 会话和认证状态。
- 对齐 OpenAI 账号管理体验：添加、编辑、重新授权、批量导入、额度、重置次数、credit、余额、邀请、资料、诊断、测试和统计。
- 支持 auth.json、普通／移动端 Refresh Token、Access Token、PAT、Setup Token、Agent Identity 等凭据方式，并提供幂等操作回执和异常恢复查询。
- 补齐 Codex 模型、搜索、图片、原生 WebSocket、账号辅助接口、平台配额、模型映射、Compact、调度、影子账号和自动用卡链路。
- 新平台保持独立的凭据、缓存、调度和 WebSocket 边界，不回退到旧 OpenAI/BPS token provider。
- 增加共享 PostgreSQL 的联合部署、整库恢复保护、跨实例操作锁和持久化回执恢复。
- 联合 Compose 首次启动自动生成并持久化内部 service key，修正 Gateway 网络归属并保留显式文件模式兼容。

验证记录、已覆盖场景和未验收项目见 [VALIDATION.md](VALIDATION.md)。
