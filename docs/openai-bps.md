# OpenAI BPS 接入

平台值为 `openai_bps`，账号类型为 `oauth`，界面显示 **Access Token**。启动新版本时需完成 `245_openai_bps_platform.sql` 迁移。Redis 是必需依赖；会话状态不使用进程内降级。

## 配置

1. 创建 **OpenAI BPS** 分组，配置模型价格、倍率和白名单。上游成本由管理员设置，不从 Excel 点数或 Codex 配额推断。
2. 创建 **OpenAI BPS** 账号时会显示账号限制/封禁风险提示。点击保存或保存并测试后，必须在确认弹窗中确认风险才会创建；取消会保留输入。填写 `access_token`。`chatgpt_account_id` 留空时从 JWT 提取，填写时覆盖默认账号；同时提取 JWT 的到期时间。解析 JWT 只读取信息，不验证真实性。
3. 绑定 BPS 分组，按需要选择代理、调整模型映射。默认候选模型为 `gpt-6-astra`、`gpt-5.6-sol`；模型映射支持管理员设置别名和白名单。删除全部规则表示不限模型。
4. 在创建或编辑窗口选择“保存并测试连接”，用保存后的账号打开测试窗口。分别选择两个模型测试文本和压缩。账号列表也保留连接测试入口。测试开始只表示正在请求上游；实际响应显示 HTTP 状态、上游模型及请求 ID，失败保留原始错误码。测试结束及关闭弹窗时刷新账号状态。
5. Token 到期或被上游撤销后需手动替换。账号状态栏、用量列和编辑窗口分别显示已过期、已撤销或认证失败；JWT 未到期不代表上游认证有效。自然到期提示每 30 秒更新，不依赖列表自动刷新。编辑时留空保留原 Token 及诊断，替换新 Token 不会解除管理员主动停用或关闭调度的状态；历史暂停账号需手动恢复调度。
6. 使用该分组的 **Sub4API API Key** 配置 Codex，`wire_api = "responses"`，`supports_websockets = false`。API Key 使用说明提供 Codex 配置和模型目录。
7. Composite 分组必须为 BPS 添加显式路由。模型目录仅发布启用的 Responses/通用路由且有合格 BPS 账号承接的模型；精确路由直接发布别名，前缀路由筛选可枚举的账号模型。没有有效候选时返回空列表。普通 `gpt-*` 自动识别规则仍选择 OpenAI。

上游地址固定为 `https://bps.openai.com/basispoints/api/responses`，不接受客户端或账号配置替换。服务端设置 Bearer token、两个账号 ID 头和 `x-basispoints-auth-mode: chatgpt`，不转发客户端提供的认证头。

## 支持范围

- HTTP Responses 的 JSON 和 SSE、function/custom/namespace 工具、`update_plan`、多轮工具结果及加密 reasoning。
- 推理强度 `low` / `medium` / `high` / `xhigh`，默认 `medium`；`max` 等不支持的值明确报错。
- `/responses/compact` 和带 `compaction_trigger` 的 Responses 压缩请求。
- `text.format.type=json_object/json_schema` 的 JSON 与 SSE 输出，采用提示词约束和本地最终结果校验。
- 不提供 WebSocket、Chat Completions、Claude Messages、图像/音频、自动续期或 Excel 本地凭证读取。不能用 `previous_response_id` 或 `item_reference` 代替完整历史。
- 模型目录声明串行工具、文本输入及 HTTP。目录的 200,000 token 上下文和 160,000 token 压缩阈值是保守的本地策略，不是上游配额或能力保证。

## 会话、工具与压缩

建议客户端保持 `session_id` 或 `prompt_cache_key` 并携带完整历史。没有显式 ID 时，以首条用户消息生成稳定身份，更换模型或工具不改变会话。完全相同的首条消息无法区分两个独立会话，因此需要并行同题对话时应指定不同 ID。

会话按 API Key 隔离，建立上下文后绑定账号和 ChatGPT workspace。绑定账号不可用时返回 `bps_account_unavailable`，不会把加密历史重放给另一个账号。Redis 中的绑定、原始工具 item 和压缩记录采用 7 天闲置过期。缺失上下文返回 `bps_context_expired`，需要新建会话。

工具声明（含 `additional_tools`）整理成稳定的文本目录；重复且相同的声明合并，冲突声明报错。function 默认使用 `name/arguments` JSON 信封，也兼容 `tool/args`；custom 默认使用 `summary=codex2api.custom/完整工具名` 和 `code` 原文，也接受旧 JSON 信封。工具信封上限 1 MiB。`run_officejs` 仅作为协议载体，代理不执行 OfficeJS 或客户端工具。只有本次声明的工具可输出给客户端，function 参数通过 JSON Schema 校验，外部 schema 引用被禁用。完整的原始 item、`id`、`call_id` 和 arguments 保存到 Redis，在客户端回传结果时恢复。结果转换为带稳定 `fc_` ID 的 `function_call_output`；加密 reasoning 重建为类型、空 summary 和不变的密文。原生 `update_plan` 对齐步骤别名、状态和成功回执，失败结果保留。一次最多交付一个工具调用；文本实时输出，工具事件在校验和保存完成后输出。

压缩调用真实摘要请求并关闭客户端工具。摘要保存到 Redis，客户端只收到版本化 `bpscmp_v1_` 引用；后续请求恢复摘要，引用本身不发给 BPS。无显式会话 ID 时，引用也可恢复原会话。压缩失败返回错误，不替换客户端原历史。异常断流或终态丢失已完成工具 item 时不会伪造成功。

压缩记录同时保存 `turn_id` 和迭代基数。恢复后没有新用户消息就保持同一轮次，仅按新增工具结果增加 `agent_iteration`；重试相同请求不改变上游请求体。旧摘要记录仍可读取，以不透明引用作为稳定轮次锚点；新记录在连续压缩与跨实例恢复后仍保留原始轮次。

## 结构化输出

通过 `text.format` 传入 `json_object` 或带 `name/schema/strict` 的 `json_schema`。代理将要求写入 developer 消息，BPS 请求不携带原生 `text.format`，客户端响应保留请求的格式声明。这是提示词约束加本地校验，不是上游原生约束解码。

`json_object` 必须返回一个 JSON 对象；`json_schema` 的最终 JSON 必须满足 schema，即使 `strict=false` 也会校验。保留大整数精度。schema 上限 1 MiB，仅允许文档内引用；无效 schema 在请求上游前返回 400。最终文本上限 16 MiB，同时受既有响应大小限制约束。

结构化消息文本等终态验证通过后才发送，JSON/SSE 使用同一套规则，普通文本仍实时输出。工具调用阶段与明确拒答按独立协议项处理；压缩始终生成真实文本摘要。校验失败返回 `bps_invalid_structured_output`：尚未开始响应时为 HTTP 502，已开始 SSE 时为 `response.failed`，不自动改写答案或重试生成。已取得的 usage 仍记录。

## 计费、错误与监测

按实际上游 `usage` 记录输入、缓存及输出 token，沿用现有分组/渠道价格、倍率、日志和用户平台配额。可取得用量的失败响应同样记录用量。

认证失败停止账号调度；过期 token 在调度前被排除。`basispoints_model_access_changed` 和 `model_not_allowed` 统一归为 `bps_model_not_allowed`，按映射后的上游模型冷却 30 分钟，原始错误码保留在诊断中；账号其他模型继续可用。普通 403 不直接判定整个账号失效。参数错误不重试，429 根据 `Retry-After` 冷却。只有尚未绑定上下文且尚未输出的新会话，才能在网络错误、429 或 5xx 时有限换号。

渠道监测选择 OpenAI BPS，填写 **Sub4API 地址和 BPS 分组 API Key**，使用 Responses 探活。监测模板支持 BPS；不提供上游配额探测。

## 验证与上线

仓库测试覆盖压缩轮次保持、连续压缩、旧摘要兼容、显式 Composite 目录、标准模型格式、结构化 JSON/SSE、错误 schema、文档内/外引用、大整数、未验证文本隔离、工具续接与拒答，以及凭证提取/覆盖/过期/脱敏、账号状态保留、平台隔离、两种调度路径、快照身份、请求头、JSON/SSE、未知工具、参数 schema、双层 JSON、连续工具往返、摘要恢复、API Key 隔离、Redis 闲置过期、双模型模拟连接及端点边界。Redis 使用 miniredis，并通过独立客户端验证跨实例读写及原子账号绑定。

本次没有真实 BPS token。两模型的实际权限、真实 Codex 工具与压缩闭环尚未验收；迁移目前通过 SQL 回归断言，未在真实 PostgreSQL 执行。正式启用前，在测试环境应用迁移，用实际账号分别验证文本、SSE、function/custom/namespace、`update_plan`，以及“压缩 → 新一轮对话 → 工具调用”。

主要验证命令：

```sh
# backend/
go test -tags=unit ./internal/service ./internal/handler/... ./internal/server/routes ./internal/repository ./migrations \
  -run 'Test(OpenAI|Composite|ResolveComposite|QuotaPlatform|BuildCodexModels|FilterCodex|Channel|SchedulerCanonical|SchedulerMetadata|FilterSchedulerCredentials|AccountFromService|UpdateAccountPreserves|CreateAccount)' -count=1
go build -tags embed ./cmd/server

# frontend/
pnpm run build
pnpm exec vitest run src/components/account/__tests__/OpenAIBPSAccountFields.spec.ts \
  src/components/account/__tests__/CreateAccountModal.spec.ts \
  src/components/account/__tests__/EditAccountModal.spec.ts \
  src/components/keys/__tests__/UseKeyModal.spec.ts
```

协议参考：[Nonary/ghcp_proxy 的固定版本实现](https://github.com/Nonary/ghcp_proxy/blob/ad23ce2db3b5212c0355762d981c3877322fb160/excel_upstream.py)（Unlicense）。该 BPS 协议不是公开承诺稳定的 OpenAI API 契约；真实上游兼容性以测试账号验收为准。

固定版本的协议移植和对照来源见 [openai-bps-sources.md](openai-bps-sources.md)。

管理接口的完整详情与精简列表提供只读 `bps_credential_state`，包含状态、Token 到期时间、最近发现时间和错误码；原有 `credentials_status` 存在性布尔字段保持不变。认证失败只更新与本次请求 AT/账号 ID 匹配的记录，旧请求不会标记新凭证失效；诊断与调度快照通知在同一事务中保存。自然过期在本地计算，上游提前撤销由正常调用或连接测试反馈，不进行后台凭证探测。
