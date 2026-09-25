# BPS 协议实现来源

本项目的 BPS 适配保留 Sub4API 自身的独立平台、账号调度、Redis 状态、计费和 HTTP 入口。下列固定版本用于协议移植和兼容性对照；不导入其他项目的部署配置、插件 ABI 或凭证采集逻辑。

- `ranxi2001/sub2api`，提交 `055a1cd1470b3b866d04aad8d1514bd34cac73ab`，`backend/internal/service/basispoints/`：移植工具信封解码、custom 原始文本标记，并适配结构化输出、工具目录和计划参数处理。仓库使用 LGPL-3.0；本项目同样使用 LGPL-3.0。新增源文件注明移植来源。
- `Nonary/ghcp_proxy`，提交 `ad23ce2db3b5212c0355762d981c3877322fb160`，`excel_upstream.py` 和 `responses_replay_ids.py`：工具隧道、完整调用回放、加密 reasoning、计划成功回执和结果 ID 规范化；Unlicense。
- `1812095643/sub2api-excel2api-plugin`，提交 `ad2039077e5a625c846ed405d3473f7bc74b601a`：对照 `tool/args` 信封、轮次保持和工具结果回放；未导入插件二进制或内置静态工具目录。

Go 参考包的 `NOTICE.md` 另注明：原始 BPS 协议源于 hloolx/codex2api（其 README 声明 MIT），相关提交为 `9d02d3f5e5d69632ebb9590082a833c0a0916356`、`c125e560eefb5fd15c995943eb1e111795b0635f`、`20ff3e860d9a149e2df731e37ba1d9b56ae053fc`、`d39f7e3697aab342e303bf4be0142e39b6a58515`、`4dea83ec53b7668419edd2a9a9dd40fb55fdaacd`；并对照 JaxsonWang/cpa-plugin-oai-basispoints 的 `05b2d97efa1bd117da6bd4d362d6e88f8e483680`。这里保留上述来源链。
