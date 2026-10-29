# Sinuo Repository Guidelines

项目名为 **Sinuo**，Go module 为 `sinuo`；GitHub 仓库为 `xiaozhaodong/sinuo`，本地目录为 `~/GoProjects/sinuo`（原名 cc-forwarder-desktop）。

## 项目结构与模块组织
- **main.go**：初始化 CLI、加载配置并启动转发器。
- **internal/**：核心业务模块，当前主要包含 endpoint（端点调度）、proxy（请求处理）、transport（HTTP 客户端）、logging、monitor、tracking、service、store、accountauth 等子包，新增功能请按现有分层扩展。
- **internal/appdir/**：数据目录解析的唯一实现，负责旧品牌目录向 Sinuo 的优先级迁移与迁移保护；目标目录已存在则跳过迁移。
- **internal/accountauth/**：ChatGPT OAuth / refresh token / id_token 解析与账号画像提取。
- **internal/modelrewrite/**：Codex 账号池与 Claude 端点共用的模型改写规则解析、校验与匹配引擎。
- **internal/service/account_pool*.go**：账号池业务逻辑，包含单账号测试、启动批量连通性检查、分组编排、最近一次调度快照、画像刷新、quota 拉取与状态回写。
- **internal/store/account_pool.go**：`upstream_accounts` 存储层；账号画像、quota、`group_key`、账号成本倍率、`model_rewrite_rules` 字段变更需同步这里与 schema。
- **internal/store/endpoint.go**：Claude 端点存储层；`model_rewrite_rules` 与端点 CRUD、schema、Wails 数据映射需保持一致。
- **app_startup_connectivity.go**：桌面端启动后异步触发端点与账号池批量连通性检查。
- **config/**：示例与运行配置，`config/example.yaml` 为基础模板，生产部署请复制为 `config/config.yaml`。
- **data/** 与 `docs/`：分别存储运行时数据库、设计文档；保证数据目录可写。
- **frontend/src/pages/account-pool/**：Codex 账号池前端工作台，包含账号 inventory、筛选搜索、批量移动、调度编排卡片/抽屉、最近一次调度快照，以及 `api_key` 账号成本倍率和模型兼容配置/展示、标准 Responses 兼容开关。
- **frontend/src/pages/endpoints/**：Claude 端点管理，包含端点级模型兼容规则的显式配置与列表摘要。
- **frontend/src/pages/requests/**：请求页与 Codex 账号切换器；切换具体账号会固定当前请求目标，切回 Auto 才恢复按编排调度。
- **tests/**：单元与集成测试分层，`tests/unit/{module}` 与 `tests/integration/request_suspend` 对应主要质量保障。

## 构建、测试与开发命令
- `go build -o Sinuo`：构建本地二进制，默认启用当前平台。
- `go run . -config config/config.yaml`：快速本地验证，支持 `--no-tui`。
- `wails3 dev -config ./build/config.yml -port 9245`：桌面端联调主入口。
- `cd frontend && npm run build`：验证前端与 Wails 绑定改动是否可打包。
- `./scripts/build.sh vX.Y.Z`：多平台交叉编译并打包 dist，生成校验和。
- `./scripts/run_tests.sh` 或 `--unit` / `--integration`：执行测试矩阵并输出 `test_reports`。
- `docker-compose up -d`：启动示例依赖，便于联调 Web 界面与数据库。

## 编码风格与命名规范
- 所有 Go 代码提交前必须执行 `go fmt ./...` 与 `goimports -w`，保持标准格式。
- 导出函数、结构体使用 PascalCase，包内私有标识符使用 camelCase；常量采用 UPPER_SNAKE_CASE。
- 配置、YAML 键保持 snake_case，HTTP 路由遵循 kebab-case，例如 `/api/v1/request-suspend`。
- 注释与文档统一使用中文，必要时补充英文术语缩写以便检索。

## 测试指南
- 单元测试位于 `tests/unit`，命名遵循 `Test{模块}_{功能}_{场景}`；集成测试集中在 `tests/integration/request_suspend`。
- 推荐使用 `./scripts/run_tests.sh --coverage` 生成覆盖率报告，并提交关键变更的覆盖率摘要。
- 并发或边界场景请附加 `go test -race ./...` 执行记录，避免引入数据竞争。
- 新增测试数据统一放置于 `tests/testdata`，避免污染生产配置。
- 如果修改账号池 / OAuth / quota / 调度编排，至少补跑：
  - `go test .`
  - `go test ./internal/store`
  - `go test ./internal/accountauth`
  - `go test ./internal/service`
  - `go test ./internal/proxy`
  - `go test ./internal/proxy/handlers`
  - `go test ./internal/tracking`
  - `cd frontend && npm run build`

## 提交与合并请求规范
- Git 历史沿用 Conventional Commits，使用 `feat|fix|docs|chore` 等前缀，中文描述具体变化，可在末尾附版本标签例如 `v3.2.1`。
- 每个 PR 需列出变更摘要、测试结论、相关 Issue/需求链接；Web 或 CLI 交互变更请附截图或录屏。
- 在评审前确保 `go build` 与 `./scripts/run_tests.sh --unit` 全部通过，并更新 `CHANGELOG.md` 或文档（若影响用户）。
- 大型重构建议拆分多次提交，并在描述中标注潜在风险与回滚策略。

## 配置与安全提示
- 生产环境建议基于 `config/example.yaml` 创建独立配置，敏感 Token 通过环境变量或外部密钥管理注入，避免直接提交。
- 确保运行时 `usage.db` 所在目录具备备份策略，重要操作前执行离线快照；桌面默认路径遵循 `utils.GetDataDir()`（macOS 通常为 `~/Library/Application Support/Sinuo/data/usage.db`）。
- Web 管理界面默认监听 `0.0.0.0:8010`，对外暴露时请置于受控网络或启用反向代理身份验证。
- `credential_raw` 当前仍可能包含 `refresh_token / access_token / id_token`，排查问题时避免直接输出到日志、截图或文档。

## 账号池与 OAuth 约定
- `upstream_accounts` 现已包含账号画像字段：`plan_type`、`chatgpt_account_id`、`chatgpt_user_id`、`organization_id`。
- `upstream_accounts` 现已包含 quota 字段：`quota_5h_*`、`quota_weekly_*`、`quota_status`、`quota_refreshed_at`。
- `upstream_accounts` 现已包含 6 个账号成本倍率字段：`cost_multiplier`、`input_cost_multiplier`、`output_cost_multiplier`、`cache_creation_cost_multiplier`、`cache_creation_cost_multiplier_1h`、`cache_read_cost_multiplier`。
- `upstream_accounts` 现已包含 `model_rewrite_rules`，用于 `api_key` 账号的 Codex 模型兼容改写；不按渠道自动生成规则，所有改写规则都由前端显式启用并维护，当前使用精确匹配。
- `endpoints` 现已包含 `model_rewrite_rules`，用于 Claude 端点的模型兼容改写；不按渠道自动生成规则，前端新配置只生成精确匹配，固定作用于 `/v1/messages` 与 `/v1/messages/count_tokens`。
- `upstream_accounts` 现已包含 `group_key`；显式分组为 `primary / backup / cold`，若为空则按优先级推断：`<=10 => primary`、`<=20 => backup`、其余 => `cold`。
- `free` 账号只有周额度；前端使用 `d7` 标签显示。
- `api_key` 账号在前端默认显示 `5h / d7 = 无限额`。
- 仅 `provider_type == api_key` 允许自定义成本倍率；其他账号类型倍率固定为 `1.0`。
- `TestUpstreamAccount` 成功时应写回 `last_success_at`；OAuth 账号成功后会自动触发画像刷新。
- 桌面端启动完成后会异步执行端点与账号池批量连通性检查；账号池仅检测“已启用且有凭据”的账号，失败结果只进摘要与日志，不应回写持久化失败状态或改写编排运行态。
- 账号池现支持组内首选账号、整组交换、全局固定账号与最近一次调度快照；请求页切换具体账号等价于“固定账号”，不会修改账号分组或优先级。
- 清除全局固定账号后才会恢复按编排自动调度；组内首选账号仅在对应组被命中时优先生效。
- `503 no_available_providers` 在账号池测试链路中视为 reachable，在账号池转发链路中应短路透传，不做整池 cooldown。
- Codex `/v1/responses` 与 `/v1/responses/compact` 均走账号池链路；若账号池未启用或服务未就绪，应返回对应的 account-pool not ready 错误而不是回退到 endpoint。
- `/v1/responses` 的成本计算口径中，`input_tokens` 已包含缓存读；实际输入计费需按 `input_tokens - cache_read_tokens`，避免对 `cache_read_tokens` 重复按输入价计费。
- Claude `/v1/messages` 与 Codex `/v1/responses` 的流式链路均已引入 tail drain；若 terminal event 已确认到达，后续尾部 `context canceled` 不应再落为 `cancelled`。
