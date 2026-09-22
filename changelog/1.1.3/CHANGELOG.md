# v1.1.3

## 新增功能

- 新增 Codex 出口地理对齐：自动把 environment_context 的时区/日期、current_time_reminder 本地时间、web_search user_location 以及 /alpha/search 的 settings.user_location 改写为账号出口地理（代理出口 IP；无代理时用服务器直连出口），避免上游看到 IP 与时区不一致。探测结果按账号缓存 24 小时，失败负缓存 60 秒；命中拒绝名单（默认中国大陆）或探测失败时回退东京，绝不写中国时区。账号级可用 extra.codex_location 覆盖、extra.codex_location_align_enabled 关闭。
- 新增 Codex 官方 TLS 指纹对齐：OpenAI OAuth/SetupToken 账号默认使用官方 CLI 形态（HTTP /responses 为 OpenSSL，Responses WebSocket 为 rustls），可用 extra.enable_tls_fingerprint=false 关闭；绑定指纹模板时以模板优先。
- 打票间隔范围可配置（默认 10~30 秒，边界 1~86400 秒）：新票成功后、票据有效期内、票据无效时的下一次打票都在区间内随机；后台「系统设置 → 网关 → Codex 设置 → 打票间隔范围」可热更新，扫描周期自动取「最小值 - 1 秒」。
- 打票请求改为每次新建 CONNECT 且禁用连接复用，便于打票代理轮换出口 IP。
- 代理探测新增出口时区字段，并在代理列表与测试结果中展示。
- 账号测试与真实转发一致：注入已捕获的 Codex 票据与 Cookie；策略禁止无票时给出警告但不阻断连通性测试。

## 优化改进

- 出口地理探测支持按出口 IP 补查时区（探测端点未返回时区时），并支持无代理账号直连探测开关。
- 出口地理对齐仅在请求真正包含待改写目标时才解析，避免正文出现关键字触发无意义探测；冷缓存最多同步等待 2 秒，探测在后台完成并写缓存。

## 修复

- 修复 WebSocket 入站路径在删除 internal_chat_message_metadata_passthrough 标记之后才做出口地理对齐的问题：WS（ctx_pool 与 passthrough）同样走精确标记路径，关闭启发式时不再静默失效，也不会误改用户粘贴的相似文本。
- 修复出口地理 singleflight 键未包含代理指纹的问题：账号改绑代理瞬间的并发请求各自解析，不再串用另一条出口链路的结果。
