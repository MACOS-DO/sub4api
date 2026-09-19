# Sub4API 部署与发布

## 已有 Docker Compose

只替换应用镜像为 `ghcr.io/macos-do/sub4api:0.2.8`（或 `latest`），保留原服务、网络、卷、数据库参数和挂载路径。不要执行 `down -v`，也不要将新 Compose 的卷名直接套在旧安装上。

镜像同时支持 `/app/sub4api`、旧路径 `/app/sub2api`，以及 `sub4api`、`sub2api` 用户名（相同 UID/GID 1000）。旧 `SUB2API_*` 环境变量仍有效；显式 `SUB4API_*` 值优先，包括显式空值。配置搜索先检查 `/etc/sub4api`，再检查 `/etc/sub2api`，原有 `CONFIG_FILE`、`DATA_DIR` 和 `/app/data` 优先级不变。

已有数据库名、表、迁移校验和不改写。没有显式指定数据库名的旧配置仍使用旧默认名；新 Compose 和安装向导显式使用新名。原生安装脚本检测 `/opt/sub2api/sub2api`，沿用原目录、用户、服务和可执行文件名，避免启动第二个服务。

## 保留的兼容标识

- 已发布 SQL 迁移及插件表名，避免迁移校验失败或丢失插件配置。
- 插件 v1 Protobuf namespace、握手 cookie、UI 消息、内置插件身份及签名 key ID，避免旧插件失效；Go 导入路径已迁移。
- 插件 manifest 同时接受 `requires.sub4api` 和旧字段，显式新字段优先；签名校验继续使用原始字节。
- Redis 键、浏览器 IndexedDB、跨标签页锁、稳定账号身份和加密派生前缀，保证已有状态和上游身份稳定。
- 浏览器语言和协议同意状态自动复制到新键，不覆盖显式新值。
- 数据管理 daemon 的旧 socket 路径仍兼容已有挂载；原生 daemon 升级沿用原目录及服务。
- 旧 `/v1/sub2api/billing` 路由及运维 WebSocket subprotocol 继续接受。

尚未升级的旧二进制仍内置上游仓库地址，必须先替换镜像或使用当前仓库安装脚本升级一次；新的 Release 不能改变已经安装的旧程序。新发布提供旧名称附件，供兼容更新器下载。

## 自动发布配置

仓库 Secrets 配置 `DOCKERHUB_USERNAME` 和 `DOCKERHUB_TOKEN`。GitHub 内置令牌需允许写入 contents 和 packages，版本 Tag 允许更新，Release 不启用不可变保护。

main push 自动读取现有版本逻辑和 `changelog/<版本号>/CHANGELOG.md`，构建双架构镜像及五个平台安装包。Tag push 和手动指定 Tag 使用相同流程。运行中的发布不中断；排队的旧 main 提交会跳过。同版本发布会覆盖镜像标签、Tag、正文和同名附件。

GitHub Actions 支持 `concurrency.queue: max`（最多 100 个等待运行）；超过队列上限的运行由 GitHub 取消。两个镜像仓库及附件上传不具备事务性，部分失败后可以重跑。发布到新仓库的 GHCR 包是否公开需在 GitHub Packages 中确认。

本地回归：`python3 scripts/release/test_release.py`。`deploy/tests/fixtures/legacy-compose.yml` 保留改名前的 Compose，仅替换应用镜像，用于真实部署验证。请在隔离的 Docker 环境运行，因为该原始 Compose 含固定容器名。
