# OpenAI Codex / Gateway v4

## 范围与界面

`release/1.1.9` 新增 `openai_codex` 平台（显示名称 **OpenAI Codex**）及 `gateway` 账号类型。在原有“添加账号”“编辑账号”弹窗中选择该平台，使用相同的代理、分组、模型、时区、并发和调度入口；没有新增独立管理页面。按本次确认直接实施 Vue，不再等待 HTML 预览审批。

Sub4API 负责业务配置、权限、调度、转换、计费和取票；Rust Gateway 独占凭据、OAuth 刷新及特殊认证。现有 OpenAI/BPS 账号、请求、刷新器和 token 缓存不迁移。Gateway 禁用或启动失败不会阻止旧平台启动。

## 数据与账号事务

共用一个 PostgreSQL 数据库及 `public` Schema：

| 所有者 | 表 |
| --- | --- |
| Sub4API | 原有业务表、`openai_codex_account_bindings`、调度 outbox |
| Gateway | `codex_gateway_accounts`、`codex_gateway_account_operations`、`codex_gateway_schema_migrations` |

Sub4API 应用代码只通过 Gateway API 管理远端账号，不访问其凭据表、不建立跨服务 ORM 关系或外键。迁移 `246_openai_codex_gateway.sql` 扩展平台约束并创建绑定表，不修改旧迁移、不自动搬迁旧凭据。

绑定保存固定远端 ID、revision、本地版本、操作 ID、请求摘要和非敏感状态。每次状态写入推进本地版本，账号锁与条件更新共同阻止慢查询覆盖新状态。账号级 PostgreSQL 会话锁可跨 Sub4API 实例协调；等待 HTTP 时不持有业务事务。锁获取结果不确定的连接被丢弃，不放回连接池。

创建在一个本地事务内保存账号、分组、绑定和 outbox；随后提交带幂等键的远端 PUT。只有校验成功回执的归属，再读取远端账号状态后，才开放 Gateway 调度条件。业务启停、有效期、限流仍独立判断。

名称和代理变更使用 PATCH；模型、分组、倍率、时区留在 Sub4API。版本冲突通过查询最新状态和新操作键恢复。凭据替换与手动刷新不自动重放；超时后查询回执。`pending` 等待，`indeterminate` 或未知凭据结果阻断调度并要求查询/重新授权。后台每 30 秒恢复状态、非敏感配置和删除。

删除先关闭调度。远端未确认时返回 HTTP 202 及待删除账号；确认后完成本地删除。影子共享母账号绑定及身份来源，创建与母账号删除使用同一把锁，删除影子只删除本地账号。普通复制在原创建弹窗复制业务配置，要求重新提供凭据。

普通导出仅包含业务配置和绑定 ID。导入后重新查询 Gateway，缺失远端时不可调度，不信任导出的可用快照。完整凭据只由整库备份保存。

## 管理接口

沿用 `/api/v1/admin/accounts` 创建和 `/:id` 更新。新平台请求使用 `Idempotency-Key`；凭据只放在只写字段 `gateway_credentials`：

```json
{
  "name": "Codex account",
  "platform": "openai_codex",
  "type": "gateway",
  "credentials": {"model_mapping": {"public-model": "upstream-model"}},
  "gateway_credentials": {"type": "auth_json", "auth_json": {}},
  "group_ids": [123]
}
```

上例 `auth_json` 是占位对象，实际填写有效导入文件。支持 Gateway v4 六种输入：`auth_json`、`tokens`、`refresh_token`、`personal_access_token`、`setup_token`、`agent_identity`。未知字段及跨认证类型字段被拒绝。普通 `credentials` 仅接受业务配置白名单，拒绝上游 token、私钥和鉴权头。

返回的只读 `gateway` 包含绑定 ID、拥有者、revision、认证方式、当前操作、同步状态、非敏感快照、资料和服务可用性；不返回导入凭据。编辑弹窗不回填凭据，重新授权有独立输入区域。

- `POST /api/v1/admin/accounts/:id/gateway/sync`：仅状态查询及非敏感恢复。
- `POST /api/v1/admin/accounts/:id/refresh`：新平台要求 `Idempotency-Key`，由 Gateway 执行刷新。
- `DELETE /api/v1/admin/accounts/:id`：支持待删除恢复。
- 原有测试、诊断、额度、资料、隐私、邀请、影子及取票入口按明确平台分流，继续使用原有权限和具体操作确认。

界面区分同步中、可用、需重新授权、刷新阻断、结果不确定、待删除、远端缺失和服务不可用。

## 身份、协议与故障

Sub4API 生成最终 IdentityContext，Gateway 只校验和投影，不二次随机化或映射。

| 模式 | installation ID | session ID | thread ID | turn ID |
| --- | --- | --- | --- | --- |
| off（默认） | 客户端标识及账号/调用方隔离 | 同左 | 同左 | 映射已有值，缺失补齐 |
| device | 账号固定 | 现有隔离规则 | 现有隔离规则 | 同上 |
| session | 账号固定 | 账号固定 | 原始会话稳定派生 | 每轮生成一次 |
| full | 账号固定 | 账号固定 | 等于账号 session | 每轮生成一次 |

显式 session/full 允许跨用户共享上游账号 session，full 还共享 thread；权限、计费和业务请求归属独立隔离。设备配置优先使用 `openai_device_id`，否则使用持久化账号种子。影子使用母账号身份来源。

收敛模式的父轮次、父线程关联通过共享 Redis 保存非敏感映射，原始标识只以带调用方/绑定作用域的哈希作键，保留 30 天。映射丢失时要求新会话，不伪造父引用。不遍历工具参数及不透明续接数据。恢复数据库后应开始新连接/会话。

HTTP 使用可信 Identity 头；WS 每个客户端独占连接，握手固定 installation/session/thread/时区/票据，每轮注入可信身份扩展。变更设备、session、thread 或 Cookie 必须新建连接。新平台不使用共享 WS 池或插件 HTTP Bridge，复用逐轮准入、并发和结算钩子，已生成后不重放、不换账号续接。

Responses、Chat Completions、Messages、compact、模型、搜索、输入计数、图片以及 Live/sideband 使用独立 Gateway 适配边界。图片 multipart 在 Sub4API 转 JSON，限制按 base64 膨胀后的实际正文检查。Live 仍需要原有平台 DeviceCheck/attestation 能力和通话归属。

时区默认 `Asia/Singapore`，Sub4API 仅发时区头，由 Gateway 改写规定位置的正文。取票保留 Sub4API 选代理、挑战、验证、保存流程；Transport 携带所选代理/Cookie，turn-state 固定于握手。捕获 Cookie 只进入内部响应对象。

伪造内部头和身份扩展被移除。服务控制头不交给最终客户端；官方请求编号、未知 WS 事件、原始错误、usage、模型和 service tier 按现有解析器保留。Gateway 服务错误不触发旧 token 直连或批量停用账号。

## 配置与构建

```yaml
gateway:
  codex4server:
    enabled: false
    base_url: http://gateway:8787
    service_key_file: /run/secrets/gateway_service_key
    auto_generate_service_key: false
    admin_timeout: 60s
    sync_interval: 30s
```

启动及恢复检查 API v4、PG 存储、持久化回执和实际请求上限；管理超时与流式生命周期分开。内部连接不经过账号出口代理。

Gateway 固定提交 `460c51b6fe85649cb4152d6d9704f57a6edb2afd`。构建目录应为该提交的完整 `git archive` 导出，本轮已对照核验 8,470 个文件。源码归档 SHA-256：`c7424aff24907bfd88085ce66319cd0e05a2c4be8fc9870a6c93d4c74513b79e`。

叠加 `deploy/docker-compose.codex-gateway.yml`，使用同一项目名和原有 PostgreSQL：

```sh
export CODEX_GATEWAY_SOURCE_DIR=/absolute/path/to/verified-gateway-source
# POSTGRES_PASSWORD 等沿用现有部署配置
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.codex-gateway.yml config
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.codex-gateway.yml up -d --build
```

联合 Compose 自动启用 `auto_generate_service_key`，将专用命名卷 `codex_gateway_service_key` 挂载到 `/run/codex4server`。Sub4API 首次启动生成随机 service key，两个服务读取 `/run/codex4server/service_key`；用户不需要运行 openssl、创建宿主机密钥文件或填写密钥。每套部署独立生成，重启、升级和重建容器时复用；保留该命名卷，不使用 `down -v` 删除持久化数据。

Sub4API 对密钥卷可写，Gateway 只读，默认两个进程均使用 UID/GID 1000，目录权限 0700、文件权限 0600。多个 Sub4API 同时初始化时，通过原子且不覆盖的文件发布选择同一个完整 key。已有合法文件原样复用；无效、不可读的文件不会被随机新值覆盖。该类错误只让 OpenAI Codex 不可用，旧平台仍正常启动；修复文件或权限后重启 Sub4API/Gateway。修改默认运行 UID 时须保证两端都能按既有文件权限读取该卷。

Gateway 仅内部端口、`/readyz` 健康检查，加入原有 `sub2api-network`，使用 `public` + `codex_gateway_`，不挂载账号目录，不是 Sub4API 的健康启动依赖。它若先于首次密钥生成启动，会按容器的重启策略恢复；无需新增初始化容器。首版只运行一个 Gateway；多实例会被数据库级单写者锁拒绝。

显式密钥文件模式继续支持：保持 `auto_generate_service_key: false`，将同一现有密钥分别挂载给两端，并显式配置 Sub4API 的 `service_key_file` 和 Gateway 的 `--service-key-file`。跨主机部署使用该方式，自动共享卷模式只面向同一 Docker 主机。

两个服务默认上限对齐到 268435456 字节，可通过联合环境变量调整。多 Sub4API 实例共享数据库、Redis、JWT secret 和 service key；基础 Compose 固定端口/容器名须由现有多实例部署覆盖，不能直接对它盲目 scale。

## 整库备份与恢复

备份必须包括业务表、绑定及 Gateway 账号、操作回执、迁移历史。备份包含完整凭据，使用现有受控备份存储；不要将它当普通账号导出。内部 service key 位于独立命名卷，升级和恢复数据库时也应保留该卷；它不写入业务表或普通账号导出。

恢复是离线维护流程：

1. 停止 Gateway、所有 Sub4API 实例及其他业务写入；保存当前整库备份。
2. 在**执行恢复的同一个 PostgreSQL 会话**取得 Gateway 锁 `7161981693156876665` 与 Sub4API 迁移锁 `694208311321144027`。任一锁不可得即拒绝恢复。
3. 使用 `psql --single-transaction --file=- --no-psqlrc --set=ON_ERROR_STOP=on` 持锁恢复。不得先探测锁再释放，不得忽略 SQL 错误。压缩备份先完整解压校验，再送入该会话。
4. Sub4API 恢复器在该事务内先清理自己的 `codex_ticket_attempts` 分区根表，再加载完整 dump，避免 PostgreSQL 18 清理继承主键时失败；任何错误均回滚。此恢复入口要求完整数据库备份，不适用于部分表 dump。
5. 在服务仍停止时清理 Sub4API 的旧调度/业务缓存。专用 Redis 数据库可清空该库；共用 Redis 库必须按所属前缀清理，不能清空其他应用数据。至少移除 `sched:*` 及 `codex_gateway_identity:*`。
6. 恢复成功释放锁后重启 Gateway 与 Sub4API，重新同步状态、重建快照，再恢复入口流量。Gateway 将恢复出的未完成凭据操作标为不确定，不重放 RT。

真实 PG 已验证锁全程持有、锁冲突拒绝、SQL 错误回滚与敏感诊断隔离；完整容器备份恢复及重建缓存的结果见验证记录。

## 验证边界

分层结果见 `changelog/1.1.9/VALIDATION.md`。本地测试、模拟官方上游、真实官方只读调用分别记录，不能相互替代。本轮不推送 Git、不发布镜像、不切换生产流量。官方生成、真实图片和实时语音未完成的项目不标记为生产验收通过。
