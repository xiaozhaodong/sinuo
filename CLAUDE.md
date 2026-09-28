# CLAUDE.md

本文件为 Claude Code 提供项目指导信息。

## 项目概述

**AI Switchboard**（工程代号 cc-forwarder-desktop） 是一款基于 Wails 的跨平台桌面应用，用于 Claude / OpenAI 请求转发、端点故障转移、请求追踪，以及 ChatGPT Codex 账号池管理。

- 技术栈：Go + Wails v2 + React + Vite + SQLite
- 平台：macOS / Windows / Linux
- 版本号以 `main.go` 与 `wails.json` 为准，不在此文档中硬编码

### 当前核心能力

- 多端点智能路由、健康检查、故障转移与自愈
- Claude 模型路由：按原始请求模型为每个模型单独指定一组上游端点；支持自动、优选、固定和独立 retained，未分配模型明确返回 422
  - 交互语义是「一个模型 → 一组端点」，一条映射独占一个后端上游组（组名自动跟随模型名，不在 UI 暴露）；删除映射级联回收该组
  - `model_upstream_groups` + `model_routing_assignments` 的写入走单事务聚合 API（`ModelRouteBinding`），避免建组成功、建分配失败留下孤儿组
  - 配置加载失败按 fail-closed 处理：开关处于打开状态时所有 Claude 请求返回 422（`model_routing_load_failed`），不静默回落到全局端点路由；开关从未打开过则保持全局路由
  - `/v1/messages/count_tokens` 的 422 契约取决于「Token 计数」开关：开启时由 `CountTokensHandler` 拦截，未分配模型一律降级为本地估算、不返回 422；关闭时 count_tokens 是普通透传路径，与 `/v1/messages` 同样 fail-closed 返回 422（错误信息会提示开启 Token 计数）
  - 模型路由只作用于精确的 `/v1/messages` 与 `/v1/messages/count_tokens`，其他进入端点管线的路径即便 body 带 `model` 也保持全局路由
  - 联网工具映射：`/v1/messages` 请求 `tools[].type` 以 `web_search_` / `web_fetch_` 开头（只认 type，不认 name，不区分日期后缀）时优先命中「联网工具」映射，没配或停用则回落到模型映射；`count_tokens` 不参与联网覆盖，始终按模型映射归组；全局仅一条，存为 `model_routing_assignments.model_name = server_tool:web` 保留键（不改表结构），用户模型名禁止 `server_tool:` 前缀；前端以 `kind = model / web_tools` 区分，不接触保留键
- 桌面端启动完成后异步执行端点与账号池批量连通性检查
- 请求生命周期追踪、Token/成本统计、SQLite 持久化
- 数据库时间统一 UTC：真实时间点以固定微秒精度 UTC 文本存储，前端按顶层 IANA `timezone` 显示并生成业务日期范围（DST 日按真实 23/25 小时计算）
  - `timezone` 留空或写 `local` 时自动跟随系统时区（`internal/timezone/system.go`，探测失败回退 `Asia/Shanghai`）；迁移解释无 offset 历史数据：留空或小写 `local` 钉死 `Asia/Shanghai`（旧版实际回退口径），精确 `Local` 按探测的系统时区解释、探测失败中止迁移要求显式配置；失败重试优先使用 manifest 已固定的时区口径
  - 启动时对比 `settings` 表中上次运行时区，变化仅打日志提示，不做数据处理
  - SQL 读取时间列必须 `CAST(col AS TEXT)`；`internal/timezone.DBTime` 拒收驱动解析出的 `time.Time`，违规查询首行即报错
- 独立图像 API 代理：设置页维护单一 OpenAI 兼容生图 URL / API Key / 每次固定价格，`POST /v1/images/generations` 与 `POST /v1/images/edits` 不进入端点或账号池调度，但保留请求追踪、成本与 prompt 隐私扫描
- Codex `/v1/responses/compact` 已接入账号池路由、请求追踪与 OAuth compact 上游透传
- Claude `/v1/messages` 与 Codex `/v1/responses` 流式尾部断连保护：
  - 独立 upstream context
  - tail drain 补齐 terminal event / usage
  - 已完成后的 `context canceled` 按 `completed` 处理
- Wails 前端页面：概览、Claude 管理（端点 / 客户端接入）、Codex 管理（账号池 / 客户端接入）、请求追踪、配置、日志
- ChatGPT OAuth 授权链接生成、回调兑换、RT / id_token 存储
- Codex 账号池：单账号测试、启动批量连通性检查、分组编排、冷却、鉴权失效处理
- 账号池编排：主组 / 备组 / 冷备、组内首选账号、全局固定账号、最近一次调度快照
- 账号画像与额度刷新：
  - `plan_type`
  - `chatgpt_account_id / chatgpt_user_id / organization_id`
  - `quota_5h_* / quota_weekly_* / quota_status / quota_refreshed_at`
- Codex 账号池 `api_key` 账号成本倍率：
  - `cost_multiplier`
  - `input_cost_multiplier`
  - `output_cost_multiplier`
  - `cache_creation_cost_multiplier`
  - `cache_creation_cost_multiplier_1h`
  - `cache_read_cost_multiplier`
- Codex 账号池 `api_key` 账号模型兼容改写：
  - `model_rewrite_rules`
  - 当前前端支持多条精确匹配规则，例如 `gpt-5.4 -> gpt-5.5`、`gpt-5.4-mini -> gpt-5.5`
  - 不按渠道自动生成规则，所有改写规则都由前端显式启用并维护
- Claude 端点模型兼容改写：
  - `endpoints.model_rewrite_rules`
  - 前端支持多条精确匹配规则，默认关闭，不按渠道自动生成规则
  - 固定作用于 `/v1/messages` 与 `/v1/messages/count_tokens`；故障转移时每个候选端点都从原始请求体重新应用自己的规则
- Responses 计费口径修正：`/v1/responses` 的 `input_tokens` 已含缓存读，实际输入计费需按 `input_tokens - cache_read_tokens`
- 请求追踪记录请求 / 上游 / 响应模型与思考等级，一致性由前端派生（v6.3）：
  - `request_logs` 新增 5 列 `request_model / upstream_model / response_model / request_reasoning / response_reasoning`（TEXT DEFAULT ''），`model_name` 仍是计费模型、所有计费口径不变
  - 请求两项在 `handler.go` 入口同步解析（`reasoning.ParseRequest`）并随 start 事件原子写入；上游模型每 attempt 覆盖（改写目标或空串 = 与请求相同）；响应两项与终态事件（`ResponseDiagnostics`）同一事件落库
  - 思考串文法：空格分隔 `key:value`，key ∈ `thinking / budget / effort / reasoning_tokens`；Claude 响应只能观测有无思考块，不做默认值推断；Codex 响应按字段合并 `created` 的 effort 与 `completed` 的 reasoning_tokens
  - 一致性判定只用 `response_model`，别名只认日期快照（`base-YYYYMMDD` / `-latest`），`gpt-5 → gpt-5-mini` 判不一致；不做「仅不一致」筛选、不回填历史
- Claude 端点手动解除冷却：端点页一键清除冷却阻断，故障 epoch + 写时 fencing 拒绝旧在途请求的晚到故障结算；tombstone 单事务落库，失败置位 pending 保留页面重试入口（`ResetEndpointFailureState` / `ApplyEndpointFailureSettlement` / `SetEndpointCooldownFenced` / `RecordSoftFailureFenced` / `SetScopedCooldownFenced`）
- Claude Code 订阅直通（本机 claude.ai 登录透传，与 Codex 订阅直通对齐）：
  - Claude Code 专用 loopback 入口（默认 `127.0.0.1:18088`，端口被占用即启动失败、不漂移；不经 AuthMiddleware）；配置存 settings 受保护分类 `claude_code_entry`
  - 入口接入模式二选一，热切换不重启 listener：`subscription`（订阅直通）/ `endpoint`（端点，与主端口相同，走全局路由或模型路由）；旧值 `auto` 按订阅直通、`api` 按端点读取。订阅与第三方**不混用**，订阅不是端点、不参与模型路由
  - 订阅直通不进端点调度：固定发往 `https://api.anthropic.com`，透传客户端 OAuth（OAuth token 不落库、不写日志；只为额度查询在内存保留最近一次，最后一个订阅请求结束 30 分钟后或查询返回 401/403 时丢弃），count_tokens 也直发官方、不受「Token 计数」开关影响；未带 Authorization 时本地返回 401 说明原因
  - 按透明网关处理：4xx / 5xx 原样透传，不冷却、不重试；流式成功带回 `anthropic-ratelimit-unified-*` 等上游头；连接失败以 Anthropic 错误格式返回 502 / 504
  - 追踪记为 `upstream_type=subscription`、上游名 `claude-subscription`（与 Codex 的 `chatgpt-subscription` 并列），请求页按「订阅直通」展示；有意按 API 价格计费；隐私规则按 `subscription` 上游类型匹配
  - 订阅额度只在入口**已启用且处于订阅直通模式**时工作：App 应用入口配置时（启动、保存、快捷切模式）先切开关再生效；切走或停用时立即丢弃查询凭据、取消待发补查、拒绝手动刷新，已采集的读数保留、切回后继续展示
  - 订阅额度两个来源，都只存内存、不落库：
    - 响应头 `anthropic-ratelimit-unified-*`：每个订阅响应采集（5h / 7d 已用、整体 status / reset / overage-status），零额外请求；只有带 `unified-status` 的响应才整份替换，count_tokens 等不带额度头的响应保留旧值；`rejected` + `overage-status=allowed*` 表示正在消耗额外用量而不是被拒绝
    - 主动查询 `GET /api/oauth/usage`（与 Claude Code `/usage`、claude.ai 用量页同源，带 `anthropic-beta: oauth-2025-04-20`）：补上网页等其他客户端的用量、按模型周额度与额外用量
  - 主动查询从严节流、全在后端卡住：订阅请求全部结束静默 3 分钟后补查一次，两次自动补查至少间隔 10 分钟（不足则推迟、不丢弃）；手动刷新至少间隔 60 秒；同一时刻只有一个查询；429 按 Retry-After 退避（缺省 10 分钟）；**不做定时轮询**，不用 Claude Code 时不发请求
  - 展示在请求页 Claude 按钮：订阅直通模式下角标显示剩余更少的窗口，「接入模式」页签展示完整额度与刷新按钮；5h / d7 取两个来源中较新的一份。状态经独立事件 `claude-code-entry:rate-limit` 推送（带递增 `revision` 防乱序），不走 `claudeCodeEntryMu`
  - 端点模式的入口请求转发第三方时删除客户端带来的 `anthropic-beta` 中 `oauth-` 前缀值（端点自定义头显式配置的以配置为准），并与订阅直通共用来源头名单，剔除 Cookie / Proxy-Authorization / Forwarded / X-Forwarded-* / Origin / Referer；主端口请求永远只走端点，来源头保持原样透传
- 出站隐私保护（v6.1）：
  - 单一模式字段 `关闭 / 仅检测 / 脱敏转发`，默认关闭
  - 规则存 SQLite（`privacy_settings` / `privacy_rules`），保存先编译后落库，原子热替换不重启
  - 按 JSON 文本字段扫描（含 Claude `tool_result` 与 Codex `function_call_output.output`），offset-aware 替换保证零命中 byte-identical
  - `PrivacyPolicyError` 短路：不进端点重试/failover、不冷却账号、不换号
  - requestID + scopeFingerprint attempt cache：重试不重复扫描、不重复计命中
  - 只记录规则名/命中数/耗时，不记录命中原文
  - Claude 请求跳过 `system[0].text` 上的 attribution block（`x-anthropic-billing-header:` 开头）：官方只在它原样位于首位时剥离；thinking / signature / redacted_thinking 不在扫描范围

## 关键文件

### 后端

- `main.go`：程序入口与版本号；`ErrorFormatter` 让后端 error 统一以 `{message}` 回传前端（Wails 默认只回传字符串，前端 `err.message` 会是 undefined）
- `app.go`：Wails App 初始化与服务装配
- `app_startup_connectivity.go`：启动完成后的端点/账号池异步批量连通性检查
- `app_api_account_pool.go`：账号池 Wails API
- `app_api_openai_oauth.go`：ChatGPT OAuth API
- `app_api_privacy.go`：隐私保护 Wails API
- `app_image_generation.go`：图像生成设置校验与代理配置提供器
- `internal/privacy/`：隐私规则纯引擎（编译、JSON walker、span 替换、预设）
- `internal/service/privacy_service.go`：隐私规则编译先行、原子快照热替换、运行统计
- `internal/store/privacy_rules.go`：`privacy_settings / privacy_rules` 存储层
- `internal/proxy/handlers/privacy.go`：PrivacyFilter 接口、attempt cache、策略短路 helper
- `internal/proxy/image_generation.go`：独立 Image API 共享转发、错误处理与请求追踪
- `internal/proxy/image_edits.go`：`/v1/images/edits` JSON / multipart 请求适配、默认模型注入与 prompt 隐私处理
- `internal/accountauth/openai_profile.go`：OAuth 画像解析
- `internal/accountauth/openai_refresh_token.go`：RT -> AT 刷新与缓存
- `internal/service/account_pool.go`：账号池 CRUD、测试连通性
- `internal/service/account_pool_startup_connectivity.go`：账号池启动连通性批量检查摘要
- `internal/service/account_pool_groups.go`：`primary / backup / cold` 分组推断与排序
- `internal/service/account_pool_runtime_cache.go`：账号池运行态缓存、组内首选与固定账号恢复
- `internal/service/account_pool_scheduler.go`：可调度账号排序与最近一次调度快照
- `internal/service/account_pool_profile.go`：账号画像与 quota 刷新
- `internal/store/account_pool.go`：`upstream_accounts` 存储层
- `internal/modelrewrite/`：Codex 账号池与 Claude 端点共用的模型改写解析、校验与匹配引擎
- `internal/reasoning/`：请求 / 响应模型与思考配置提取的纯引擎（归一化 token 串、按字段合并的 `ResponseState`）
- `internal/proxy/lifecycle_manager_diagnostics.go` / `pipeline_response_diagnostics.go` / `token_parser_reasoning.go`：模型一致性诊断字段在生命周期管理器、两条管线与 TokenParser 的接入点
- `internal/tracking/response_diagnostics.go`：终态随 success / final_failure / settlement 原子落库的 `ResponseDiagnostics` 与共用 SQL 片段
- `internal/modelrouting/`：Claude 模型到端点集合的精确匹配编译与校验引擎
- `internal/endpoint/route_scope.go`：全局、模型端点集合与未分配请求的统一路由作用域契约
- `internal/service/model_routing_service.go` / `app_api_model_routing.go`：模型路由配置、运行态和 Wails API
- `internal/service/model_routing_bindings.go`：「模型 → 端点集合」的事务聚合读写（专属组建/改/回收、历史共享组分裂）
- `internal/store/endpoint.go` / `internal/service/endpoint.go`：Claude 端点 `model_rewrite_rules` 的持久化、校验与运行时配置同步
- `internal/proxy/account_pipeline.go`：账号池转发链路
- `internal/proxy/handlers/streaming.go`：非账号池 streaming 重试与 tail drain 入口
- `internal/proxy/handlers/forwarder.go`：Claude 端点普通/流式 upstream request 构造与端点级模型改写；端点管线与订阅直通共用出站核心 `sendUpstream`；入口请求转第三方时剔除 OAuth beta 与来源类头
- `internal/proxy/claude_subscription.go` / `internal/proxy/handlers/claude_subscription_forward.go`：Claude Code 订阅直通分支（透明透传、追踪、复用端点管线的响应处理）与出站请求头策略
- `internal/proxy/claude_subscription_ratelimit.go` / `claude_subscription_usage.go`：订阅额度响应头解析、`/api/oauth/usage` 查询与解析
- `internal/proxy/claude_subscription_quota.go`：订阅额度内存状态、开关（入口启用且订阅直通）、请求结束后静默补查、节流 / 退避与查询凭据过期
- `app_claude_subscription_rate_limit.go`：订阅额度 Wails API（读取 / 手动刷新）与事件
- `internal/endpoint/claude_code_entry.go`：入口接入模式常量（含旧值归一）与入口上下文标记
- `internal/proxy/claude_code_entry.go`：Claude Code 入口 handler（Host / Origin / 路径校验）、运行态与可热更新的入口策略
- `internal/service/claude_code_entry_settings.go` / `app_claude_code_entry.go`：入口配置（受保护分类）与 Wails API；`applyClaudeCodeEntryLocked` 是应用入口配置的唯一入口，同时切换订阅额度开关
- `internal/proxy/stream_processor.go`：流式 terminal / cancellation / tail drain 核心
- `internal/tracking/tracker.go`：统一成本计算、倍率缓存、热池详情口径
- `internal/tracking/archive_manager.go`：归档时成本计算口径
- `internal/tracking/schema.sql`：SQLite schema
- `internal/tracking/sqlite_adapter.go`：SQLite 迁移逻辑

### 前端

- `frontend/src/pages/account-pool/index.jsx`：账号池页面
- `frontend/src/pages/privacy-protection/index.jsx`：隐私保护页面（规则表格 / 编辑抽屉 / 测试面板 / 预设导入）
- `frontend/src/pages/account-pool/components/SchedulerDrawer.jsx`：调度编排抽屉
- `frontend/src/pages/account-pool/components/GroupBoardCard.jsx`：主组 / 备组 / 冷备运行态卡片
- `frontend/src/pages/account-pool/utils/dashboardViewModel.js`：账号 inventory 与编排视图模型
- `frontend/src/pages/requests/components/AccountPoolSwitcher.jsx`：请求页 Codex 账号切换器
- `frontend/src/utils/api.js`：HTTP/Wails 统一 API 封装
- `frontend/src/utils/wailsApi.js`：Wails 绑定适配层
- `frontend/src/utils/errors.js`：错误归一化（Error / `{message}` / 字符串三态），错误提示取文案统一走 `resolveErrorMessage`，勿直接写 `err.message ||`
- `frontend/src/pages/endpoints/components/model-routing/`：模型路由抽屉（`ModelRoutingDrawer`）、映射行、编辑弹窗、端点多选器与试算面板
- `frontend/src/pages/endpoints/utils/modelRouting.js`：映射草稿、策略自洽与前置校验纯函数
- `frontend/src/pages/claude/index.jsx`：「Claude 管理」页（「端点 / 客户端接入」两个页签，页签条 `components/layout/PageTabs.jsx` 与 Codex 管理共用）；端点页以 `embedded` 嵌入
- `frontend/src/components/claude-code-entry/`：Claude Code 客户端接入卡片（启停、端口、接入模式、试用命令与配置片段）与共用状态 hook `useClaudeCodeEntry`；订阅额度面板 `SubscriptionRateLimitPanel`、hook `useClaudeSubscriptionRateLimit` 与纯函数 `subscriptionRateLimit.js` 也在这里，由请求页 Claude 按钮使用
- `frontend/src/pages/requests/components/ClaudeAccessSwitcher.jsx`：请求页 Claude 按钮；入口未启用时即端点切换器，启用后分「接入模式 / 端点策略」两页（浮层定位 `useAnchoredPanel` 与 Codex 共用）；订阅直通模式下带额度角标与额度区
- `frontend/src/pages/requests/utils/modelConsistency.js`：模型 / 思考等级一致性判定纯函数（`assessModel` / `assessReasoning` / `buildModelAlert`），列表 `ModelTag` 警告与详情「模型与思考」卡片共用
- `frontend/src/wailsjs/go/*`：Wails 生成的前端绑定

## 关键数据流

### ChatGPT OAuth

1. 前端调用 `GenerateChatGPTOAuthLink`
2. 浏览器完成登录并回调
3. 前端调用 `ExchangeChatGPTOAuthCallback`
4. 后端解析 `id_token`，提取账号画像
5. `credential_raw` 存储 RT / AT / id_token / 账号画像字段

### 账号测试与画像刷新

1. 前端点击“测试连通性”
2. 后端 `TestUpstreamAccount` 请求 `/v1/responses` 或 Codex `/backend-api/codex/responses`
3. 成功时写回 `last_success_at`
4. 若为 OAuth 账号，成功后自动触发 `RefreshAccountProfile`
5. `RefreshAccountProfile` 通过 `wham/usage` 更新 quota 字段

### 启动连通性检查

1. `App.startup` 完成初始化后调用 `scheduleStartupConnectivityChecks`
2. 端点批量检查与账号池批量检查均异步执行，不阻塞桌面端启动
3. 账号池仅检测“已启用且有凭据”的账号，默认并发数为 4
4. 启动检测失败只进入日志与摘要，不回写持久化失败状态，也不改写当前编排运行态

### 账号池转发

1. `internal/proxy/account_pipeline.go` 从 store 读取可调度账号
2. 逐个尝试转发
3. `401/403` 标记鉴权失效
4. `429` 或普通 `5xx` 标记瞬时失败并冷却
5. `503 no_available_providers` 直接短路透传，不做整池 failover
6. `/v1/responses/compact` 与 `/v1/responses` 共用账号池链路与请求追踪；OAuth 账号会透传到 `/backend-api/codex/responses/compact`

### 编排与手动固定

1. `group_key` 显式分为 `primary / backup / cold`
2. 若 `group_key` 为空，按优先级推断：`<=10 => primary`、`<=20 => backup`、其余 => `cold`
3. `SetGroupActiveAccount` 只会设置组内首选账号，仅在该组被命中时优先生效
4. `PinUpstreamAccountSelection` 会全局固定具体账号，直到该账号严格不可用
5. `EnableAutomaticAccountSelection` 清除全局固定账号，恢复按编排自动调度
6. `GetLatestAccountScheduleSnapshot` 返回最近一次调度快照，供账号池页面与请求页展示候选决策和最终命中账号

### 流式完成语义

- Claude `/v1/messages` 以 `message_stop` / 完整性判定为完成信号
- Codex `/v1/responses` 以 `response.completed` / 完整性判定为完成信号
- 下游客户端尾部断开时，允许短时继续 drain 上游尾部
- 若 terminal event 已确认到达，则后续 `context canceled` 不再落为 `cancelled`

## 当前账号池与计费语义

- `free`：只有周额度，前端展示为 `d7`
- 非 `free`：展示 `5h` 与 `d7`
- `api_key`：前端默认显示 `5h / d7 = 无限额`
- `api_key`：允许配置 6 个成本倍率字段
- 非 `api_key`：成本倍率固定 `1.0`，前端只读展示或隐藏编辑
- 账号池 inventory 与调度抽屉共用 `group_key / is_group_preferred / is_active_selection / latest schedule snapshot`
- 请求页切换具体账号等价于“固定账号”，不会提升账号层级或改写 `group_key / priority`
- 组内首选账号与全局固定账号是两套语义：前者只影响命中该组后的组内排序，后者直接覆盖当前请求目标
- 账号页进度条和数字统一展示“剩余”
- `/v1/responses`：输入计费口径为 `billable_input = input_tokens - cache_read_tokens`
- `/v1/responses/compact`：同样走 Codex 账号池与请求追踪链路，未就绪时返回 account-pool not ready 错误

## 常用命令

```bash
# Wails 开发
wails dev

# 直接运行 Go 程序
go run . -config config/config.yaml

# 构建
go build ./...
cd frontend && npm run build
```

## 测试建议

日常开发不要默认直接跑全量 `go test ./...`。优先跑修改相关模块。

```bash
# App / Wails API / 启动连通性
go test .

# 账号池 / OAuth / quota
go test ./internal/store
go test ./internal/accountauth
go test ./internal/service
go test ./internal/proxy
go test ./internal/proxy/handlers

# 请求追踪 / SQLite
go test ./internal/tracking

# 前端最小检查
cd frontend && npx eslint src/pages/account-pool/index.jsx src/utils/api.js src/utils/wailsApi.js
cd frontend && npm run build
```

如果修改了以下内容，建议至少补跑对应测试：

- `internal/service/account_pool*.go`：`go test ./internal/service`
- `app.go` / `app_startup_connectivity.go` / `app_api_account_pool.go`：`go test .`
- `internal/proxy/account_pipeline.go`：`go test ./internal/proxy`
- `internal/proxy/handlers/streaming.go` / `forwarder.go`：`go test ./internal/proxy ./internal/proxy/handlers`
- `internal/accountauth/*`：`go test ./internal/accountauth`
- `internal/tracking/schema.sql` / `sqlite_adapter.go` / `tracker.go`：`go test ./internal/tracking`
- `internal/store/account_pool.go`：`go test ./internal/store`
- `internal/privacy/*` / `internal/service/privacy_service.go` / `internal/store/privacy_rules.go`：`go test ./internal/privacy ./internal/service ./internal/store`
- `internal/proxy/handlers/privacy.go` / 隐私链路接入点：`go test ./internal/proxy ./internal/proxy/handlers`
- `frontend/src/pages/privacy-protection/*`：`node --test frontend/src/pages/privacy-protection/utils/privacyRules.test.js`
- `internal/service/model_routing_bindings.go` / `internal/store/model_routing.go` / `app_api_model_routing.go`：`go test ./internal/service ./internal/store .`
- `frontend/src/pages/endpoints/components/model-routing/*`：`node --test frontend/src/pages/endpoints/utils/modelRouting.test.js`
- `internal/reasoning/*` / 模型一致性链路（`lifecycle_manager_diagnostics.go` / `pipeline_response_diagnostics.go` / `token_parser_reasoning.go` / `tracking/response_diagnostics.go`）：`go test ./internal/reasoning ./internal/proxy ./internal/proxy/handlers ./internal/tracking ./internal/migration .`
- `frontend/src/pages/requests/utils/modelConsistency.js` / `ModelTag.jsx` / `RequestDetailModal.jsx`：`node --test frontend/src/pages/requests/utils/*.test.js`
- `internal/service/codex_model_capabilities.go` / `frontend/src/pages/account-pool/utils/codexModelCatalog.js`（Codex 模型能力校验，前后端规则须同步，共用用例 `internal/service/testdata/codex_capabilities_cases.json`）：`go test ./internal/service -run Codex` + `node --test frontend/src/pages/account-pool/utils/codexModelCatalog.test.js`
- `main.go` 的 `formatWailsError` / `frontend/src/utils/errors.js`：`go test . -run TestFormatWailsError` + `node --test frontend/src/utils/errors.test.js`
- Claude Code 订阅直通（`internal/endpoint/claude_code_entry.go` / `internal/proxy/claude_code_entry.go` / `internal/proxy/claude_subscription.go` / `handlers/claude_subscription_forward.go` / `forwarder.go` / `app_claude_code_entry.go` / `claude_subscription_{ratelimit,usage,quota}.go` / `app_claude_subscription_rate_limit.go`）：`go test ./internal/endpoint ./internal/proxy ./internal/proxy/handlers ./internal/service .` + `node --test frontend/src/components/claude-code-entry/*.test.js frontend/src/pages/claude/*.test.js frontend/src/pages/requests/utils/claudeRouteLabel.test.js frontend/src/pages/requests/utils/requestSource.test.js`

## 调试提示

- 请求追踪：用 `req-xxxxxxxx` 过滤日志
- Claude Code 订阅直通问题优先看：请求是否来自入口端口（主端口永远走端点）、入口卡片的运行状态与接入模式、请求记录的 `upstream_type`（`subscription` 才是订阅直通）；订阅直通不进端点调度，调度快照里不会有它
- 订阅额度不更新优先看：日志 `[订阅额度]`（补查只在订阅请求全部结束约 3 分钟后发生）、额度区刷新按钮的悬停提示（冷却 / 退避 / 凭据已过期）；角标只在接入模式为订阅直通时显示
- 账号池问题优先看：
  - `group_key`
  - `is_active_selection / is_group_preferred`
  - 最近一次 `latest schedule snapshot`
  - `last_success_at`
  - `quota_status`
  - `quota_refreshed_at`
  - `model_rewrite_rules`
  - `state`
  - `cost_multiplier / input_cost_multiplier / output_cost_multiplier`
- OAuth 画像问题优先看：
  - `credential_raw` 中的 `id_token`
  - `plan_type / chatgpt_account_id / chatgpt_user_id / organization_id`
- 前端账号页问题优先看：
  - `frontend/src/pages/account-pool/index.jsx`
  - `frontend/src/utils/api.js`
  - `frontend/src/utils/wailsApi.js`
