# Codex 订阅 WebSocket 兼容核查

核查日期：2026-11-04。范围为 `/v1/responses` 的订阅直通桥接；Realtime 语音使用独立协议入口。

## 证据与根因

- 运行中 Sinuo v6.4.1 / `59036b6` 在 13:22:30 的拒绝日志明确记录 `event_type=response.interrupt`。这不是上游模型拒绝，而是本地控制事件白名单遗漏。
- 本机 Desktop 内置 Codex `0.166.0-alpha.5.1` 二进制包含 `response.interrupt`、`response_id`、`discard_partial_items` 和中断回执处理标识。
- 官方源码快照 `d84c872464aca4821dc5cb548a651651aefd84fa` 的 [Responses 请求枚举](https://github.com/openai/codex/blob/d84c872464aca4821dc5cb548a651651aefd84fa/codex-rs/codex-api/src/common.rs#L386) 只有 `response.create`；[WS 传输实现](https://github.com/openai/codex/blob/d84c872464aca4821dc5cb548a651651aefd84fa/codex-rs/codex-api/src/endpoint/responses_websocket.rs#L801) 另外发送 `response.interrupt`。主线源码与本机二进制交叉核对，不将主线快照声称为本机的精确构建提交。
- 官方 [追加输入测试](https://github.com/openai/codex/blob/d84c872464aca4821dc5cb548a651651aefd84fa/codex-rs/core/tests/suite/pending_input.rs#L1589) 与 [子代理邮箱抢占测试](https://github.com/openai/codex/blob/d84c872464aca4821dc5cb548a651651aefd84fa/codex-rs/core/tests/suite/scenarios_mailbox_preemption_tests.rs#L204) 使用“中断、收尾、再次 create”序列。v6.4.1 仅依据 [公开 WebSocket 文档](https://developers.openai.com/api/reference/resources/responses/websocket-events) 补充 `response.steer`，没有覆盖实际客户端序列。

## 协议处理矩阵

| 事件/边界 | 桥接处理 | 追踪与排空 |
| --- | --- | --- |
| `response.create` | 每帧 JSON/隐私检查，保留原字段与增量上下文 | 唯一客户端生成登记入口；排空拒绝新工作 |
| `response.create(generate=false)` | 预热仍使用 create | 不计生成用量；排空拒绝新预热 |
| `response.interrupt` | 保留 `response_id`、`mode=discard_partial_items`；目标与参数由上游校验 | 不创建生成、不提前结算；排空仍允许收尾 |
| `response.steer` | 保留公开协议支持；对新增 input 进行隐私处理 | 不单独登记生成；可能引发续答，排空拒绝 |
| `response.interrupt.accepted/failed` | 原样回传；不伪造成功、不重排回执 | 不计首响/生成字节，不作为终态 |
| `response.steer.accepted/pending/failed` | 原样回传 | 不作为生成输出或终态 |
| `response.output_item.interrupted` | 原样回传，客户端负责丢弃未完成项 | 只结束一个输出项，不结束整个 response |
| `response.incomplete(reason=interrupted)` | 原样回传；SSE 与 WS 共用解析 | 取消状态，保留 usage 和结束原因；写出尾帧后释放在途计数 |
| `response.incomplete(reason=steered)` | 沿用公开协议续答语义 | 原有自动 successor 单独追踪 |
| `response.completed/done` | 原样回传 | 按响应 ID 一次结算，保留用量 |
| `response.failed`、其他 incomplete、`error` | 原样回传真实失败 | 不因增加中断支持而吞掉真实故障 |
| `response.cancelled/canceled` | 沿用现有服务端终态支持 | 取消结算 |
| 未识别客户端事件 | 明确本地拒绝，日志仅记录有界事件标识 | 不按任意 `response.*` 放行；目前未发现客户端发送 `response.cancel/append` |
| Ping/Pong、分片、压缩、Close | 继续由底层 WS 桥与库处理 | 不进入 JSON 业务事件名单 |

所有控制事件仍经过隐私与歧义 JSON 边界，并占一个空出站回调位置，避免后续 create 的字节错位。WS 仍是一对一上游连接，不重新路由中断目标。

握手核查同时覆盖 `x-openai-internal-codex-responses-lite`、`x-openai-subagent`、`OpenAI-Beta`、`x-codex-turn-state`；已有白名单可正确透传，无需增加另一套头部处理。

## 可重复验证

```sh
go test ./internal/proxy -run 'TestSubscriptionWS(Interrupt|Steer)' -count=1
go test ./internal/proxy/subscriptionws
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

- 修复前，新增测试复现中断被拒、控制回执计入生成字节、主动中断被判成 API 故障；修复后三项均通过。
- 两跳真实 WS 测试覆盖：回执先到、终态先到、无回执、失败回执、终态后迟到中断、真实上游故障、排空中断；每组都验证预热不记生成、尾部用量不丢、后续 create 复用连接，以及工具结果/用户追加输入/模拟子代理消息保持原样。
- 单测覆盖控制帧隐私失败零出站、重复键拒绝、混合控制帧出站计数、多响应隔离、单项中断非终态、重复终态与迟到回执不重复结算、尾帧写出前不释放在途计数。
- 本次执行结果：`go test ./...` 与 `go test -race ./...` 均通过 30 个包，`go vet ./...` 和 `go build ./...` 均通过；7 组真实 WS 桥接场景全部通过。
- 旧 `scripts/run_tests.sh --unit` 引用的 `tests/unit/config` 已不存在，脚本无法作为验收入口；上述全量 Go 命令覆盖仓库当前实际包，不在本补丁中修改无关测试脚本。
- 以上是本地桥接和按官方协议构造的模拟上游回归，不冒充真实账号的端到端验收。安装包含本补丁的构建后，还需在实际 Desktop 中重做运行中追加指令、停止生成及子代理消息到达场景，检查对应时间的新日志。
