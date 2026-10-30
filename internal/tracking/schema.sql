-- Usage Tracking Database Schema
-- 使用跟踪系统数据库结构
-- 创建时间: 2025-09-04

-- 时间列约定：所有 DATETIME 列实际存储固定微秒精度 UTC 文本
-- （strftime('%Y-%m-%dT%H:%M:%f000Z')，对应 Go 侧 timezone.StorageLayout）。
-- 读取必须写 CAST(col AS TEXT)：SQLite 驱动会把 DATETIME 声明列的裸查询
-- 自动解析成 time.Time，internal/timezone.DBTime 拒收该类型并直接报错。

-- 请求记录主表：统一使用 request_family + upstream 维度。
CREATE TABLE IF NOT EXISTS request_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT UNIQUE NOT NULL,        -- req-xxxxxxxx
    
    -- 请求基本信息
    client_ip TEXT,                         -- 客户端IP
    user_agent TEXT,                        -- 客户端User-Agent
    method TEXT DEFAULT 'POST',             -- HTTP方法
    path TEXT DEFAULT '/v1/messages',       -- 请求路径
    
    -- 时间信息
    start_time DATETIME NOT NULL,           -- 请求开始时间
    end_time DATETIME,                      -- 请求完成时间
    duration_ms INTEGER,                    -- 总耗时(毫秒)
    first_token_ms INTEGER,                 -- 上游请求写完到首个有效流式响应耗时(毫秒，仅流式请求)
    completion_ms INTEGER,                  -- 首个有效流式响应到流式完成耗时(毫秒，仅流式请求)
    first_content_ms INTEGER,               -- 首个正文、思考或工具内容相对 start_time 偏移；只写一次
    first_text_ms INTEGER,                  -- 首个正文偏移；只写一次
    first_reasoning_ms INTEGER,             -- 首个可见思考偏移；只写一次
    reasoning_hidden INTEGER,               -- 观测到不可见思考为1，否则为0；未观测为NULL，只写一次
    stream_flow_ms INTEGER,                 -- data内容字节中间80%到达跨度乘10/8；超限或未知为NULL
    stop_reason TEXT,                       -- 上游结束原因原文，最终尝试覆盖写
    upstream_request_id TEXT,               -- 最终尝试响应头请求ID，覆盖写
    upstream_write_ms INTEGER,              -- 最终尝试完整写出相对 start_time 偏移；首响前覆盖，首响后冻结
    upstream_first_dispatch_ms INTEGER,     -- 本次请求第一次联系上游（取连接）的偏移，本地处理段终点；只写一次
    upstream_dispatch_ms INTEGER,           -- 最终尝试开始取连接的偏移（重试段终点）；HTTP 链路记录
    upstream_conn_ms INTEGER,               -- 最终尝试取得可用连接的偏移；HTTP 链路记录
    schedule_snapshot_json TEXT,            -- v6.2+: 账号池/端点调度快照（白名单 DTO JSON，终态覆盖写）
    privacy_scan_json TEXT,                 -- v6.2: 隐私扫描摘要 JSON（只有规则名/计数，绝无命中原文）
    request_source TEXT NOT NULL DEFAULT 'legacy_proxy'
        CHECK (request_source IN ('legacy_proxy', 'codex_subscription_proxy')),
    request_kind TEXT NOT NULL DEFAULT 'legacy'
        CHECK (request_kind IN ('legacy', 'generation', 'compaction')),
    
    -- 转发信息
    request_family TEXT NOT NULL DEFAULT 'other'
        CHECK (request_family IN ('claude', 'codex', 'image', 'other')),
    endpoint_name TEXT,                     -- Claude 生命周期兼容字段
    model_name TEXT,                        -- 计费模型：有响应模型用响应模型，否则回退请求模型
    request_model TEXT DEFAULT '',          -- v6.3: 客户端原始请求模型（随 start 事件写入）
    upstream_model TEXT DEFAULT '',         -- v6.3: 最终 attempt 实际发给上游的模型；空 = 与 request_model 相同
    response_model TEXT DEFAULT '',         -- v6.3: 仅由响应解析写入的模型；无响应模型时恒空
    request_reasoning TEXT DEFAULT '',      -- v6.3: 客户端请求的思考配置（归一化 token 串）
    response_reasoning TEXT DEFAULT '',     -- v6.3: 上游响应可观测的思考信息（归一化 token 串）
    session_id TEXT DEFAULT '',             -- 客户端原始会话，只在开始记录时写入（索引在迁移后创建）
    upstream_type TEXT NOT NULL DEFAULT '', -- 内部来源形态: endpoint/account
    upstream_source_name TEXT NOT NULL DEFAULT '',
    upstream_name TEXT NOT NULL DEFAULT '', -- 用户可见的实际上游
    upstream_id INTEGER NOT NULL DEFAULT 0,
    is_streaming BOOLEAN DEFAULT FALSE,     -- 是否为流式请求

    -- Claude 端点路由诊断信息 (V1.1)
    route_mode TEXT DEFAULT 'auto',          -- auto/manual_preferred/manual_fixed
    requested_endpoint TEXT DEFAULT '',      -- 用户手动指定的端点
    effective_endpoint TEXT DEFAULT '',      -- 本次实际选中的端点
    fallback_reason TEXT DEFAULT '',         -- manual_preferred fallback 或 manual_fixed 阻塞原因
    route_decision_at DATETIME,              -- 路由决策时间
    
    -- 状态信息 (v3.5.0更新: 生命周期状态与错误原因分离 - 2025-09-28)
    status TEXT NOT NULL DEFAULT 'pending', -- 生命周期状态: pending/forwarding/processing/retry/suspended/completed/failed/cancelled
    http_status_code INTEGER,               -- HTTP状态码
    retry_count INTEGER DEFAULT 0,          -- 重试次数

    -- 失败信息 (状态机重构新增字段 v3.5.0 - 2025-09-28)
    failure_reason TEXT,                    -- 当前失败原因类型: rate_limited/server_error/network_error/timeout/empty_response/invalid_response
    last_failure_reason TEXT,               -- 最后一次失败的详细错误信息

    -- 取消信息 (状态机重构新增字段 v3.5.0 - 2025-09-28)
    cancel_reason TEXT,                     -- 取消原因 (取消时间使用end_time字段)
    
    -- Token统计
    input_tokens INTEGER DEFAULT 0,        -- 输入token数
    output_tokens INTEGER DEFAULT 0,       -- 输出token数
    cache_creation_tokens INTEGER DEFAULT 0, -- 缓存创建token数（总计，向后兼容）
    cache_creation_5m_tokens INTEGER DEFAULT 0, -- 5分钟缓存创建token数 (v5.0.1+)
    cache_creation_1h_tokens INTEGER DEFAULT 0, -- 1小时缓存创建token数 (v5.0.1+)
    cache_read_tokens INTEGER DEFAULT 0,   -- 缓存读取token数

    -- 成本计算（包含缓存）
    input_cost_usd REAL DEFAULT 0,         -- 输入token成本
    output_cost_usd REAL DEFAULT 0,        -- 输出token成本
    cache_creation_cost_usd REAL DEFAULT 0, -- 缓存创建成本（总计，向后兼容）
    cache_creation_5m_cost_usd REAL DEFAULT 0, -- 5分钟缓存创建成本 (v5.0.1+)
    cache_creation_1h_cost_usd REAL DEFAULT 0, -- 1小时缓存创建成本 (v5.0.1+)
    cache_read_cost_usd REAL DEFAULT 0,    -- 缓存读取成本
    total_cost_usd REAL DEFAULT 0,         -- 总成本
    
    -- 审计字段（统一使用带时区格式，微秒精度）
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
);

-- 热力图按天汇总缓存：只存配置时区「前天及更早」业务日的库内聚合（不含热池）。
-- 纯缓存，清空只能 DELETE：下面的触发器引用此表，DROP 会让 request_logs 写入报错。
CREATE TABLE IF NOT EXISTS usage_daily (
    timezone_name TEXT NOT NULL,
    date TEXT NOT NULL,                        -- 业务日 YYYY-MM-DD
    start_utc TEXT NOT NULL,                   -- FormatStorage 固定微秒 UTC 文本
    end_utc TEXT NOT NULL,                     -- 业务日 UTC 终点（不含）
    request_count INTEGER NOT NULL DEFAULT 0,  -- 生成口径次数
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    total_cost_usd REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (timezone_name, date)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_usage_daily_start ON usage_daily(start_utc);

-- MAX 定位依赖单时区不变式：封存事务提交前必须清除其他时区行。
CREATE TRIGGER IF NOT EXISTS usage_daily_invalidate_insert AFTER INSERT ON request_logs BEGIN
    DELETE FROM usage_daily
     WHERE start_utc = (SELECT MAX(start_utc) FROM usage_daily WHERE start_utc <= NEW.start_time)
       AND end_utc > NEW.start_time;
END;
CREATE TRIGGER IF NOT EXISTS usage_daily_invalidate_update
AFTER UPDATE OF start_time, request_kind, request_family, path,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, total_cost_usd ON request_logs BEGIN
    DELETE FROM usage_daily
     WHERE start_utc = (SELECT MAX(start_utc) FROM usage_daily WHERE start_utc <= OLD.start_time)
       AND end_utc > OLD.start_time;
    DELETE FROM usage_daily
     WHERE start_utc = (SELECT MAX(start_utc) FROM usage_daily WHERE start_utc <= NEW.start_time)
       AND end_utc > NEW.start_time;
END;
CREATE TRIGGER IF NOT EXISTS usage_daily_invalidate_delete AFTER DELETE ON request_logs BEGIN
    DELETE FROM usage_daily
     WHERE start_utc = (SELECT MAX(start_utc) FROM usage_daily WHERE start_utc <= OLD.start_time)
       AND end_utc > OLD.start_time;
END;

-- 索引优化
CREATE INDEX IF NOT EXISTS idx_request_logs_request_id ON request_logs(request_id);
CREATE INDEX IF NOT EXISTS idx_request_logs_start_time ON request_logs(start_time);
CREATE INDEX IF NOT EXISTS idx_request_logs_status ON request_logs(status);
CREATE INDEX IF NOT EXISTS idx_request_logs_model ON request_logs(model_name);
CREATE INDEX IF NOT EXISTS idx_request_logs_endpoint ON request_logs(endpoint_name);
CREATE INDEX IF NOT EXISTS idx_request_logs_failure_reason ON request_logs(failure_reason);
CREATE INDEX IF NOT EXISTS idx_request_logs_family_time ON request_logs(request_family, start_time);
CREATE INDEX IF NOT EXISTS idx_request_logs_family_upstream_time ON request_logs(request_family, upstream_name, start_time);
CREATE INDEX IF NOT EXISTS idx_request_logs_upstream_type_time ON request_logs(upstream_type, start_time);

-- 使用统计汇总表 (可选，用于快速查询)
CREATE TABLE IF NOT EXISTS usage_summary (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timezone_name TEXT NOT NULL,
    date TEXT NOT NULL,                    -- YYYY-MM-DD
    model_name TEXT NOT NULL,
    request_family TEXT NOT NULL
        CHECK (request_family IN ('claude', 'codex', 'image', 'other')),
    request_kind TEXT NOT NULL DEFAULT 'legacy'
        CHECK (request_kind IN ('legacy', 'generation', 'compaction')),
    upstream_type TEXT NOT NULL DEFAULT '',
    upstream_name TEXT NOT NULL DEFAULT '',
    upstream_id INTEGER NOT NULL DEFAULT 0,
    
    request_count INTEGER DEFAULT 0,       -- 请求总数
    success_count INTEGER DEFAULT 0,       -- 成功请求数
    error_count INTEGER DEFAULT 0,         -- 失败请求数
    
    total_input_tokens INTEGER DEFAULT 0,
    total_output_tokens INTEGER DEFAULT 0,
    total_cache_creation_tokens INTEGER DEFAULT 0,
    total_cache_read_tokens INTEGER DEFAULT 0,
    total_cost_usd REAL DEFAULT 0,
    
    avg_duration_ms REAL DEFAULT 0,        -- 平均响应时间
    
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    
    UNIQUE(timezone_name, date, model_name, request_family, request_kind, upstream_type, upstream_name, upstream_id)
);

-- 汇总表索引
CREATE INDEX IF NOT EXISTS idx_usage_summary_date ON usage_summary(date);
CREATE INDEX IF NOT EXISTS idx_usage_summary_timezone_date ON usage_summary(timezone_name, date);
CREATE INDEX IF NOT EXISTS idx_usage_summary_model ON usage_summary(model_name);
CREATE INDEX IF NOT EXISTS idx_usage_summary_family ON usage_summary(request_family);
-- idx_usage_summary_kind 在 migrateSchema 补齐旧表 request_kind 后创建。
CREATE INDEX IF NOT EXISTS idx_usage_summary_upstream ON usage_summary(upstream_type, upstream_name, upstream_id);

-- 触发器：自动更新 updated_at 时间戳（统一使用带时区格式，微秒精度）
CREATE TRIGGER IF NOT EXISTS update_request_logs_timestamp
    AFTER UPDATE ON request_logs
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE request_logs SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS update_usage_summary_timestamp
    AFTER UPDATE ON usage_summary
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE usage_summary SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;

-- ============================================================================
-- Claude 端点配置表：一条记录对应一个 URL 与一个完整认证组合。
-- ============================================================================
CREATE TABLE IF NOT EXISTS endpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,

    -- ========== 基本信息 ==========
    name TEXT UNIQUE NOT NULL CHECK (length(trim(name)) > 0), -- 端点唯一名称
    url TEXT NOT NULL CHECK (length(trim(url)) > 0),          -- 端点 URL

    -- ========== 认证配置 ==========
    token TEXT,                                     -- Bearer Token
    api_key TEXT,                                   -- API Key (备用)
    headers TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(headers) AND json_type(headers) = 'object'), -- 自定义请求头

    -- ========== 路由配置 ==========
    priority INTEGER NOT NULL DEFAULT 1 CHECK (priority >= 0), -- 优先级（数字越小越高）
    failover_enabled INTEGER NOT NULL DEFAULT 1
        CHECK (failover_enabled IN (0, 1)),          -- 是否参与故障转移 (1=是, 0=否)
    availability_enabled INTEGER NOT NULL DEFAULT 1
        CHECK (availability_enabled IN (0, 1)),      -- 硬启用：0=任何模式都不可使用
    cooldown_seconds INTEGER,                       -- 冷却时间（秒，NULL=使用全局配置）
    timeout_seconds INTEGER NOT NULL DEFAULT 300
        CHECK (timeout_seconds > 0),                 -- 请求超时（秒）

    -- ========== 功能支持 ==========
    supports_count_tokens INTEGER NOT NULL DEFAULT 0
        CHECK (supports_count_tokens IN (0, 1)),     -- 是否支持 count_tokens 端点
    model_rewrite_rules TEXT NOT NULL DEFAULT '',   -- CC 端点模型兼容改写规则 JSON
    soft_failure_window_minutes INTEGER,           -- NULL 表示继承全局软失败窗口
    soft_failure_threshold INTEGER,                -- NULL 表示继承全局软失败阈值

    -- ========== 成本倍率 ==========
    cost_multiplier REAL NOT NULL DEFAULT 1.0
        CHECK (cost_multiplier > 0),                 -- 总成本倍率
    input_cost_multiplier REAL NOT NULL DEFAULT 1.0
        CHECK (input_cost_multiplier > 0),           -- 输入成本倍率
    output_cost_multiplier REAL NOT NULL DEFAULT 1.0
        CHECK (output_cost_multiplier > 0),          -- 输出成本倍率
    cache_creation_cost_multiplier REAL NOT NULL DEFAULT 1.0
        CHECK (cache_creation_cost_multiplier > 0),  -- 5分钟缓存创建成本倍率（默认）
    cache_creation_cost_multiplier_1h REAL NOT NULL DEFAULT 1.0
        CHECK (cache_creation_cost_multiplier_1h > 0), -- 1小时缓存创建成本倍率
    cache_read_cost_multiplier REAL NOT NULL DEFAULT 1.0
        CHECK (cache_read_cost_multiplier > 0),      -- 缓存读取成本倍率

    -- ========== 审计字段 ==========
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
);

-- v8 端点运行态（收敛方案 §7.2）：按 scope 保存达到阈值后的持久化 cooldown。
-- scope=global：认证失败/可靠 quota，阻断两个 path；messages：生成请求 cooldown；
-- count_tokens：预留，第一版不写入。软失败事件列表不落库。
CREATE TABLE IF NOT EXISTS endpoint_runtime_states (
    endpoint_id INTEGER NOT NULL,
    scope TEXT NOT NULL CHECK (scope IN ('global', 'messages', 'count_tokens')),
    state TEXT NOT NULL DEFAULT 'active',
    cooldown_until DATETIME,
    cooldown_reason TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    PRIMARY KEY (endpoint_id, scope),
    FOREIGN KEY (endpoint_id) REFERENCES endpoints(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_endpoint_runtime_states_cooldown
ON endpoint_runtime_states(scope, cooldown_until);

-- 端点表索引
CREATE INDEX IF NOT EXISTS idx_endpoints_priority ON endpoints(priority);
CREATE INDEX IF NOT EXISTS idx_endpoints_failover ON endpoints(failover_enabled);
CREATE INDEX IF NOT EXISTS idx_endpoints_availability ON endpoints(availability_enabled);

-- 端点表触发器：自动更新 updated_at
CREATE TRIGGER IF NOT EXISTS update_endpoints_timestamp
    AFTER UPDATE ON endpoints
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE endpoints SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;

-- ============================================================================
-- 模型定价表 (v5.0.0 新增 - 2025-12-06, 更新于 2025-12-06 支持 5m/1h 缓存)
-- 将模型定价从 config.yaml 迁移到 SQLite，支持动态管理
-- ============================================================================
CREATE TABLE IF NOT EXISTS model_pricing (
    id INTEGER PRIMARY KEY AUTOINCREMENT,

    -- ========== 模型信息 ==========
    model_name TEXT UNIQUE NOT NULL,                -- 模型名称（如 claude-sonnet-4-20250514）

    -- ========== 定价信息 (USD per 1M tokens) ==========
    input_price REAL NOT NULL DEFAULT 3.0,          -- 输入价格
    output_price REAL NOT NULL DEFAULT 15.0,        -- 输出价格
    cache_creation_price_5m REAL DEFAULT 3.75,      -- 5分钟缓存创建价格 (通常为 input * 1.25)
    cache_creation_price_1h REAL DEFAULT 6.0,       -- 1小时缓存创建价格 (通常为 input * 2.0)
    cache_read_price REAL DEFAULT 0.30,             -- 缓存读取价格 (通常为 input * 0.1)

    -- ========== 模型元信息 ==========
    display_name TEXT,                              -- 显示名称（用于前端展示）
    description TEXT,                               -- 模型描述
    is_default INTEGER DEFAULT 0,                   -- 是否为默认定价 (1=是)

    -- ========== 审计字段 ==========
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
);

-- 模型定价表索引
CREATE INDEX IF NOT EXISTS idx_model_pricing_model_name ON model_pricing(model_name);
CREATE INDEX IF NOT EXISTS idx_model_pricing_is_default ON model_pricing(is_default);

-- 模型定价表触发器：自动更新 updated_at
CREATE TRIGGER IF NOT EXISTS update_model_pricing_timestamp
    AFTER UPDATE ON model_pricing
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE model_pricing SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;

-- ============================================================================
-- 系统设置表 (v5.1.0 新增 - 2025-12-08)
-- 将运行时可调配置从 config.yaml 迁移到 SQLite，支持动态管理和热更新
-- ============================================================================
CREATE TABLE IF NOT EXISTS settings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,

    -- ========== 设置标识 ==========
    category TEXT NOT NULL,                         -- 分类: strategy, retry, health, failover, request, streaming, auth, token_counting, retention, hot_pool, server
    key TEXT NOT NULL,                              -- 配置键

    -- ========== 设置值 ==========
    value TEXT NOT NULL,                            -- 值 (JSON格式支持复杂类型)
    value_type TEXT DEFAULT 'string',               -- 类型: string, int, float, bool, duration, json

    -- ========== 显示信息 ==========
    label TEXT,                                     -- 显示名称
    description TEXT,                               -- 配置说明
    display_order INTEGER DEFAULT 0,                -- 显示顺序（同分类内排序）

    -- ========== 元信息 ==========
    requires_restart INTEGER DEFAULT 0,             -- 是否需要重启生效 (1=需要, 0=立即生效)

    -- ========== 审计字段 ==========
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),

    UNIQUE(category, key)
);

-- 设置表索引
CREATE INDEX IF NOT EXISTS idx_settings_category ON settings(category);
CREATE INDEX IF NOT EXISTS idx_settings_category_key ON settings(category, key);

-- 设置表触发器：自动更新 updated_at
CREATE TRIGGER IF NOT EXISTS update_settings_timestamp
    AFTER UPDATE ON settings
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE settings SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;

-- ============================================================================
-- 上游账号表
-- 账号池中的可调度账号（纯手动维护，无订阅源概念）
-- ============================================================================
CREATE TABLE IF NOT EXISTS upstream_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,

    provider_type TEXT NOT NULL DEFAULT 'api_key', -- api_key / session_cookie
    account_name TEXT NOT NULL,                     -- 账号展示名
    credential_raw TEXT NOT NULL,                   -- 凭据（首版明文）
    base_url TEXT NOT NULL DEFAULT 'https://api.openai.com', -- 上游基础URL
    model_rewrite_rules TEXT DEFAULT '',            -- Codex 模型兼容改写规则 JSON
    soft_failure_window_minutes INTEGER,           -- NULL 表示继承全局软失败窗口
    soft_failure_threshold INTEGER,                -- NULL 表示继承全局软失败阈值
    enable_request_compression INTEGER DEFAULT 0,   -- API Key 上游是否发送 zstd 请求体
    strip_codex_internal_metadata INTEGER NOT NULL DEFAULT 0, -- 账号级移除 Codex 内部元数据

    -- ========== 成本倍率 ==========
    cost_multiplier REAL DEFAULT 1.0,               -- 总成本倍率
    input_cost_multiplier REAL DEFAULT 1.0,         -- 输入成本倍率
    output_cost_multiplier REAL DEFAULT 1.0,        -- 输出成本倍率
    cache_creation_cost_multiplier REAL DEFAULT 1.0,-- 5分钟缓存创建成本倍率
    cache_creation_cost_multiplier_1h REAL DEFAULT 1.0, -- 1小时缓存创建成本倍率
    cache_read_cost_multiplier REAL DEFAULT 1.0,    -- 缓存读取成本倍率

    -- ========== 调度 ==========
    group_key TEXT DEFAULT '',                     -- 显式组别 primary/backup/cold
    priority INTEGER DEFAULT 100,                   -- 优先级（数字越小越高）
    enabled INTEGER DEFAULT 1,                      -- 是否可参与调度
    state TEXT DEFAULT 'active',                    -- active/cooldown/disabled_auth
    cooldown_until DATETIME,                        -- 冷却截止时间
    fail_count INTEGER DEFAULT 0,                   -- 失败次数

    -- ========== 状态 ==========
    last_success_at DATETIME,                       -- 最近成功时间
    last_error TEXT DEFAULT '',                     -- 最近错误
    plan_type TEXT DEFAULT '',                      -- 账号类型画像
    chatgpt_account_id TEXT DEFAULT '',            -- ChatGPT 账号ID
    chatgpt_user_id TEXT DEFAULT '',               -- ChatGPT 用户ID
    organization_id TEXT DEFAULT '',               -- 组织ID
    quota_5h_used_percent REAL,                    -- 5小时窗口已用百分比
    quota_5h_reset_at DATETIME,                    -- 5小时窗口重置时间
    quota_weekly_used_percent REAL,                -- 周窗口已用百分比
    quota_weekly_reset_at DATETIME,                -- 周窗口重置时间
    quota_status TEXT DEFAULT '',                  -- quota 状态
    quota_refreshed_at DATETIME,                   -- 最近 quota 刷新时间
    fingerprint TEXT UNIQUE NOT NULL,               -- 去重指纹

    -- ========== 审计字段 ==========
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_upstream_accounts_priority ON upstream_accounts(priority);
CREATE INDEX IF NOT EXISTS idx_upstream_accounts_enabled ON upstream_accounts(enabled);
CREATE INDEX IF NOT EXISTS idx_upstream_accounts_state ON upstream_accounts(state);
CREATE INDEX IF NOT EXISTS idx_upstream_accounts_cooldown_until ON upstream_accounts(cooldown_until);

CREATE TRIGGER IF NOT EXISTS update_upstream_accounts_timestamp
    AFTER UPDATE ON upstream_accounts
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE upstream_accounts SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;

-- ============================================================================
-- 隐私保护 (v5.3.0 新增 - 2026-06-11)
-- 出站请求隐私规则与全局设置；mode 是全局模式唯一真值来源
-- ============================================================================
CREATE TABLE IF NOT EXISTS privacy_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    mode TEXT NOT NULL DEFAULT 'disabled',          -- disabled / detect / redact
    scan_max_bytes INTEGER NOT NULL DEFAULT 4194304,-- 单请求累计扫描文本字节上限
    over_limit_action TEXT NOT NULL DEFAULT 'scan_prefix', -- scan_prefix / fail_closed
    on_error TEXT NOT NULL DEFAULT 'fail_open',     -- fail_open / fail_closed
    updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
);

INSERT OR IGNORE INTO privacy_settings (id) VALUES (1);

CREATE TABLE IF NOT EXISTS privacy_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    name TEXT NOT NULL,
    description TEXT DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 100,          -- 数字越小优先级越高
    match_type TEXT NOT NULL,                       -- literal / regex / builtin
    pattern TEXT NOT NULL,
    placeholder TEXT NOT NULL DEFAULT '[已脱敏]',
    action TEXT NOT NULL DEFAULT 'redact',          -- detect / redact
    scope_json TEXT NOT NULL DEFAULT '{}',          -- 作用域 JSON
    source TEXT NOT NULL DEFAULT 'custom',          -- custom / preset / builtin
    compile_error TEXT DEFAULT '',                  -- 历史规则编译失败信息
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_privacy_rules_enabled_priority
ON privacy_rules(enabled, priority);

INSERT INTO privacy_rules (
    enabled, name, description, priority, match_type, pattern, placeholder, action, scope_json, source
)
SELECT TRUE, '中国身份证号', '18 位格式、合法出生日期与校验位', 20, 'builtin', 'builtin:cn_id_card', '[身份证]', 'redact', '{}', 'builtin'
WHERE NOT EXISTS (
    SELECT 1 FROM privacy_rules WHERE source = 'builtin' AND pattern = 'builtin:cn_id_card'
);

INSERT INTO privacy_rules (
    enabled, name, description, priority, match_type, pattern, placeholder, action, scope_json, source
)
SELECT TRUE, '银行卡号', '13-19 位数字并通过 Luhn 校验', 30, 'builtin', 'builtin:bank_card_luhn', '[银行卡]', 'redact', '{}', 'builtin'
WHERE NOT EXISTS (
    SELECT 1 FROM privacy_rules WHERE source = 'builtin' AND pattern = 'builtin:bank_card_luhn'
);

INSERT INTO privacy_rules (
    enabled, name, description, priority, match_type, pattern, placeholder, action, scope_json, source
)
SELECT TRUE, '中国手机号', '中国大陆手机号，支持 +86 前缀', 40, 'builtin', 'builtin:cn_mobile', '[手机号]', 'redact', '{}', 'builtin'
WHERE NOT EXISTS (
    SELECT 1 FROM privacy_rules WHERE source = 'builtin' AND pattern = 'builtin:cn_mobile'
);

CREATE TABLE IF NOT EXISTS privacy_exact_secrets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    name TEXT NOT NULL,
    secret_value TEXT NOT NULL,
    value_hash TEXT NOT NULL,
    placeholder TEXT NOT NULL DEFAULT '[敏感值]',
    category TEXT NOT NULL DEFAULT 'custom',
    source_type TEXT NOT NULL DEFAULT 'manual',
    source_ref TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_privacy_exact_secrets_value_hash
ON privacy_exact_secrets(value_hash);

CREATE TRIGGER IF NOT EXISTS update_privacy_settings_timestamp
    AFTER UPDATE ON privacy_settings
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE privacy_settings SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS update_privacy_rules_timestamp
    AFTER UPDATE ON privacy_rules
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE privacy_rules SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS update_privacy_exact_secrets_timestamp
    AFTER UPDATE ON privacy_exact_secrets
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE privacy_exact_secrets SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;

-- ============================================================================
-- 按模型选择 Claude 上游组（P0）
-- members_json 保存端点名称数组；route_revision 用于后续运行态写入的版本校验
-- ============================================================================
CREATE TABLE IF NOT EXISTS model_routing_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    claude_enabled INTEGER NOT NULL DEFAULT 0 CHECK (claude_enabled IN (0, 1)),
    codex_enabled INTEGER NOT NULL DEFAULT 0 CHECK (codex_enabled IN (0, 1)),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
);

INSERT OR IGNORE INTO model_routing_settings (id) VALUES (1);

CREATE TABLE IF NOT EXISTS model_upstream_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    family TEXT NOT NULL DEFAULT 'claude' CHECK (family IN ('claude', 'codex')),
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    members_json TEXT NOT NULL CHECK (json_valid(members_json) AND json_type(members_json) = 'array'),
    route_mode TEXT NOT NULL DEFAULT 'auto' CHECK (route_mode IN ('auto', 'manual_preferred', 'manual_fixed')),
    route_target TEXT NOT NULL DEFAULT '',
    route_revision INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_model_upstream_groups_family_name
    ON model_upstream_groups(family, lower(name));

CREATE TABLE IF NOT EXISTS model_routing_assignments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    family TEXT NOT NULL DEFAULT 'claude' CHECK (family IN ('claude', 'codex')),
    match_type TEXT NOT NULL DEFAULT 'exact' CHECK (match_type IN ('exact', 'prefix')),
    model_name TEXT NOT NULL CHECK (length(trim(model_name)) > 0),
    group_id INTEGER NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    note TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_model_routing_assignments_family_model
    ON model_routing_assignments(family, match_type, lower(model_name));
CREATE INDEX IF NOT EXISTS idx_model_routing_assignments_group
    ON model_routing_assignments(group_id);

CREATE TRIGGER IF NOT EXISTS update_model_routing_settings_timestamp
    AFTER UPDATE ON model_routing_settings
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE model_routing_settings SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS update_model_upstream_groups_timestamp
    AFTER UPDATE ON model_upstream_groups
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE model_upstream_groups SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS update_model_routing_assignments_timestamp
    AFTER UPDATE ON model_routing_assignments
    FOR EACH ROW
    WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE model_routing_assignments SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
END;
