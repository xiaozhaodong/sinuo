# AI Switchboard

<p align="center">
  <img src="build/appicon.png" width="128" height="128" alt="AI Switchboard">
</p>

<p align="center">
  <strong>Claude API 智能转发代理</strong><br>
  多端点负载均衡 · 自动故障转移 · 实时使用统计
</p>

<p align="center">
  <img src="https://img.shields.io/badge/version-5.3.0-blue.svg" alt="Version">
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Windows%20%7C%20Linux-lightgrey.svg" alt="Platform">
  <img src="https://img.shields.io/badge/license-MIT-green.svg" alt="License">
  <img src="https://img.shields.io/badge/Go-1.21+-00ADD8.svg" alt="Go">
  <img src="https://img.shields.io/badge/React-18-61DAFB.svg" alt="React">
</p>

---

## 概述

AI Switchboard 是一款基于 [Wails](https://wails.io) 构建的跨平台桌面应用。它作为本地代理运行，统一转发 Claude 与 OpenAI / Codex 请求，提供多端点智能路由、账号池调度、自动故障恢复等能力，同时记录完整的使用统计和成本数据。

### 为什么需要它？

- **多账号/多端点管理** - 统一管理多个 API 端点与 ChatGPT Codex 账号池，无需频繁切换配置
- **高可用保障** - 主端点故障时自动切换到备用端点，确保服务不中断
- **成本透明** - 实时追踪 Token 用量和费用，支持不同端点设置成本倍率
- **请求可观测** - 完整的请求生命周期追踪，便于问题排查
- **出站隐私保护** - 请求体按规则扫描脱敏，敏感信息不出本机

![overview](images/overview.png)

## 功能特性

### 🚀 智能转发引擎

- **优先级路由** - 按优先级自动选择最优端点
- **故障转移** - 端点异常时自动切换，支持配置冷却时间
- **端点自愈** - 持续监测故障端点，恢复后自动重新启用
- **流式传输** - 完整支持 SSE 流式响应，零延迟透传

### 📊 使用统计

- **实时监控** - Token 用量、请求成功率、响应时间一目了然
- **成本追踪** - 自动计算费用，支持按端点设置成本倍率
- **历史记录** - 完整的请求日志，支持筛选和导出
- **可视化图表** - 请求趋势图，直观展示使用情况

### 🎛️ 端点管理

- **可视化配置** - 图形界面管理端点，无需编辑配置文件
- **扁平化管理** - 每个 Claude 端点都是独立记录，不依赖渠道、组或共享密钥
- **三态路由** - 支持自动调度、手动优选和手动固定端点
- **状态监控** - 实时显示端点健康状态和响应延迟
- **灵活配置** - 支持 Token/API Key、自定义 Header、超时、模型改写和成本倍率
- **调度快照** - 记录每次调度的候选端点、跳过原因与最终命中，异常时才前台警示

### 🔀 Codex 账号池

- **OAuth 授权** - ChatGPT 账号一键授权，自动刷新 Token 与账号画像
- **分组编排** - 主组 / 备组 / 冷备三层，支持组内首选与全局固定账号
- **额度可见** - 展示 5h / 周额度剩余与刷新时间，`api_key` 账号可配置成本倍率
- **健康管理** - 启动批量连通性检查、冷却与鉴权失效自动处理

### 🔒 出站隐私保护

- **三种模式** - 关闭 / 仅检测 / 脱敏转发，默认关闭
- **规则可维护** - 规则存 SQLite，保存时先编译后落库，原子热替换不重启
- **精确扫描** - 按 JSON 文本字段扫描（含工具调用结果），零命中时请求体逐字节不变
- **不留痕** - 只记录规则名、命中数与耗时，不记录命中原文

### 🔧 其他特性

- **请求生命周期追踪** - 从接收到完成的全程状态管理，详情页展示分段耗时
- **独立图像 API 代理** - OpenAI 兼容生图/改图转发，不进端点与账号池调度
- **暗黑模式** - 全站语义色 token，跟随系统或手动切换
- **热池架构** - 内存缓存 + 异步写入，高性能低延迟
- **本地存储** - SQLite 数据库，无需额外依赖
- **跨平台** - macOS、Windows、Linux 全平台支持

## 截图

| 概览 | 端点管理 |
|:---:|:---:|
| ![overview](images/overview.png) | ![endpoints](images/endpoints.png) |

| 请求追踪 | 设置 |
|:---:|:---:|
| ![requests](images/requests.png) | ![settings](images/settings.png) |

## 快速开始

### 方式一：下载安装包

从 [Releases](https://github.com/xiaozhaodong/cc-forwarder-desktop/releases) 页面下载：

| 平台 | 文件 |
|------|------|
| macOS (Intel) | `AI-Switchboard-darwin-amd64.zip` |
| macOS (Apple Silicon) | `AI-Switchboard-darwin-arm64.zip` |
| Windows | `AI-Switchboard-windows-amd64.zip` |
| Linux | `AI-Switchboard-linux-amd64.tar.gz` |

### 方式二：从源码构建

```bash
# 1. 安装 Wails v3 CLI
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.27

# 2. 克隆项目
git clone https://github.com/xiaozhaodong/cc-forwarder-desktop.git
cd cc-forwarder-desktop

# 3. 安装前端依赖
cd frontend && npm install && cd ..

# 4. 开发模式运行
wails3 dev -config ./build/config.yml -port 9245

# 5. 构建生产版本
wails3 task build
```

### 配置 Claude Code

启动应用后，在 Claude Code 中设置代理地址：

```bash
# 设置 API 代理地址（默认端口 8080）
claude config set --global apiBaseUrl http://127.0.0.1:8080
```

## 配置说明

### 端点配置

端点通过应用内「Claude 端点」页面进行管理，并保存到应用核心 SQLite 数据库。Runtime 不再从 YAML 加载端点。

| 配置项 | 说明 | 示例 |
|--------|------|------|
| 名称 | 唯一标识 | `claude-primary` |
| URL | API 端点地址 | `https://api.anthropic.com` |
| Token / API Key | 可单独或同时使用；已存储密钥不回显明文 | `sk-ant-xxx` |
| 优先级 | 数字越小优先级越高 | `1` |
| 硬启用 | 关闭后任何路由模式都不使用该端点 | `启用` |
| 自动调度 | 是否参与 Auto 路由和故障转移 | `启用` |
| 成本倍率 | 费用计算倍率 | `1.0` |

### 从旧版升级

首次启动新版时，应用会在启动代理前顺序执行端点扁平化与 UTC 时间规范化迁移。每项迁移都会创建独立的数据库/活动配置一致性备份和校验清单；迁移失败时应用仅显示只读恢复页，不启动代理或后台写入。详见 [UPGRADING.md](UPGRADING.md)。

### 全局配置

编辑 `config/config.yaml` 配置全局选项：

```yaml
# 时区设置（留空或写 "local" 时自动跟随系统时区）
timezone: "Asia/Shanghai"

# 日志配置
logging:
  level: "info"              # debug, info, warn, error
  file_enabled: true
  file_path: "logs/app.log"
  max_file_size: "50MB"
  max_files: 10

# 流式传输
streaming:
  heartbeat_interval: "30s"
  read_timeout: "10s"
  response_header_timeout: "90s"  # Claude API 可能需要较长等待

# 使用统计
usage_tracking:
  enabled: true
  database:
    type: "sqlite"
    path: "data/usage.db"
  hot_pool:
    enabled: true             # 内存热池，提升写入性能
    max_age: "30m"
    max_size: 10000

# Claude 端点只使用 SQLite
endpoints_storage:
  type: "sqlite"
```

`timezone` 是唯一活动时区：数据库时间点固定保存为 UTC，Wails API 返回 UTC，桌面前端按该 IANA 时区显示并计算“今天”等业务日期。留空或写 `local` 时自动跟随系统时区（探测失败回退 `Asia/Shanghai`）；切换系统时区后重启应用生效，历史日统计将按新时区重新分组展示。旧的 `usage_tracking.database.timezone` 已弃用；若仍保留，必须与顶层值语义相同，否则应用会拒绝启动或热重载。

## 技术架构

```
┌──────────────────────────────────────────────────────────────┐
│                         AI Switchboard                        │
├──────────────────────────────────────────────────────────────┤
│                                                               │
│  ┌─────────────────────┐      ┌─────────────────────────┐   │
│  │   Frontend (React)  │      │     Backend (Go)        │   │
│  │                     │      │                         │   │
│  │  ├─ 概览仪表板      │◄────►│  ├─ HTTP 代理服务       │   │
│  │  ├─ 端点管理        │ Wails│  ├─ 端点管理器          │   │
│  │  ├─ 账号池          │ IPC  │  ├─ 账号池调度器        │   │
│  │  ├─ 请求追踪        │      │  ├─ 请求生命周期管理    │   │
│  │  ├─ 隐私保护        │      │  ├─ 隐私规则引擎        │   │
│  │  ├─ 系统日志        │      │  ├─ 使用量追踪          │   │
│  │  └─ 设置页面        │      │  └─ 事件推送系统        │   │
│  └─────────────────────┘      └─────────────────────────┘   │
│                                          │                    │
│                                          ▼                    │
│                               ┌─────────────────────┐        │
│                               │       SQLite        │        │
│                               │   ├─ 端点配置       │        │
│                               │   ├─ 账号池         │        │
│                               │   ├─ 隐私规则       │        │
│                               │   ├─ 请求日志       │        │
│                               │   ├─ 使用统计       │        │
│                               │   └─ 模型定价       │        │
│                               └─────────────────────┘        │
└──────────────────────────────────────────────────────────────┘

请求处理流程：
┌────────┐    ┌────────┐    ┌────────┐    ┌────────┐    ┌────────┐
│ Client │───►│ Proxy  │───►│Endpoint│───►│ Claude │───►│Response│
│Request │    │ Server │    │ Select │    │  API   │    │ Stream │
└────────┘    └────────┘    └────────┘    └────────┘    └────────┘
                  │              │                           │
                  ▼              ▼                           ▼
             ┌────────┐    ┌────────┐                  ┌────────┐
             │Tracking│    │Failover│                  │ Token  │
             │ Record │    │ Logic  │                  │ Parse  │
             └────────┘    └────────┘                  └────────┘
```

### 核心模块

| 模块 | 路径 | 职责 |
|------|------|------|
| 代理引擎 | `internal/proxy/` | 请求转发、流式处理、错误恢复 |
| 端点管理 | `internal/endpoint/` | 端点调度、健康检查、故障转移 |
| 账号池 | `internal/service/` | 账号 CRUD、分组编排、调度与画像刷新 |
| 账号鉴权 | `internal/accountauth/` | ChatGPT OAuth 授权、Token 刷新、画像解析 |
| 隐私引擎 | `internal/privacy/` | 规则编译、JSON 扫描、offset-aware 替换 |
| 模型改写 | `internal/modelrewrite/` | 端点与账号共用的模型改写解析与匹配 |
| 使用追踪 | `internal/tracking/` | 热池缓存、数据库写入、统计查询 |
| 事件系统 | `internal/events/` | SSE 推送、状态同步 |
| 前端应用 | `frontend/` | React + Vite + TailwindCSS |

## 常见问题

<details>
<summary><b>Q: 代理启动后 Claude Code 连接失败？</b></summary>

1. 确认代理端口（默认 8080）没有被占用
2. 检查 Claude Code 的 `apiBaseUrl` 配置是否正确
3. 确认至少配置了一个可用端点

</details>

<details>
<summary><b>Q: 如何查看请求日志？</b></summary>

应用内「请求追踪」页面可以查看所有请求记录，支持按时间、状态、模型等筛选。详细日志文件位于 `logs/app.log`。

</details>

<details>
<summary><b>Q: 成本统计不准确？</b></summary>

本地统计的费用与服务端账单可能存在差异，这是正常现象：

**可能的原因：**
1. **网络中断** - 请求发送后网络断开，服务端已计费但本地未收到响应
2. **流式传输中断** - 流式响应传输过程中断开，服务端实际输出的 Token 数可能与本地接收到的不一致
3. **缓存计费差异** - Prompt Caching 的计费依赖服务端返回的 `cache_creation_input_tokens` 和 `cache_read_input_tokens`，网络问题可能导致统计遗漏
4. **定价配置** - 本地模型定价配置与实际 API 定价不一致

**建议：**
- 本地统计仅供参考，实际费用以服务商账单为准
- 如果使用第三方端点，记得在端点配置中设置正确的成本倍率
- 在「基础定价」页面确认模型定价与实际一致

</details>

<details>
<summary><b>Q: 支持其他 AI API 吗？</b></summary>

除 Claude API 外，当前还支持：

- **OpenAI / Codex** - `/v1/responses` 与 `/v1/responses/compact`，走独立的 Codex 账号池调度
- **图像生成** - OpenAI 兼容的 `/v1/images/generations` 与 `/v1/images/edits`，单独配置 URL / Key / 固定单价，不参与端点与账号池调度

兼容 Claude API 格式的第三方服务也可以直接作为端点使用。

</details>

<details>
<summary><b>Q: 日志和数据存储在哪里？</b></summary>

应用数据存储在系统用户目录下，不同平台路径如下：

| 平台 | 应用数据目录 |
|------|-------------|
| macOS | `~/Library/Application Support/AI-Switchboard/` |
| Windows | `%APPDATA%\AI-Switchboard\` |
| Linux | `~/.local/share/ai-switchboard/` |

**目录结构：**
```
AI-Switchboard/
├── data/
│   └── usage.db        # SQLite 数据库（端点配置、请求记录、使用统计）
├── logs/
│   └── app.log         # 应用日志（支持轮转）
└── config/
    └── config.yaml     # 配置文件（如果存在）
```

**说明：**
- 日志文件支持自动轮转，可在配置中设置大小限制和保留数量
- 数据库包含所有端点配置和历史请求记录，建议定期备份
- 卸载应用时这些数据不会自动删除，如需清理请手动删除对应目录

</details>

## 开发计划

- [ ] 多语言支持（English）
- [ ] 请求重放功能
- [ ] 更多统计维度
- [ ] 自动更新

## 贡献

欢迎提交 Issue 和 Pull Request！

## 许可证

[MIT License](LICENSE)

## 致谢

- 本项目最初受 [xinhai-ai/endpoint_forwarder](https://github.com/xinhai-ai/endpoint_forwarder) 启发
- 感谢 [Wails](https://wails.io) 提供优秀的桌面应用框架
- 感谢所有开源库的贡献者

---

<p align="center">
  <sub>Made with ❤️ and mass vibe coding</sub>
</p>
