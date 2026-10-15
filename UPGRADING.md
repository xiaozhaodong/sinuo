# Sinuo 升级说明

## 从 AI Switchboard 升级到 Sinuo（v6.0.0）

### 首次启动前

首次启动 Sinuo 前，必须先完全退出旧 App：点击旧 App 的菜单栏图标 →「退出」。两个 App 可以同时运行，因此需要手动确认旧 App 已完全退出，不能只关闭窗口。

### 数据目录与迁移保护

Sinuo 会在打开数据库和启动代理前自动整体迁移旧数据目录：优先将 `AI-Switchboard` 目录迁移到 `Sinuo`；该旧目录不存在时，再从更早的 `CC-Forwarder` 目录迁移。各平台使用对应的旧品牌目录名，配置、请求记录和日志都会随目录一起迁移。新目录路径见 [README.md](README.md#常见问题)。如果 `Sinuo` 数据目录已经存在，则沿用该目录，不合并或覆盖旧目录。

迁移保护只检查旧目录下 `data/usage.db-wal` 与 `data/usage.db-shm` 两个确切文件名。任一文件存在时，视为旧数据库可能仍在使用，应用会弹出原生错误框并退出；目录迁移失败时也会弹框并退出。这些情况下不会创建 Sinuo 数据目录、打开数据库或监听端口，旧数据原样保留。

如果旧 App 已退出但仍出现迁移保护提示，请打开一次旧 App，再通过菜单栏图标 →「退出」正常退出，然后重新启动 Sinuo。

这项数据目录迁移与下文 v5.3.0 的数据库结构迁移是不同阶段：数据目录迁移失败会弹框退出，数据库结构迁移失败则进入只读恢复页。

### 一次性界面与系统设置变化

bundle id 已改为 `app.sinuo.desktop`：主题、请求页分页 / 筛选栏、账号池视图等界面偏好会重置一次；系统通知需要重新授权，Dock 中的应用需要重新固定。这些变化不会删除数据库中的配置或请求记录。

### 客户端与外部工具

端口保持不变，Claude Code / Codex 客户端配置无需修改。数据库客户端、备份脚本等外部工具如果写死了旧数据库路径，需要改到 Sinuo 数据目录下的 `data/usage.db`；例如 macOS 的新路径为 `~/Library/Application Support/Sinuo/data/usage.db`。

## v5.3.0：Claude 端点扁平化与 UTC 时间规范化版本

本版本将 Claude 端点改为 SQLite 中的扁平独立记录，并把数据库中的真实时间点统一规范化为固定微秒精度 UTC。升级前请先保留当前安装包或可执行文件，不要手工删除应用生成的迁移备份。

### 升级时会发生什么

1. 应用在代理、健康检查和后台写入启动前检查数据库与配置格式。
2. 如果需要迁移，每个 migration ID 都先用 SQLite 一致性快照备份数据库，并备份完整活动配置。UTC 迁移启动前要求同文件系统可用空间不少于活动数据库大小的 3 倍。
3. 备份目录权限为 `0700`，数据库、配置和 manifest 权限为 `0600`。Manifest 记录文件大小、SHA-256 和 SQLite `integrity_check` 结果。
4. 迁移器根据升级前配置声明的事实源执行转换：
   - `endpoints_storage.type: yaml` 或空值：解析 YAML 端点、组继承和多 Token/API Key，再替换 SQLite 端点。
   - `endpoints_storage.type: sqlite`：仅以旧 SQLite `endpoints` 表为事实源，忽略活动配置中残留的 YAML endpoints。
5. 多 Token 和多 API Key 会按笛卡尔积拆成独立端点；派生端点默认关闭，避免升级后意外扩大流量。
6. 请求历史保留记录数、Token 与成本，来源维度改为 `request_family` 和 `upstream_*`。
7. 成功后活动配置固定为 `endpoints_storage.type: sqlite`，并删除 YAML `endpoints` 和 legacy `group`。
8. UTC 迁移按字段历史语义解析已有时间并重建相关表；`usage_summary` 作为缓存会清空，由 Tracker 启动后按活动时区重建最近 7 个业务日。查询超出缓存窗口时会整段从请求明细实时聚合。
9. 顶层 `timezone` 成为唯一展示和业务日期时区；留空或写 `local` 时自动跟随系统时区（探测失败回退 `Asia/Shanghai`）。旧 `usage_tracking.database.timezone` 仅在与顶层语义相同的情况下兼容接受。迁移解释无 offset 历史时间时：留空或旧版无法加载的写法（如小写 `local`）按旧版实际回退口径 `Asia/Shanghai` 钉死解释；精确的旧值 `Local` 按探测出的系统时区解释，探测失败会中止迁移并要求显式配置。
10. UTC 迁移完成后的日常启动只执行 schema 快检，不再每次扫描全部请求历史；若需要核查历史值，应在离线副本上运行完整迁移测试或专项诊断。

备份默认位于应用数据目录的：

```text
migration-backups/20260803-claude-endpoint-flatten-<timestamp>/
├── usage.db
├── config.yaml
└── manifest.json
```

UTC 时间迁移使用独立目录：

```text
migration-backups/20260804-timezone-utc-<timestamp>/
├── usage.db
├── config.yaml
└── manifest.json
```

该 manifest 会固定记录解释历史无 offset 时间所使用的旧 tracking 时区，确保失败重试不会因后来修改配置而改变结果。

### 迁移失败

迁移失败后，应用进入只读恢复页，不启动代理、批量连通性检查、调度器或请求记录写入。恢复页会显示经脱敏的错误、迁移阶段、数据库/配置路径和备份目录。

先处理文件权限、磁盘空间、配置 YAML 语法、非法历史时间或 SQLite 完整性问题，然后在恢复页重试。迁移帐本按 migration ID 记录阶段；重试不会重复拆分端点或重复转换已完成的 UTC 数据。

### 升级后检查

- 「Claude 端点」页能看到扁平端点表，并能执行单点/批量健康检查。
- 端点 priority 按旧版运行时口径原样保留，仅在端点未设置 priority 时用 v3.x `group-priority` 补位。若旧配置各组内 priority 曾从 1 重新编号，请核对全局优先级：相同数字会并列为同一调度层，层内按名称序尝试并带成功粘性。
- 请求页使用「类型」和「上游」筛选，不再显示 channel/group。
- 旧多凭据拆分生成的派生端点默认处于硬关闭状态；核对后再手动启用。
- 活动配置中只保留 `endpoints_storage.type: sqlite`，不再存在 YAML `endpoints` 或 `group`。
- 请求、账号、端点和日志时间按顶层 `timezone` 显示；修改时区后无需重写历史请求。
- 请求页“今天/7 天/30 天”使用配置日历边界，DST 切换日不会按固定 24 小时计算。
- 保留 `migration-backups` 目录；这是物理降级所需的唯一完整数据快照。

### 离线回滚到旧版本

本版本已物理删除旧列和旧 API，不支持仅替换旧可执行文件的原地降级。回滚必须在应用完全退出后进行：

1. 完全退出 AI Switchboard，确认没有代理或后台进程读写数据库。
2. 将当前新版数据库和配置另行备份，不覆盖迁移前备份。
3. 在 `migration-backups/<timestamp>/manifest.json` 中核对原路径、文件大小和 SHA-256。
4. 将同一个备份目录中的 `usage.db` 和 `config.yaml` 分别恢复到 manifest 记录的原路径。
5. 检查恢复后文件权限为 `0600`，并对 SQLite 运行 `PRAGMA integrity_check`。
6. 启动与该备份 schema/配置兼容的旧版本。

新版本读取迁移前备份时会再次执行迁移，因此「恢复旧文件」和「启动旧应用版本」必须成对完成。
