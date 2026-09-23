# Codex 账号池身份补全（第二批）

实现状态：2026-09-23，已接入桌面端、原账号池入口、模型路由和订阅进池入口。默认自动按实际目标处理，无需随接入模式调整；自动模式尚未进行新构建的真实 Wails / provider 验收。第一批基础协议修复位于提交 `46f2260`。

## 使用与回退

设置页的“Codex 身份”分类提供：

| 设置 | 默认值 | 行为 |
| --- | --- | --- |
| `codex_identity.mode` | `auto` | 官方 OAuth 启用描述补全和账号隔离；API Key / 第三方跳过身份映射，保留原身份正文与状态头 |
| `codex_identity.compat_version` | `0.155.1` | 无有效来源描述时使用的版本；覆盖版本不继承内置基线的验证结论 |

日常保留“自动”。“高级排障”提供 `disabled`（强制关闭）和 `enabled`（强制开启）；兼容版本也收起到高级设置。强制开启保留原有全量策略，第三方也会删除 `client_metadata` 和首方内部头；强制关闭停止身份层处理。两种覆盖都不改变订阅直通。

启动时只迁移一次旧 `codex_identity.enabled`：`true` → `auto`，`false` → `disabled`，保留明确关闭的回退意图。已有 `mode` 优先；先插入新键再删除旧键，重复初始化不会覆盖新设置。新安装、分类重置默认自动。旧开关键不再接受通用设置写入。

SQLite settings 是唯一可写事实来源。进程内缓存提供请求级策略快照，设置写入成功后失效并在下一请求发布新值；在途请求不随设置修改改变身份。通用设置服务整体校验批量输入后再写入，失败不报告生效；首次加载或缓存失效时读取失败明确返回 503，不悄悄回退默认值。

第一次加载自动或强制开启策略，使用原子 `InsertIfAbsent` 加读回建立随机 namespace，保存在受保护分类 `codex_identity_internal.namespace`。通用表单不展示、覆盖或删除它。关闭、分类重置和重新启动都保留 namespace；关闭不依赖它可读取。坏 namespace 不自动替换，只阻断需要稳定映射的首方请求，不阻断自动模式下的第三方请求。备份/恢复应包含整个 settings 表。

强制关闭及自动模式选中第三方时，停止合成描述、ID 映射、JSON 身份校验、隐私前后身份指纹检查和 turn-state owner 过滤。原账号池入口保留原身份正文及请求/响应状态头双向透传。订阅进池仍隔离来源头，自动选中第三方时仅从已校验 context 恢复 turn-state，不恢复来源凭据、Cookie、attestation 或 Connection 点名的头。订阅入口既有 JSON 边界校验、隐私过滤、模型改写、压缩等独立策略不由身份模式控制。固定基础 UA、Codex transport、解压和终态处理继续使用第一批实现。

## 入口与目标边界

| 用户接入方式 | 自动处理 |
| --- | --- |
| 订阅直通中的“代理订阅” | 使用客户端订阅凭据直通，完全绕过池身份策略 |
| 订阅直通中的“代理直通”（账号池） | POST responses 按选中账号判定：官方 OAuth 映射，API Key / 第三方保留 |
| 全局代理模式 | 按账号池实际选中的账号判定 |
| 模型路由模式 | 先按模型选账号，再按实际账号判定；同一逻辑请求换号时逐次重新判定 |

自动判断只收敛在共享出站管线，模式名称、请求模型和客户端 UA 都不用于推断上游是否首方。请求级配置快照不变；每次 attempt 的实际生效状态重新计算，并始终从原始正文开始，避免 OAuth 映射结果被带到第三方。

| 操作与目标 | 策略 |
| --- | --- |
| 旧入口 Responses / compact / search → 官方 OAuth | 账号作用域映射；按操作设置 Accept；Responses 才设置 routing-hint 和功能声明 |
| 旧入口 models 透传 → 官方 OAuth | 成组选择描述和 `client_version`，不生成线程字段，不依赖 namespace；本地目录模式不出站 |
| 订阅 pool 模式 POST responses | 清理动态 hop-by-hop 后提取有界来源 context，再进入同一账号构造器 |
| 订阅直通及 pool 模式的其他操作 | 保留客户端凭据、身份和正文；不读取池身份策略。pool 的 WS 仍返回 426 |
| API Key / 任意凭据的第三方目标 | 自动模式保留 `client_metadata`、`prompt_cache_key` 和状态语义；仅高级“强制开启”执行原有第三方清理 |

首方要求 OAuth 凭据加 HTTPS 官方 `chatgpt.com` 目标。当前账号路由对 OAuth 始终选择官方地址，数据库中的 OAuth `BaseURL` 不改变该既有约定；目标策略本身另覆盖 OAuth 第三方 origin。启用映射时拒绝跨 origin 重定向，防止已按一个目标构造的正文泄漏到另一个目标；同源跳转和共享 transport 保持原行为。

目标凭据由 `accountauth.ApplyAccountAuth` 解析一次，同一个最终账号 ID 同时用于 scope 和请求头，不再由旧 helper 二次覆盖。scope 包含账号记录 ID、provider、目标 origin 和解析出的真实 ChatGPT 账号 ID，不含 access/refresh token。

## 关联字段与正文

规则依据固定 `rust-v0.155.1` 源码 `responses_metadata.rs`、`requests/headers.rs`、`prompt_cache_key` 测试，以及本地 P0 记录 `docs/plans/2026-09-22-codex-fingerprint-p0-results.md`。这是字段格式证据，不代表当前实现已获得目标服务的真实验收。

只编辑以下明确位置，不全文替换 UUID，也不改服务端 response/item ID：

| 位置 | 编辑规则 |
| --- | --- |
| 顶层 `prompt_cache_key` | OAuth 按同一 ID 函数映射；`<source>:<parent>` 保留前缀并映射父线程。第三方保留 |
| `client_metadata` 直属 session/thread/request/parent/turn/root-turn/context-window/forked-thread ID | 相同原值使用同一映射函数，CLI 等值关系和 App 不等值关系均保留 |
| `window_id` / `x-codex-window-id` | `<thread>:<generation>` 保留 generation；其他形态单独映射 |
| `client_metadata` 内的 `installation_id` / `x-codex-installation-id` | 仅从本地 namespace 和目标 scope 派生；HTTP 不补发 installation-id 头 |
| `client_metadata.x-codex-turn-metadata` | 解码一层 JSON 字符串，编辑同一组已知直属路径；保留工作区、工具信息等非身份字段；不接受递归嵌套 |
| `x-codex-turn-metadata` 头 | 同一关联规则，允许正文中的工具清单与头不同；同一逻辑 ID 有冲突则明确 400 |
| `x-codex-turn-state` / attestation | 未知、其他 owner 或其他策略的状态移除；来源证明不进入池 |
| 其他 `x-codex-*` / `x-openai-*` / `x-oai-*` 头与 metadata 键 | 未识别即删除，只按名称限频告警（每名每小时一次）；无前缀的业务键保留。该命名空间是身份字段高发区，宁可新功能异常可见，也不让未映射的 ID 静默出站 |

缺失 session 头时先使用已验证正文中的 session 字段或 `prompt_cache_key`；仍缺失才生成每请求随机缺省值，重试复用该值。不同账号映射隔离；A → B → A、应用重启和 token 自动刷新后，同一 namespace / scope / 原 ID 的结果稳定。自动第三方路径不补 session-id。

局部 JSON 编辑共用一份校验与字节区间实现：拒绝重复键、多个文档和超过 256 层嵌套；未修改字段保留原始数字、转义、密文与用户文本。原模型改写和 input 内部 metadata 清理也使用它。映射后执行隐私过滤，必要关联字段、model 或 service_tier 被隐私规则改动时返回 422，不带着头/正文冲突出站。

### 大正文校验与复用

语法仍由标准库 `json.Valid` 校验；字节扫描器只检查重复键、深度和字段位置，不再使用 `Decoder.Token` 为每个用户值分配对象。转义键、无效 UTF-8 和未配对 surrogate 按标准库解码语义判断重复。公开的 `JSONFields` 仍返回独立字节副本；身份管线使用只读字段视图。

每个逻辑请求仅保留一份当前正文版本的校验快照，包含自有字节副本和直属字段位置。同内容的新切片及 A → B → A 重试复用它；原地修改、模型改写等导致正文内容变化时重新校验。不跨请求缓存，也不以切片地址或全局可变状态作为校验凭证。

身份映射复用已验证位置，只验证替换值（包含实际嵌入层级的深度和重复键），按最终长度一次分配输出。隐私前的身份指纹直接读取生成后的字段视图；隐私后的正文先比较 SHA-256 内容摘要，相同则跳过 JSON 重读，不同则完整校验并比较身份字段。相同内容的新切片可复用，原地修改也能发现；不信任过滤器的 `Changed` 声明。关闭和自动第三方路径仍跳过身份 JSON 校验。订阅入口校验、独立模型改写及内部 metadata 过滤仍各自执行其边界检查，不宣称整个代理只解析一次。

2026-09-23 本机基准：Apple M4 Pro、Go 1.25.6、合成约 3.7 MB 的大量小对象正文；相同 benchmark 在改前源码 overlay 和改后源码各运行三次，以下取中位数。覆盖账号身份准备与身份隐私校验，不含网络、模型生成或真实隐私规则扫描；不能直接替代原始用户样本的耗时。

| 场景 | 改前 | 改后 | 每请求累计分配（改前 → 改后） |
| --- | ---: | ---: | ---: |
| 官方 OAuth，一次尝试 | 332.5 ms | 18.1 ms | 189.8 → 11.3 MB |
| 官方 OAuth，A → B → A 三次尝试总计 | 985.0 ms | 22.8 ms | 569.4 → 18.8 MB |
| 隐私过滤返回相同内容的新切片 | 329.9 ms | 19.2 ms | 193.5 → 15.0 MB |
| 隐私过滤确实改写用户文本 | 338.7 ms | 37.1 ms | 193.5 → 18.9 MB |

分配量是 `B/op`，不是进程峰值内存。关闭与自动第三方路径没有新增 JSON 扫描。性能复现：

```sh
go test ./internal/proxy -run '^$' -bench '^BenchmarkAccountIdentityLargeJSON$' -benchmem -benchtime=500ms -count=3
go test ./internal/codexidentity -run '^$' -fuzz '^FuzzJSONValidationAndFieldSpans$' -fuzztime=20s -parallel=2
```

本轮验证：Go 全量回归、四个相关包 race、Go/前端构建通过；20 秒差分 fuzz 执行 328,801 次，未发现与旧校验语义或标准库字段提取不一致。回归另覆盖内容相同的新切片、缓存快照防原地污染、并发正文版本、隐私过滤原地改写和拒绝后不换号/不冷却。本轮未替换或重启正在运行的应用，优化须部署新构建后生效。

已知客户端描述成组选用 UA/version/originator。缺失或未知描述使用内置档案。描述识别只决定出站 UA，不参与功能权限判断：Responses 上的 beta 列表、subagent、lite、memgen、timing 声明只要格式合法就原样保留（beta 保留原始值与顺序），不论描述是否被识别。rust-v0.155.1 基线之外的 beta 值（基线：`remote_compaction_v2` 与 `/experimental` 可开的 `network_proxy`、`worktrees`、`realtime_conversation`）照常透传，只按名称限频告警，由上游决定是否支持；本地不猜账号权限，不删除声明后重试。只有无法按协议解释的取值（如布尔声明不是 `true`/`false`）返回 `codex_identity_unsupported_feature`（422）。布尔声明的 `false` 与缺省同义，不补发。HTTP 不补旧的 `responses=experimental`。没有 plan→feature 权限推断，free/plus/pro 的实际能力仍需真实样本验证。

取舍：允许未知功能声明透传，但未识别的首方扩展字段仍删除。新版本若引入依赖新字段的功能，可能表现为该功能异常并出现删除告警，需要补充映射规则；这是为避免新增身份字段未经映射出站而有意保留的限制。

## 响应与测试边界

turn-state 索引只保存 SHA-256 摘要、owner 和到期时间，最多 4096 条、TTL 30 分钟。owner 包含完整目标 scope、来源 session/thread 和关联策略。冲突归属失效，过期、淘汰或重启后未知状态不回送。没有入站 token 时不主动补旧状态。

普通响应、SSE、错误响应都在共享的 `client.Do` 返回边界完成头过滤与 owner 登记，早于任何下游 `WriteHeader` / `Flush`。状态索引不承载用量或结算业务。上游 Cookie 和动态 Connection 点名字段先清理，再登记状态。

自动化覆盖订阅直通、全局池、订阅进池、模型路由及订阅进池叠加模型路由；包括自动/强制开/强制关、OAuth 与 API Key 双向换号、策略快照、第三方状态双向透传、Connection 隔离、旧设置迁移/重启/重置、namespace 故障边界，以及既有首次响应写入、zstd 和原始正文保护。真实服务接受确定性 ID、缓存收益、跨 plan 功能及新 UI 操作尚未验证；TLS/HTTP2 指纹和新增池 WS 不在本批实现范围。

2026-09-23 自动模式验证：Go 全量测试、四个相关包的 race、Go 构建和前端构建均通过。未修改真实运行数据库，未切换或重启用户已启动的应用；新构建的 Wails 操作和真实 provider 验收仍待运行验证。

验证命令：

```sh
go test ./...
go test -race ./internal/codexidentity ./internal/service ./internal/proxy ./internal/accountauth
go build -o /tmp/codex-identity-auto-ai-switchboard .
cd frontend && npm run build
```

此前验证发现仓库旧脚本 `scripts/run_tests.sh --unit` 引用了不存在的 `tests/unit/config`（以及其他旧目录），不能作为当前完整矩阵；实际包回归由 `go test ./...` 覆盖，未修改无关脚本。
