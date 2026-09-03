# Changelog

所有项目的重要更改都将记录在此文件中。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/)，
项目遵循 [语义化版本控制](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### ✨ 新功能 (Features)

- **Claude 模型路由**：可按原始请求模型为每个模型单独指定一组上游端点，复用自动、优选、固定与故障转移语义，未分配的模型明确返回 422。编排入口是端点页头部的「模型路由」抽屉：一行一个模型，端点用搜索 + 勾选选取，可从现有模型一键复制端点集合，配套端点运行态徽章与路由试算。
- **模型路由请求观测**：调度快照透出作用域、命中的端点集合、请求模型、成员缺失与未分配原因；请求页在模型路由启用时禁用全局端点切换入口。

## [5.3.0] - 2026-08-31

### ✨ 新功能 (Features)

- **Claude 端点扁平化管理**：移除 channel/group/组密钥和单激活端点模型，端点改为 SQLite 中的独立记录。
- **启动迁移与恢复页**：迁移前自动备份数据库和配置，校验完整性与 SHA-256；失败时进入只读恢复状态并支持重试。
- **请求来源统一**：历史记录改用 `request_family` 和 `upstream_*` 表达 Claude、Codex、Image 与 Other 请求的实际上游。
- **配置时区全链路收敛**：数据库时间点统一为固定微秒精度 UTC，前端按顶层 IANA `timezone` 显示和生成业务日期范围。
- **UTC 历史迁移**：新增独立备份、格式预检、表重建、汇总缓存重置、外键/完整性校验、幂等重试与只读恢复流程。
- **端点手动解除冷却**：冷却中的端点可在端点页一键解除，立即移除冷却阻断并重置失败计数；故障 epoch + 写时 fencing 保证清除前在途请求的晚到故障结算零残留。
- **Claude Gateway 模型目录**：端点页可维护独立的模型展示目录；带 `anthropic-version` 的 `GET /v1/models` 本地返回任意合法模型 ID，不参与端点能力判断或调度；配套客户端补丁可保留全局非必要流量限制，仅放行目录发现且无需单独设置 discovery 开关。
- **故障转移统一管线**：端点侧决策表、调度器与 `activeEndpoint` 持久化协调收敛为单一管线，旧 v3 RetryHandler 与旧故障转移体系整体退役；账号池支持请求内受限换号，并输出四事件转发 trace。
- **端点调度快照**：新增调度决策观测面板，记录候选端点、跳过原因与最终命中；页面常态收进抽屉，仅在异常时显示警示条。
- **故障转移通知**：端点发生转移时给出桌面提示，不再需要盯日志。
- **Claude 端点模型兼容改写**：`endpoints.model_rewrite_rules` 支持多条精确匹配规则，默认关闭；固定作用于 `/v1/messages` 与 `/v1/messages/count_tokens`，故障转移时每个候选端点都从原始请求体重新应用自己的规则。
- **请求生命周期面板**：请求详情展示分段耗时（排队/连接/首字/生成/尾部）、调度快照与隐私扫描结果，终态段落改为短标签 + tooltip 全文。
- **全站暗黑模式**：新增语义色 token 与 `tone-*` 徽章层，概览、端点、请求追踪、账号池、隐私、定价、设置、日志、配置、迁移各页完成适配；复选框改为自绘以摆脱插件依赖。
- **请求列表实时动效**：新增行入场 stagger、进行中「请求轨道」、指标 count-up 平滑过渡与耗时跑秒。轨道按 `firstTokenMs / completionMs` 真实比例着色，让「卡在等首字」与「正常生成」一眼可辨；无进行中请求时整表零定时器零动画。

### 🔧 改进 (Improvements)

- Claude 端点页改为高密度表格，支持硬启用、自动调度、手动优选/固定、批量健康检查与调度快照。
- 端点 Token 和 API Key 可单独或同时配置，编辑时不回显明文，删除凭据需显式操作。
- CodeRelay 不再改写业务请求体；AnyRoute 仅对精确 HTTPS 主机名应用兼容头。
- Codex `/v1/models` 在本地目录和账号池都不可用时返回稳定的 503，不再回退 Claude 端点。
- 请求、账号、端点、日志、OAuth、quota 与调度时间 API 统一返回 UTC；日期筛选使用配置时区和半开区间，支持 DST 23/25 小时日。
- `usage_summary` 收敛为活动时区最近 7 个业务日缓存；查询超出缓存覆盖范围或热切换缓存未就绪时，整段从请求明细聚合，缓存与实时路径保持相同分页语义。
- UTC 迁移 completed ledger 的正常启动只做 schema 快检，避免冷启动反复扫描全部请求历史；首次迁移仍执行完整数据校验。
- 解除冷却的持久化 tombstone 单事务原子写入（global + messages 两行），落库失败时保留页面重试入口，重启后冷却自然回归展示。
- 请求页改为 Wails 事件驱动实时刷新，取代轮询；页面切走时订阅与定时器全部释放。
- 账号池与端点管理工作台重做，账号卡片补充时间悬停提示并修复操作按钮溢出。
- 统一模态生命周期：全量接入滚动锁定、Escape 栈与焦点管理；编辑类弹窗统一为 portal 真模态。
- Codex 支持 zstd 与 ChatGPT OAuth 请求压缩，搜索端点路由收口为仅走 OAuth。
- 流完整性判定改为终态事件驱动，并增加 content block 配对检查。
- 请求追踪模型标签补充 deepseek / kimi / glm 配色，并优化长模型名展示。
- 前端 ESLint 存量问题清零（6 error + 14 warning）。

### 🐛 修复 (Bug Fixes)

- 修复端点前置短路分支的上游归属与路由上下文丢失。
- 修复账号池失败请求未保留真实上游的问题。
- 修复生命周期终态标签归类与模型目录排序。
- 修复 `useSSE` 条件调用 hook 与快照卡 effect 级联渲染。
- 修复隐私敏感值删除确认与掩码展示。
- 校验生图上游响应协议，避免非法响应落库。
- 用量统计移除当前窗口的运行时兜底，重启恢复的冷却时间转回本地时区。
- 修复 Release 工作流的版本注入：`-ldflags` 原先写的是 `main.version`（小写），与 `main.go` 中声明的 `Version` 不匹配，导致注入静默失败、发布包版本恒为硬编码值；同时补上 `Commit` 与 `BuildTime` 注入。

### ⚠️ 升级注意 (Upgrade Notes)

- 运行时仅支持 SQLite 端点存储；旧 YAML `endpoints`、`group`、`tokens` 和 `api-keys` 只在首次升级时读取。
- 旧 API 和旧数据库列已物理删除；代码降级必须同时离线恢复迁移前数据库与配置备份。详见 `UPGRADING.md`。
- `usage_tracking.database.timezone` 已弃用且不得与顶层 `timezone` 冲突；UTC 迁移前同文件系统可用空间必须至少为活动数据库大小的 3 倍。

---

## [5.2.5] - 2025-12-24

### ✨ 新功能 (Features)

- **端点管理页面卡片式布局**: 全新的端点管理界面
  - 新增 `ChannelCard` 组件：按渠道分组显示，默认展示 2 个端点，支持展开/折叠
  - 新增 `EndpointCard` 组件：紧凑单行布局，显示核心信息（名称、状态、URL、优先级、延迟等）
  - 一行显示两个渠道卡片，信息密度更高
  - 点击 URL 图标可在系统默认浏览器打开网站
  - 点击 Key 图标可快速复制 Token
  - 激活的渠道卡片显示紫蓝色边框高亮
  - 统一使用 indigo 色系，视觉更协调

---

## [5.2.4] - 2025-12-24

### 🐛 Bug 修复 (Bug Fixes)

- **配置热更新修复**: 修复故障转移和挂起功能配置修改后不生效的问题
  - 在 `TriggerRequestFailover` 添加 `Failover.Enabled` 开关检查，关闭故障转移后不再触发
  - 统一所有组件使用 `Failover.Enabled` 字段，移除废弃的 `Group.AutoSwitchBetweenGroups` 兼容逻辑
  - 热更新时同步 `Group.AutoSwitchBetweenGroups` 字段，确保旧代码路径也能正确响应
  - 修复 `SuspensionManager` 和 `RetryHandler` 的挂起判断逻辑

---

## [5.2.3] - 2025-12-20

### 🐛 Bug 修复 (Bug Fixes)

- **EOF 重试机制修复**: 修复 LoggingMiddleware 导致 Flusher 接口断言失败的问题
  - `responseWriter` 包装器未实现 `http.Flusher` 接口，导致流式处理退化为 `noOpFlusher`
  - EOF 发生时 `sendStreamInterruptedMessage` 发送的重试信号无法推送到客户端
  - 新增 `Flush()` 方法正确转发到底层 ResponseWriter
  - 新增 `Unwrap()` 方法支持 Go 1.20+ `http.ResponseController` 解包

---

## [5.2.2] - 2025-12-20

### 🐛 Bug 修复 (Bug Fixes)

- **SuspensionManager 配置热更新**: 修复配置热更新时 SuspensionManager 未同步更新的问题
  - 在 `SuspensionManager` 接口中添加 `UpdateConfig` 方法
  - `Handler.UpdateConfig` 现在会正确调用 `sharedSuspensionManager.UpdateConfig`
  - 挂起相关配置（超时、最大挂起数等）修改后无需重启即可生效

---

## [5.2.1] - 2025-12-20

### 🔧 改进 (Improvements)

- **EOF 重试机制改进**: 使用双 message_start 模式触发客户端自动重试
  - 发送完整的 Anthropic 流式消息序列（message_start → content_block → message_delta → message_stop）
  - 修复 usage 字段使用 Anthropic 风格（input_tokens/output_tokens）
  - 修复模型名称优先级：优先使用从流中解析的模型名称

- **EOF 调试增强**: 流中断时始终保存 debug 文件，无论是否有 Token 信息
  - 方便调试 EOF 错误场景
  - 保存完整的流式数据内容

- **Debug 文件自动清理**: 防止调试文件无限增长
  - 按天数清理：删除 N 天前的文件（默认 7 天）
  - 按数量清理：保留最新的 N 个文件（默认 50 个）
  - 节流机制：每 24 小时最多执行一次清理

---

## [5.2.0] - 2025-12-20

### ✨ 新增功能 (New Features)

- **EOF 重试提示**: 流式传输中断时发送 Anthropic API 标准格式的可重试错误
  - 新增 `eof_retry_hint` 配置项（默认关闭）
  - 发送 `overloaded_error` 类型错误，让 Claude Code 等客户端能自动重试
  - 精确识别流式传输阶段的 EOF 错误，不影响连接阶段的错误处理

- **端点渠道分组显示**: 端点管理页面按渠道折叠/展开显示
  - 端点按渠道（channel）分组，支持折叠/展开
  - 渠道行显示端点数量和健康状态汇总
  - 全部展开/折叠快捷操作
  - 组件拆分重构，提升代码可维护性

### 🔧 改进 (Improvements)

- **请求状态文字优化**: 状态标签更加一致
  - "挂起" → "已挂起"
  - "失败" → "已失败"
  - "超时" → "已超时"

- **CompleteRequestWithQuality 原子操作**: 避免请求完成时的时序问题
  - 一次性完成 status、tokens、duration 和 failureReason 的设置
  - 解决两次独立操作可能导致的数据不一致

---

## [5.1.0] - 2025-12-17

### ✨ 新增功能 (New Features)

- **请求级故障转移**: 实现细粒度的请求级别故障转移机制
  - 单个请求失败时自动切换到备用端点
  - 不影响其他正常请求的路由
  - manager.go 代码拆分，提升可维护性

- **端点 Token/ApiKey 编辑显示**: 编辑端点时显示已保存的认证信息
  - 用户可以查看和修改已配置的 Token/ApiKey
  - 改善端点配置的用户体验

### 🐛 修复 (Bug Fixes)

- **失败请求信息修复**: 修复失败请求 Token 信息丢失和错误信息不显示的问题
  - 确保失败请求也能正确记录 Token 使用量
  - 错误详情正确展示给用户

- **系统日志重复输出修复**: 修复 React StrictMode 导致的系统日志重复输出问题
  - 兼容 React 严格模式的双重渲染特性
  - 避免日志组件重复初始化

---

## [5.0.1] - 2025-12-12

### 🐛 修复 (Bug Fixes)

- **KPI Token 统计修复**: 概览页面的 Token 统计现在包含所有类型
  - 修复前：仅统计 `input_tokens + output_tokens`
  - 修复后：统计 `input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens`
  - 影响范围：今日 Tokens、全部历史 Tokens 两个 KPI 卡片

---

## [5.0.0] - 2025-12-08

### 🚀 重大变更 (Breaking Changes)

#### 架构重构：从 CLI 到桌面应用
- **Wails 桌面应用**: 完全重构为基于 Wails v2 的跨平台桌面应用
- **移除 Web 服务器**: 不再独立运行 Web 服务器，UI 内嵌在桌面应用中
- **移除 TUI**: 移除终端用户界面，统一使用图形界面
- **移除 MySQL 支持**: 桌面应用专注于本地使用，仅保留 SQLite

### ✨ 新增功能 (New Features)

#### 全新桌面体验
- **原生窗口**: macOS/Windows/Linux 原生窗口，支持系统主题
- **透明标题栏**: macOS 风格的透明标题栏设计
- **实时状态指示**: 顶部状态栏显示代理运行状态和端口

#### 端点管理增强
- **渠道分组**: 新增渠道标签，端点按渠道分组展示
- **数据库存储**: 端点配置从 YAML 迁移到 SQLite 数据库
- **可视化管理**: 完整的端点增删改查界面
- **成本倍率**: 支持每个端点独立配置成本计算倍率

#### 使用统计增强
- **今日统计**: 新增今日成本、今日 Tokens、今日请求数
- **全部历史**: 新增全部历史成本和 Tokens 统计
- **KPI 卡片**: 概览页面 8 个 KPI 卡片展示关键指标

#### 内存热池架构
- **热池缓存**: 活跃请求在内存中管理，减少数据库写入
- **异步归档**: 请求完成后异步写入数据库
- **性能提升**: 数据库写入减少约 80%

### 🔧 改进 (Improvements)

- **代理状态检测**: 真实检测端口监听状态，带缓存避免频繁检测
- **请求追踪优化**: 热池 + 数据库双源查询，实时显示进行中的请求
- **前端重构**: React + Vite + TailwindCSS 现代化技术栈
- **事件系统**: Wails 原生事件替代 SSE，更稳定的实时更新

### 📦 构建变更 (Build Changes)

- **构建工具**: 从 `go build` 迁移到 `wails build`
- **跨平台构建**: 支持 macOS (Intel/Apple Silicon)、Windows、Linux
- **应用图标**: 新增应用图标和系统托盘支持

### 🗑️ 移除 (Removed)

- 移除独立 Web 服务器 (`internal/web/`)
- 移除 TUI 终端界面
- 移除 MySQL 数据库支持
- 移除 Docker 部署支持

---

## [3.5.1] - 2025-10-15

### ✨ 功能增强 (Enhancements)

#### 响应头超时配置化
- **可配置超时时间**: 新增 `streaming.response_header_timeout` 配置项
  - 支持自定义响应头等待超时时间，默认值：60秒
  - 推荐配置范围：30s-120s，可根据实际服务响应时间调整
  - 完全向后兼容，旧配置自动使用新默认值
- **优化默认值**: 从硬编码15秒优化为60秒
  - 减少AI服务排队导致的假超时约80%
  - 降低不必要重试和重复计费风险
  - 更好适应Claude API等AI服务的实际响应特性
- **双重保险机制**: 配置未设置时自动使用60秒默认值

### 🔧 内部改进 (Internal Changes)

- **config/config.go**: 新增 `ResponseHeaderTimeout` 字段和默认值设置
- **internal/proxy/handlers/forwarder.go**: 从配置动态读取超时值
- **internal/proxy/stream.go**: 从配置动态读取超时值
- **config/example.yaml**: 新增配置说明和最佳实践

### 📊 性能提升 (Performance)

- **假超时率**: 从 ~20% 降低到 ~2-5%
- **重复计费风险**: 降低约80%
- **用户体验**: 减少不必要的等待和重试

### 📝 配置示例 (Configuration)

```yaml
streaming:
  heartbeat_interval: "30s"
  read_timeout: "10s"
  max_idle_time: "120s"
  # 响应头超时配置 (v3.5.1新增)
  # 控制等待服务端首次响应头的最大时间
  # 对于AI服务建议设置较长时间，以应对排队和预处理
  response_header_timeout: "60s"
```

---

## [3.5.0] - 2025-10-12

### 🚀 重大功能 (Major Features)

#### 状态机架构重构
- **双轨状态管理**: 业务状态与错误状态完全分离
  - 业务状态: `pending` → `forwarding` → `processing` → `completed`/`failed`/`cancelled`
  - 错误状态: `retry`, `suspended` 独立管理
  - 前端可同时显示业务进度和错误原因
- **统一事件系统**: 新增 `flexible_update`, `success`, `final_failure` 语义化事件类型
- **数据库Schema增强**:
  - 新增 `failure_reason` 字段（失败原因类型）
  - 新增 `last_failure_reason` 字段（最后失败的详细信息）
  - 新增 `cancel_reason` 字段（取消原因）
  - 新增 `failure_reason` 索引优化查询性能

#### MySQL数据库支持
- **数据库适配器模式**: 抽象SQLite和MySQL差异，支持多数据库切换
- **新增适配器实现**:
  - `database_adapter.go` (+144行): 适配器接口定义
  - `mysql_adapter.go` (+602行): MySQL完整实现
  - `sqlite_adapter.go` (+337行): SQLite实现
  - `mysql_schema.sql` (+105行): MySQL schema定义
- **连接池管理**: MySQL连接池配置，读写分离优化
- **时区支持**: 统一时区处理，支持全局和数据库级别配置
- **向后兼容**: 保留原有 `database_path` 配置，自动兼容旧配置

#### /v1/messages/count_tokens 端点支持
- **智能转发策略**: 优先转发到配置了 `supports_count_tokens: true` 的端点
- **降级估算**: 转发失败时自动降级到本地字符估算
- **新增配置项**: `supports_count_tokens` 端点配置
- **新增处理器**: `count_tokens.go` (+188行)

#### 端点自愈机制
- **快速恢复**: 从5分钟冷却时间优化到0.7秒自动恢复
- **智能检测**: 监控503/502等服务器错误触发恢复检查
- **主动探测**: 失败端点自动进行健康检查
- **新增模块**:
  - `endpoint_recovery_manager.go` (+119行)
  - `endpoint_recovery_test.go` (+212行)
  - `endpoint_self_healing_integration_test.go` (+249行)

### ✨ 功能增强 (Enhancements)

#### 流式Token处理优化
- **FlushPendingEvent机制**: 解决SSE终止空行缺失导致的Token信息丢失
- **事件缓冲区管理**: 流结束/取消/中断时自动触发flush
- **Token智能合并**: 防止缓存信息丢失，避免重复计费
- **空响应精确判断**: 只在真正无使用量时标记empty_response

#### 响应格式检测优化
- **JSON误判修复**: 修复JSON响应被误判为SSE的bug
- **压缩支持**: 完善Gzip/Deflate响应解析
- **新增测试**:
  - `format_detection_test.go` (+342行)
  - `processor_stream_test.go` (+306行)
  - `processor_unified_test.go` (+128行)

#### Token调试工具
- **Debug开关**: 可配置的Token调试功能
- **数据采集**: 保存完整的请求/响应数据用于分析
- **新增模块**: `internal/utils/debug.go` (+237行)

#### 错误处理增强
- **400错误码重试**: 归类为限流错误，享受与429相同的重试策略
- **Cloudflare错误码**: 520-525错误码智能处理与重试策略
- **错误分类精度**: 完善错误处理优先级，避免误分类
- **挂起策略统一**: 常规模式和流式模式使用统一的重试挂起策略

#### 前端UI升级
- **状态机兼容**: 支持双轨状态显示
- **HTTP状态码**: 新增状态码列显示
- **交互体验**: 请求列表展开/收起、详情弹窗优化
- **样式优化**: CSS模块化改进，视觉层级清晰

### 🧪 测试增强 (Testing)

- **新增测试文件**: 16个新测试文件
  - 状态机测试: `message_start_usage_test.go` (+190行)
  - 数据库测试: `phase2_test.go` (+147行), `sqlite_regression_test.go` (+358行)
  - 响应处理测试: 3个新测试文件 (+776行)
  - 端点自愈测试: 3个新测试文件 (+580行)
- **测试覆盖增强**: 从25+提升到30+测试文件
- **测试场景扩展**: 覆盖状态机转换、数据库适配、格式检测、端点恢复等

### 📝 文档更新 (Documentation)

- **CLAUDE.md更新**: 版本升级到v3.5.0，完整功能说明
- **配置示例**: 新增 `config/mysql_example.yaml`, `config/test_count_tokens.yaml`
- **架构说明**: 更新组件架构图，标注v3.5新增模块

### 🔧 内部改进 (Internal Changes)

- **代码重构**: `lifecycle_manager.go` 大幅重构（~730行）
- **模块化**: handlers和response模块持续优化
- **类型安全**: 引入 `UpdateOptions` 统一更新接口
- **并发优化**: RWMutex保护，支持高并发访问

### 📊 变更统计 (Statistics)

- **代码变更**: 66个文件修改
- **代码增量**: +7,245行 / -1,353行
- **净增代码**: 5,892行高质量代码
- **新增文件**: 16个
- **提交数量**: 24个commits

---

## [3.4.2] - 2025-09-24

### 🔧 错误处理策略优化 (Error Handling Strategy Enhancement)
- **400错误码重试支持**: 将400错误码从HTTP错误重新归类为限流错误
  - 400错误码现在享受与429相同的重试策略（1分钟延迟，最多3次重试）
  - 支持指数退避延迟和组故障转移
  - 修改 `internal/proxy/error_recovery.go` 错误分类逻辑和优先级
  - 更新测试用例验证400错误码重试行为

### 📊 统计指标优化 (Statistics Enhancement)
- **请求追踪页面统计改进**: StatsOverview组件优化
  - 替换"挂起请求数"为"失败请求数"，显示更有价值的实时统计
  - 数据源从硬编码0改为基于数据库的真实失败请求统计
  - UI更新：图标⏸️ → ❌，样式warning → error，文案优化
  - 失败请求统计包含：error, auth_error, rate_limited, server_error, network_error, stream_error
  - 排除独立状态：timeout(超时), cancelled(用户取消)，与前端徽章设计保持一致

### 🛠️ 技术改进 (Technical Improvements)
- **前后端数据一致性**: 统一字段命名 suspended_requests → failed_requests
- **错误分类优先级**: 完善错误分类逻辑，避免400错误被误归类为一般HTTP错误
- **数据准确性**: 基于数据库查询的实时统计替代固定值显示

## [3.4.1] - 2025-09-23

### 🔧 错误处理增强 (Error Handling Enhancement)
- **Cloudflare 5xx错误码支持**: 将Cloudflare专有的5xx错误码归类为服务器错误
  - 支持错误码: 520-525 (Web Server Error, Server Down, Connection Timeout, Origin Unreachable, Timeout, SSL Handshake Failed)
  - 现在享受与502相同的重试策略: 同端点重试 → 切换端点 → 组故障转移
  - 在组故障情况下可触发请求挂起等待组切换
  - 修改 `internal/proxy/error_recovery.go` 错误分类逻辑

## [3.4.0] - 2025-09-23

### 🔧 关键修复 (Critical Fixes)
- **流式Token丢失修复**: 解决上游缺少SSE终止空行导致Token信息丢失的问题
  - 实现 `FlushPendingEvent()` 方法强制解析缓存事件
  - 在流结束、取消、中断时自动触发缓冲区刷新
  - 确保 `empty_response` 只在真正无使用量时出现
  - 新增集成测试验证修复效果 (`streaming_missing_newline_test.go`)
- **端点日志优化**: 修复流式请求端点失败日志中尝试次数计数不准确问题

### 🎨 前端架构升级 (Frontend Architecture)
- **React架构迁移**: 完成Web界面React Layout架构迁移
- **UI优化**: 完善前端状态处理和交互体验
- **图表功能增强**:
  - 新增端点Token使用成本分析功能
  - 优化图表页面布局和优先级
  - 完成概览页面与图表功能融合重构
- **交互改进**: 简化请求页面时间筛选交互

### 📝 技术细节 (Technical Details)
- 修改 `TokenParser.FlushPendingEvent()` 支持 message_delta/message_start/error 事件类型
- 在 `StreamProcessor.waitForBackgroundParsing()` 中调用flush确保完整解析
- 在 `collectAvailableInfoV2()` 错误恢复场景中调用flush保留Token信息

## [3.3.2] - 2025-09-20

### 🐛 错误修复 (Bug Fixes)
- **日志优化**: 消除重复错误分类日志，减少日志冗余
- **性能提升**: 优化错误处理流程，降低系统开销
- **监控改进**: 确保同一错误不会产生重复日志条目

## [3.3.1] - 2025-09-20

### 🔄 重试策略优化 (Retry Strategy Optimization)
- **架构重构**: 重试策略架构全面优化，提升可维护性
- **错误分类**: 完善错误分类系统，提升系统稳定性
- **统一处理**: 统一错误处理流程，减少重复日志问题

## [3.3.0] - 2025-09-20

### 🚀 重大架构更新 (Major Architecture Update)
- **重试策略统一**: 采用统一简化设计，移除过度复杂的适配器架构
- **架构简化**: 大幅简化代码结构，提升系统性能和维护性
- **响应速度**: 简化重试逻辑，提升请求处理速度

## [3.2.2] - 2025-09-20

### 🐛 关键修复 (Critical Fixes)
- **流式请求挂起**: 修复流式请求挂起状态存储问题
- **状态一致性**: 统一RetryCount语义，确保状态管理一致性
- **流式处理**: 优化流式请求的状态管理机制

## [3.2.1] - 2025-09-20

### 💰 Token管理增强 (Token Management Enhancement)
- **失败请求Token保存**: 即使请求失败也能保存Token使用信息
- **重复计费防护**: 防止异常情况下的重复计费问题
- **成本追踪**: 完善成本追踪系统，提升财务透明度

## [3.2.0] - 2025-09-20

### 🔄 重试管理器完善 (Retry Manager Enhancement)
- **413错误处理**: 专门处理Payload Too Large错误
- **HTTP 429优先级**: 修复限流错误分类，正确支持重试和挂起
- **监控指标修复**: 修复错误类型断言失败导致的监控降级问题

### 📖 文档增强 (Documentation Enhancement)
- **开发规范**: 添加AGENTS.md项目开发规范文档

## [3.1.2] - 2025-09-13

### 🎯 用户体验优化 (UX Optimization)
- **模型信息早期显示**: 解决用户在请求处理早期看到"unknown"模型的问题
- **搭便车更新机制**: 通过状态更新"搭便车"实现模型信息早期显示
- **渐进式优化**: 多次更新机会确保最终一致性，用户体验显著改善

### 🛡️ 架构改进 (Architecture Improvements)
- **零延迟转发**: 保持异步解析，主请求流程完全无阻塞
- **线程安全设计**: 使用互斥锁保护更新标记，支持并发环境
- **重复更新保护**: 模型信息仅在首次有效时写入数据库，避免重复操作
- **幂等SQL设计**: UPDATE操作天然幂等，确保数据一致性

### 📈 技术实现 (Technical Implementation)
- **lifecycle_manager.go**: 添加搭便车机制和`modelUpdatedInDB`保护标记
- **tracker.go**: 新增`RequestUpdateDataWithModel`结构和`RecordRequestUpdateWithModel`方法  
- **database.go**: 实现`buildUpdateWithModelQuery`方法，支持`update_with_model`事件类型
- **向后兼容**: 不影响现有功能，渐进式增强

### ✨ 预期效果 (Expected Benefits)
- **早期可见性**: 模型信息在请求处理早期阶段就能显示
- **准确性保证**: 最终仍以SSE解析的权威模型为准
- **性能保持**: 利用现有状态更新流程，最小化额外开销

## [3.1.1] - 2025-09-13

### 🎯 重大优化 (Major Optimization)
- **SQLite读写分离架构**: 专为本地使用设计的数据库并发处理架构
- **写操作队列化**: 实现单写连接 + 队列串行化，彻底消除SQLite锁竞争
- **8读+1写连接模式**: 最大化读性能，完全避免写冲突

### 🔧 关键修复 (Critical Fixes)  
- **SQLITE_BUSY错误**: 彻底解决"database is locked"并发问题
- **事务嵌套问题**: 修复"cannot start a transaction within a transaction"错误
- **持续时间存储**: 确保request duration_ms字段正确保存
- **安全事务处理**: 重构defer rollback逻辑，避免重复回滚

### 📊 架构改进 (Architecture Improvements)
- **读写分离设计**: readDB处理查询，writeDB处理修改，完全适配SQLite单写者特性  
- **队列化写入**: 所有写操作通过队列串行处理，保证数据一致性
- **连接池优化**: 针对SQLite调整连接数配置，避免过度并发
- **完整向后兼容**: 所有现有API保持不变，零配置升级

### 🎯 性能提升 (Performance Gains)
- **消除锁竞争**: 写操作不再竞争，稳定性大幅提升
- **高并发读取**: 8个读连接支持高并发查询操作
- **本地优化**: 专门为本地SQLite使用场景优化

### 📝 技术细节 (Technical Details)
- **文件修改**: tracker.go、database.go、queries.go大幅重构
- **新增结构**: WriteRequest队列，读写分离连接管理
- **事务安全**: committed标志位确保事务正确提交

## [3.1.0] - 2025-09-13

### 🚀 新增 (Added)
- **异步模型解析**: 从请求体中异步提取模型名称，解决`/v1/messages/count_tokens`端点模型显示"unknown"问题
- **智能模型对比**: 请求体与响应中模型不一致时输出警告，以响应中模型为准
- **流式请求标记**: 完整实现流式请求识别功能，支持多种检测模式
- **优化的请求追踪UI**: 去掉操作列，支持点击行查看详情，界面更简洁

### 🔧 修复 (Fixed)
- **端点健康状态图表**: 修复Web界面图表显示错误
- **Token分布图表**: 修复Web图表token分布不显示问题  
- **5xx服务器错误**: 添加专门的服务器错误分类处理
- **流式处理错误**: 优化错误分类与状态管理

### 📈 改进 (Improved)
- **线程安全**: 使用RWMutex保护模型字段，支持并发访问
- **性能优化**: 异步处理，主转发流程零延迟
- **日志增强**: 详细的模型来源标识和不一致警告
- **Token解析重构**: 职责分离，提升代码可维护性

### 📊 技术细节 (Technical Details)
- **异步架构**: goroutine并行处理，请求体副本避免数据竞争
- **智能对比逻辑**: 只在两边都有有效值时进行对比，避免误报
- **优先级策略**: 响应中解析的模型优先级高于请求体模型
- **UI交互改进**: 点击行查看详情，提升用户体验

## [3.0.0] - 2025-09-12

### 🏗️ 重构 (Refactored)
- **Handler.go模块化重构**: 按照单一职责原则完全重构，1194行代码拆分为7个专门模块
- **handlers/模块**: 创建专门的请求处理模块
  - `streaming.go` (~310行) - 流式请求处理器
  - `regular.go` (~311行) - 常规请求处理器  
  - `forwarder.go` (~144行) - HTTP请求转发器
  - `interfaces.go` (~115行) - 组件接口定义
- **response/模块**: 创建专门的响应处理模块
  - `processor.go` (~173行) - 响应处理和解压缩
  - `analyzer.go` (~346行) - Token分析和解析
  - `utils.go` (~21行) - 响应工具方法
- **handler.go精简**: 重构为核心协调器角色（~432行），专注于请求分发

### 🔒 兼容性保证 (Compatibility)
- **API完全兼容**: 所有对外接口保持完全不变
- **功能完全一致**: 所有业务逻辑和处理流程保持完全相同
- **性能无退化**: 重构不引入任何性能开销
- **测试100%通过**: 所有现有25+测试文件继续通过
- **日志格式一致**: 所有日志输出格式和内容完全保持不变

### 🔧 修复 (Fixed)
- **流式请求日志修复**: 修复流式请求日志中显示`endpoint=unknown`问题
- **上下文传递**: 在streaming.go中添加selected_endpoint上下文设置

### 📈 改进 (Improved)
- **可维护性增强**: 模块化设计，职责明确，便于理解和维护
- **可测试性改善**: 每个模块可独立测试，测试覆盖更全面
- **可扩展性提升**: 新功能可在对应模块中扩展，影响范围控制
- **代码可读性**: 逻辑分层清晰，代码结构更加合理

### 📚 文档更新 (Documentation)
- **CHANGELOG标准化**: 创建标准的CHANGELOG.md版本历史记录
- **架构文档更新**: 更新CLAUDE.md反映v3.0模块化架构
- **README精简**: 精简README.md，引用CHANGELOG.md详细历史

## [2.0.0] - 2025-09-11

### 🚀 新增 (Added)
- **Stream Processor v2**: 高级流式处理器，支持实时SSE处理和取消支持
- **智能错误恢复**: 智能错误分类和恢复策略系统
- **完整生命周期管理**: 端到端请求监控和分析
- **综合测试覆盖**: 新增25+综合测试文件
- **统一请求处理架构**: 流式v2和统一v2请求处理架构

## [1.0.3] - 2025-09-09

### 🐛 修复 (Fixed)
- **Token解析重复处理**: 修复Token解析重复处理导致成本计算错误的严重bug
- **状态系统增强**: 增强请求状态粒度，提供更好的用户体验

### 📈 改进 (Improved)
- **错误处理**: 改进错误处理和状态追踪
- **用户体验**: 优化状态反馈机制

## [1.0.2] - 2025-09-10

### 🔧 优化 (Optimized)
- **Web处理器重构**: 11个专门处理器文件的模块化架构
  - `dashboard_handlers.go` (312行) - 仪表板数据处理
  - `endpoint_handlers.go` (421行) - 端点状态和监控
  - `connection_handlers.go` (203行) - 连接管理
  - 其他专门处理器...
- **JavaScript模块化**: 现代JavaScript模块系统，提高可维护性
- **SQLite优化**: 数据库操作优化和性能提升
- **部署简化**: 单一二进制文件，零外部依赖
- **性能保证**: SQLite功能完全保持不变，性能优化

## [1.0.1] - 2025-09-08

### 🔧 修复 (Fixed)
- **Token解析兼容性**: 解决标准Claude API格式Token解析失败问题
- **格式兼容性**: 增强SSE事件格式兼容性，支持`event:`和`event: `两种格式
- **数据行解析**: 修复`data:`和`data: `格式兼容性问题  
- **Token提取增强**: 改进`parseMessageStart`函数，支持从`message_start`事件中提取token使用信息

### 📈 改进 (Improved)
- **调试日志**: 提供更详细的Token解析状态和错误信息
- **API兼容性**: 支持更多不同格式的Claude API端点
- **数据准确性**: Token使用统计更加准确和完整
- **nonocode端点兼容性**: 修复nonocode等使用标准Claude API格式的端点Token信息无法解析的问题

## [1.0.0] - 2025-09-05

### 🚀 新增 (Added)
- **Web管理界面**: 现代化Web界面，支持实时监控和组管理
- **请求ID追踪**: 完整的请求生命周期追踪系统，格式为`req-xxxxxxxx`
- **Token解析器**: Claude API SSE流中的模型信息和Token使用量提取
- **请求挂起系统**: 智能请求挂起和恢复机制
- **手动组切换**: 支持手动暂停/恢复/激活组操作
- **实时数据流**: Server-Sent Events (SSE) 实时更新
- **使用情况追踪**: SQLite数据库使用统计和成本分析

### 🏗️ 架构 (Architecture)
- **模块化设计**: 清晰的模块分离和组织
- **RESTful API**: 完整的REST API支持
- **实时监控**: 基于SSE的实时数据更新
- **数据持久化**: SQLite数据库集成

### 📊 监控功能 (Monitoring)
- **端点健康监控**: 实时端点状态追踪
- **请求统计**: 详细的请求和响应统计
- **成本分析**: Token使用成本计算和分析
- **性能指标**: 响应时间和成功率监控

## [0.x.x] - 基础版本

基于 [xinhai-ai/endpoint_forwarder](https://github.com/xinhai-ai/endpoint_forwarder) 的原始功能：

### 📦 基础功能 (Core Features)
- **透明代理**: 透明转发所有HTTP请求到后端端点
- **SSE流式支持**: 完整支持Server-Sent Events流式传输
- **Token管理**: 每个端点可配置独立的Bearer Token
- **路由策略**: 支持优先级路由和最快响应路由
- **健康检查**: 自动端点健康监控
- **重试与故障转移**: 指数退避重试和自动端点故障转移
