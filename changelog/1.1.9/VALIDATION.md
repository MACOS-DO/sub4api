# 1.1.9 Gateway v4 验证记录

日期：2026-10-02。分支：`release/1.1.9`。这是本地开发交付记录，不是生产发布或完整官方协议验收声明。

## 源码与镜像

- Sub4API 基于 `a3ece4348ee57bfa6b9ce5a2621c7289ba11dfa3`，本次变更保留在工作树；不能用该 HEAD 代替完整实现。
- Gateway：`460c51b6fe85649cb4152d6d9704f57a6edb2afd`，干净源码已与导出目录逐一核对 8,470 个文件。
- Gateway git archive SHA-256：`c7424aff24907bfd88085ce66319cd0e05a2c4be8fc9870a6c93d4c74513b79e`。
- 本地标签：`sub4api:1.1.9-gateway-local`、`codex-gateway:sub4api-460c51b6fe85`，平台 `linux/amd64`。
- 最终源码归档、摘要与镜像清单记录在 `/tmp/sub4api-119-e2e/artifact-manifest.json`；源码归档不包含凭据、数据库备份、node_modules 或编译缓存。
- 没有推送 Git、发布镜像或切换生产流量。Docker 本地 RepoDigest/OCI 摘要不代表已推送。

## 工程检查

| 检查 | 结果 |
| --- | --- |
| Gateway 固定提交全量 `just test -p codex-gateway --locked --offline --retries 0 --run-ignored all --test-threads 1` | 125/125，0 skipped，包含真实 PG 专项 |
| Go 定向：config、Gateway client、service、handler/admin/DTO/quotaview、routes、responseheaders、repository | 通过；覆盖旧 OpenAI/BPS、Composite、影子、监控、配额和恢复相关用例 |
| 新增服务用例 | 凭据不持久化、未知回执不重放、同键冲突、错误归属、慢快照条件更新、完整审计凭据字段脱敏、四档身份、跨实例父引用、WS 固定票据、启动鉴权失败隔离 |
| 真实 PG/Redis repository integration | 绑定事务回滚、批量调度投影、两个实例账号锁、恢复双锁、全 dump 恢复、SQL 错误回滚通过 |
| 客户端取消/无效身份 | 单请求失败不把 Gateway 标为全局不可用；取消健康检查也不覆盖有效能力状态 |
| Redis 调度投影回归 | ready/unknown/pending_delete 正确恢复；拒绝缓存上游凭据 |
| Wire diff | 无差异；临时 modfile 补足 Wire CLI 自身依赖，未修改项目依赖 |
| 前端类型、构建 | 通过；保留原有 chunk-size/Browserslist 提示 |
| 前端相关测试 | 6 文件、142 项通过，包括原创建/编辑弹窗、新平台创建和用户平台配额 |
| 联合 Compose `config --quiet` | 通过 |

Go 主回归命令：

```sh
go test -tags=unit -p 2 ./internal/config ./internal/pkg/codexgateway \
  ./internal/service ./internal/handler/... ./internal/server/routes \
  ./internal/util/responseheaders ./internal/repository \
  -run 'Test.*(Codex|OpenAI|BPS|Gateway|Shadow|Backup|Restore|PlatformQuota|Composite|ChannelMonitor|Group)' -count=1
CI=1 SUB2API_TEST_POSTGRES_IMAGE=postgres:18-alpine \
  go test -tags=integration -p 2 ./internal/repository -run TestCodexGateway -count=1
```

原始日志位于 `/tmp/sub4api-119-*.log`。后续小范围修复另外执行了对应 client/service/repository 定向回归，不用历史 `107/107` 代替本轮结果。

## 真实容器 + 模拟官方上游

环境使用真实 Rust Gateway、PostgreSQL 18、Redis 和两个 Sub4API 容器。Python 仅充当不访问外网的合成官方 TLS 上游，不是部署架构的代理。

| 项目 | 实测结果 |
| --- | --- |
| 两个 Sub4API 共用一个 Gateway | 并发请求均 200 |
| 创建幂等与竞争 | 并发为 200/409，随后两实例恢复同一本地账号及固定远端 ID |
| 母子绑定 | 影子引用母绑定；删除影子后远端账号仍存在 |
| 远端版本推进后的重新授权 | 显式提交读取当前远端 revision，PUT 成功；不重放之前的凭据操作 |
| 管理员停用 | 同步后保持 inactive |
| 停机删除 | 202 pending_delete；Gateway 恢复后本地和远端均删除 |
| 远端成功、本地失败 | 对单个合成账号注入真实 PG 更新失败；Gateway 回执 succeeded、本地仍 syncing；重启后查回执恢复，远端 revision 保持 1，无凭据重交 |
| Responses、Chat、Messages | HTTP/SSE、非流式转换通过 |
| compact、搜索、输入计数 | 通过 |
| 原生 WS | 同一独占连接两轮通过，未知事件保留，服务身份扩展不出站到官方 |
| 大图片 | 20 MiB JSON 图片编辑正文通过实际 Rust Gateway；验证传输大小限制，不代表真实图片生成质量或官方模型权限 |
| 错误服务密钥 | 临时实例的新平台返回单个 503 JSON；旧平台 200；管理员状态不变，服务不可用状态按实例隔离 |
| Gateway 停机 | 旧 OpenAI 请求 200，新平台 503；没有旧 token 兜底 |
| 整库恢复 | 运行中的 Gateway 阻止恢复；停写后双锁持有至结束；业务、绑定、Gateway 账号/回执/迁移记录完整恢复 |
| 恢复后缓存 | 清空专用测试 Redis DB 后重建，使用重新查询的状态 |
| 凭据归属 | 真实官方导入后检查业务账号、绑定、幂等结果、实际 Redis 账号 JSON 及服务日志，未发现导入 AT/RT/ID Token |

恢复演练发现 PostgreSQL 18 清理分区继承主键的顺序问题，已在完整恢复事务内处理本应用分区根表，错误时整次回滚；没有修改既有迁移校验和。

## 官方服务

使用用户指定的官方 auth.json 导入源，原文件未修改。凭据仅导入 Gateway，测试数据库备份权限为 0600，不随源码交付。

- 官方凭据导入：成功，远端 ready，认证 at_rt。
- 官方额度只读查询：HTTP 200。
- 官方模型目录：直接 Gateway 为 9 项；经 Sub4API 组策略过滤后为 8 项，HTTP 200 并返回 ETag；条件查询 HTTP 304。初次本地生成目录的 13 项不作为官方目录验收证据。
- 官方文本验证：用户明确授权 3 次。前两次（HTTP、WS）使用 gpt-5.4，官方均返回模型不支持，错误保留且未产生输出；核对实际目录后，第 3 次使用 gpt-5.6-luna，原生 WS 成功返回 OK，usage 为输入 9 / 输出 5。未追加请求。计费表共 2 条（失败轮次零费用 1 条，成功扣费 1 条），成功用量合计 9 / 5，actual_cost 0.0000078，没有重复扣费。HTTP 成功生成和同一 WS 连续两轮官方成功仍未验收。
- 官方图片、完整工具/compact/取消矩阵、实时语音 SDP/sideband：尚未官方验收。当前 Linux/WSL 实测 Live 返回 503，原有能力检查明确要求 macOS DeviceCheck（Windows 尚未实现）；不能以模拟请求替代该能力。
- 官方额度重置、邀请和隐私变更：未执行，继续保留具体操作确认。

## 仍需明确的验收边界

- 没有宣称所有 HTTP/WS 失败组合均已完成官方端到端验证；新增适配复用的计费、取消和重复终态钩子有定向回归，真实官方矩阵仍需执行。
- 真实浏览器视觉验收未执行；本次按用户指示直接在原有弹窗实现，组件测试/类型检查/构建不能替代视觉验收。
- 收敛父引用的 Redis 映射保留 30 天，缓存丢失或恢复后无法解析旧引用时需要新会话。
- 本地测试服务和凭据数据库仅用于验收，不作为生产流量入口。请按集成文档的维护流程处理恢复和缓存。


## 2026-10-03：内部 service key 自动初始化

本次按确认的后续计划，改为 Sub4API 首次启动自动生成随机内部 key，保存到专用 Docker 命名卷；Gateway 只读同一文件。联合 Compose 不再要求宿主机密钥文件或手动运行 openssl，默认保持原有显式文件模式兼容，并补齐 Gateway 的 `sub2api-network` 网络配置。Gateway 源码与镜像未变更。

| 本轮检查 | 结果 |
| --- | --- |
| Go 自动密钥、配置加载、Gateway 服务及既有客户端定向回归 | 通过 |
| Go race：自动密钥文件测试 | 通过，覆盖 32 个并发初始化、完整发布、已有文件复用 |
| 写入中断/异常、损坏文件、不可读文件、孤立临时文件 | 无半写入最终 key；不会覆盖已有文件；后续首次生成正常 |
| 实际合并 Compose：首次启动两个 Sub4API | 自动生成同一个 32 字节随机 key；文件 0600、目录 0700，UID/GID 1000；Gateway 只读 |
| Gateway 先启动 | 缺少 key 时按容器重启策略等待，Sub4API 生成后恢复并通过 capabilities 鉴权 |
| 重启及强制重建容器 | key 内容保持一致，管理接口和旧平台请求正常 |
| 未携带/错误 key | Gateway 内部接口均返回 401 |
| 损坏/不可读 key 故障注入 | 原文件保留，OpenAI Codex 不可用；两个实例旧 OpenAI 模拟请求均返回 200 |
| 日志检查 | 不包含生成的 service key |
| 既有 Compose 环境和安全配置检查、Wire diff、diff whitespace | 通过 |

实际测试由 `deploy/tests/docker-compose-codex-key-test.py --runtime` 运行仓库基础 Compose、Gateway 叠加文件及测试覆盖配置。测试使用独立项目名、新 PostgreSQL/Redis/密钥卷、每实例独立应用数据卷、无外网路由的内部网络和合成上游；只清理该测试创建的项目。未启动先前官方账号测试库，也未增加官方请求。

复现命令（需预先准备本地镜像）：

```sh
python3 deploy/tests/docker-compose-codex-key-test.py
python3 deploy/tests/docker-compose-codex-key-test.py --runtime --image sub4api:1.1.9-gateway-local
cd backend
go test ./internal/pkg/codexgateway ./internal/config ./internal/service -run 'Test(ServiceKey|LoadCodexGateway|CodexGateway)' -count=1
go test -race ./internal/pkg/codexgateway -run TestServiceKey -count=1
```

本轮原始记录：`/tmp/sub4api-119-auto-key-unit-final.log`、`/tmp/sub4api-119-auto-key-client-final.log`、`/tmp/sub4api-119-auto-key-race.log`、`/tmp/sub4api-119-auto-key-compose.log`、`/tmp/sub4api-119-auto-key-wire.log`。首次测试因沙箱不允许本地监听而失败，取得本地测试执行权限后重跑通过；容器脚本的动态端口及共享应用目录问题已在测试覆盖配置中修正。

更新后的源码归档和本地镜像/二进制摘要保存于 `/tmp/sub4api-119-auto-key/artifact-manifest.json`，取代此前交付清单中的 Sub4API 构建基线；此前 Gateway 及官方协议验收结论保持其原有范围。无推送、镜像发布或生产切换。

## 2026-10-04：修复五项 Gateway 接入审查问题

本轮修复待删除账号被旧成功回执重新开放调度、原生 WS 未应用账号模型映射、固定账号目录的平台过滤与前端保存遗漏、账号测试错误回退到 Claude 模型，以及编辑时覆盖临时禁用规则的问题。此前临时复现案例已迁入正式 Go/Vue 测试，并补充状态恢复、平台隔离与多轮请求边界。

| 验证 | 结果 |
| --- | --- |
| Go 定向回归：Gateway client、service、handler/admin/DTO、routes、repository | 通过；包含 OpenAI/BPS、影子、分组和调度缓存相关用例 |
| 删除状态 | PUT/PATCH/refresh 的 pending/succeeded/failed/indeterminate 回执均保持待删除；重启后查询回执并完成删除，不重交凭据；旧异常记录会重新发布不可调度投影 |
| PostgreSQL 18 + Redis 集成 | 6 个 Gateway 专项测试通过；新增用例验证母账号和影子的数据库读取、Redis 投影及调度 outbox，使用独立临时容器 |
| 原生 WS 模拟联调 | 两条本机 WS 连接转发链；渠道映射与账号通配符组合、连续 4 轮、模型省略/别名切换、固定握手票据及每轮仅结算一次通过；需要不同票据的模型要求重连 |
| 模型目录 | 新平台固定账号可用性、平台隔离、配置顺序、部分失败、调度回退及 ETag 通过；账号测试保留目录映射和有效空结果 |
| 前端组件 | 4 文件、77 项通过；覆盖新平台账号搜索、过期搜索响应丢弃、固定账号配置保存/重开、默认测试模型及临时禁用规则的启停/修改/删除 |
| 前端类型、i18n、生产构建 | `vue-tsc --noEmit`、3 项 locale 检查与 Vite build 通过；保留 Browserslist/chunk-size 提示 |
| 修复差异复审与空白检查 | 已复审本轮差异，`git diff --check` 通过 |

主要复现命令（在 backend 或 frontend 对应目录执行）：

```sh
go test -tags=unit -p 2 ./internal/pkg/codexgateway ./internal/service ./internal/handler/... ./internal/server/routes ./internal/repository -run 'Test.*(Codex|OpenAI|BPS|Gateway|Shadow|PlatformQuota|Group|SchedulerCache)' -count=1
go test -tags=unit ./internal/service -run 'Test(CodexGateway|PassthroughLifecycle|OpenAIWS.*(Turn|Model|Failure)|OpenAIWSPassthrough)' -count=1
go test -tags=unit ./internal/handler -run 'Test(OrdinaryPinnedModels|PinnedCodexModels)' -count=1
CI=1 SUB2API_TEST_POSTGRES_IMAGE=postgres:18-alpine go test -tags=integration -p 2 ./internal/repository -run '^TestCodexGateway' -count=1
node node_modules/vitest/vitest.mjs run src/components/account/__tests__/EditAccountModal.spec.ts src/components/account/__tests__/AccountTestModal.spec.ts src/components/admin/group/__tests__/CodexManifestAccountsField.spec.ts src/views/admin/__tests__/GroupsView.codexManifest.spec.ts
node node_modules/vue-tsc/bin/vue-tsc.js --noEmit
node node_modules/vitest/vitest.mjs run src/i18n/__tests__/localeKeyCompleteness.spec.ts
node node_modules/vite/bin/vite.js build
```

原始日志保存在 `/tmp/sub4api-five-fixes-{go-regression,ws-final,model-handlers,integration,ui-second,typecheck,build}.log`。本轮未调用官方账号，不新增数据库迁移或修改 Rust Gateway 协议。修复保留在工作区，没有 Git 提交/推送、镜像发布或部署；前述源码归档与镜像不包含本轮修复。
