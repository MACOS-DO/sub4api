# v1.1.11

## 上游同步

- 对齐 SUB2API 0.2.17，包含 0.2.16、0.2.17 的完整代码增量；上游提交：`b0a02a3304a0a78cdd003ddda1dd23ba02c03084`。
- 新增 Gemini 显式缓存、Vertex API Key 认证、Gemini 4 Argon、兼容入口思考档位和流式思考摘要。
- 新增分组多语言展示、可配置最小输出 token，并修复 Responses `created_at`、Codex 工作区导入和模型调度问题。
- 合并支付订单类型校验、退款待定扣减和失败回退、支付宝退款查询、易支付查单确认及 Stripe 支付页修复。

## Sub4API 兼容与打包

- 保留 `github.com/MACOS-DO/sub4api` 模块路径、OpenAI BPS、Codex 定制和本仓库的在线更新源。
- 调度缓存同时保留 BPS 工作区信息与 Gemini 显式缓存能力；合并 composite OAuth 路由保护与 BPS 排除规则。
- 部署模板默认使用 `ghcr.io/info-talent/sub4api:latest`，可通过 `SUB4API_IMAGE` 指定私有镜像或固定版本。
- 保持本仓库的 GHCR / Docker Hub 发布流程，以及 `sub4api` 安装包和旧更新器需要的 `sub2api` 兼容包。
- 发布地址与在线更新源改为 `info-talent/sub4api`；GHCR 可直接使用 GitHub Actions 的 `GITHUB_TOKEN` 发布，Docker Hub 仅在配置两项凭据后启用。
- 前端包名改为 `sub4api-frontend`，版本同步为 1.1.11，并固定 pnpm 9.15.9。
- 修复 Windows 交叉打包时 Unix 安装包丢失可执行权限的问题；归档显式保留二进制和部署脚本的 0755 权限，并新增 CI 打包回归检查。

## 升级说明

- 启动时自动应用新增的分组 i18n 和 Gemini 缓存迁移。迁移以完整文件名识别，不修改已经发布的 SQL 文件；升级前请备份 PostgreSQL。
- 上游已停止向 ChatGPT OAuth / SetupToken 账号调度 `gpt-5.5`；使用该模型的站点需要调整模型映射或使用 API Key 账号。详见 `docs/OPENAI_GPT55_RETIREMENT.md`。
- 保留已有服务名、数据目录和容器内 `sub2api` 可执行文件名，兼容现有部署与更新器。
