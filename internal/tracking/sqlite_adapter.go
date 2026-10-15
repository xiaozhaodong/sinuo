package tracking

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sinuo/internal/appdir"
	timezonepolicy "sinuo/internal/timezone"
)

//go:embed schema.sql
var sqliteSchemaFS embed.FS

// SQLiteAdapter SQLite数据库适配器实现（保持原有逻辑）
type SQLiteAdapter struct {
	config DatabaseConfig
	db     *sql.DB
	logger *slog.Logger
}

// NewSQLiteAdapter 创建SQLite适配器实例
func NewSQLiteAdapter(config DatabaseConfig) (*SQLiteAdapter, error) {
	// 设置默认配置
	setDefaultConfig(&config)

	adapter := &SQLiteAdapter{
		config: config,
		logger: slog.Default(),
	}

	return adapter, nil
}

// Open 建立SQLite数据库连接
func (s *SQLiteAdapter) Open() error {
	dbPath := s.config.DatabasePath
	if dbPath == "" {
		// 使用跨平台用户目录作为默认路径
		dbPath = filepath.Join(appdir.GetAppDataDir(), "data", "usage.db")
		s.logger.Info("使用默认数据库路径", "path", dbPath)
	}

	s.logger.Info("正在连接SQLite数据库", "path", dbPath)

	// 确保数据库目录存在
	if dbPath != ":memory:" {
		dbDir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			return fmt.Errorf("failed to create database directory: %w", err)
		}
	}

	db := sql.OpenDB(newSQLiteConnector(dbPath + sqliteDSNParams))

	// 设置连接池参数（SQLite建议少量连接）
	db.SetMaxOpenConns(1) // SQLite写操作需要单一连接
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	// 测试连接
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return fmt.Errorf("failed to ping SQLite database: %w", err)
	}
	if dbPath != ":memory:" {
		s.warnIfNotWAL(ctx, db, dbPath)
	}

	s.db = db

	s.logger.Info("✅ SQLite数据库连接成功")

	return nil
}

// warnIfNotWAL 校验 WAL 实际生效：无法切换时 SQLite 不报错，而是保持原模式
func (s *SQLiteAdapter) warnIfNotWAL(ctx context.Context, db *sql.DB, dbPath string) {
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		s.logger.Warn("读取SQLite journal_mode失败", "path", dbPath, "error", err)
		return
	}
	if !strings.EqualFold(mode, "wal") {
		s.logger.Warn("SQLite未进入WAL模式，外部读取可能阻塞写入", "path", dbPath, "journal_mode", mode)
	}
}

// Close 关闭数据库连接
func (s *SQLiteAdapter) Close() error {
	if s.db != nil {
		s.logger.Info("正在关闭SQLite数据库连接")
		return s.db.Close()
	}
	return nil
}

// Ping 测试数据库连接
func (s *SQLiteAdapter) Ping(ctx context.Context) error {
	if s.db == nil {
		return fmt.Errorf("database not connected")
	}
	return s.db.PingContext(ctx)
}

// GetDB 获取数据库连接
func (s *SQLiteAdapter) GetDB() *sql.DB {
	return s.db
}

// GetReadDB 获取读数据库连接
func (s *SQLiteAdapter) GetReadDB() *sql.DB {
	return s.db
}

// GetWriteDB 获取写数据库连接
func (s *SQLiteAdapter) GetWriteDB() *sql.DB {
	return s.db
}

// BeginTx 开始事务
func (s *SQLiteAdapter) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database not connected")
	}
	return s.db.BeginTx(ctx, opts)
}

// InitSchema 初始化SQLite数据库Schema
func (s *SQLiteAdapter) InitSchema() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.logger.Info("正在初始化SQLite数据库Schema")

	// 读取并执行SQLite schema
	schema, err := sqliteSchemaFS.ReadFile("schema.sql")
	if err != nil {
		return fmt.Errorf("failed to read schema.sql: %w", err)
	}

	// SQLite可以直接执行整个schema
	if _, err := s.db.ExecContext(ctx, string(schema)); err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	// v5.0.1+: 执行迁移添加新字段
	if err := s.migrateSchema(ctx); err != nil {
		return fmt.Errorf("failed to migrate schema: %w", err)
	}

	s.logger.Info("✅ SQLite数据库Schema初始化完成")
	return nil
}

// migrateSchema 执行数据库迁移（v5.0.1+: 添加 5m/1h 缓存字段）
func (s *SQLiteAdapter) migrateSchema(ctx context.Context) error {
	migrations := []struct {
		table       string
		checkColumn string
		alterSQL    string
		description string
	}{
		{
			table:       "request_logs",
			checkColumn: "first_token_ms",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN first_token_ms INTEGER",
			description: "上游首响耗时字段",
		},
		{
			table:       "request_logs",
			checkColumn: "completion_ms",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN completion_ms INTEGER",
			description: "流式首响后完成耗时字段",
		},
		{
			table:       "request_logs",
			checkColumn: "upstream_write_ms",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN upstream_write_ms INTEGER",
			description: "上游请求写完偏移字段（生命周期面板）",
		},
		{
			table:       "request_logs",
			checkColumn: "upstream_dispatch_ms",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN upstream_dispatch_ms INTEGER",
			description: "最终尝试开始取连接偏移字段（仅调度链路）",
		},
		{
			table:       "request_logs",
			checkColumn: "upstream_conn_ms",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN upstream_conn_ms INTEGER",
			description: "最终尝试连接就绪偏移字段（HTTP 链路）",
		},
		{
			table:       "request_logs",
			checkColumn: "schedule_snapshot_json",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN schedule_snapshot_json TEXT",
			description: "账号池调度快照字段（生命周期面板）",
		},
		{
			table:       "request_logs",
			checkColumn: "privacy_scan_json",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN privacy_scan_json TEXT",
			description: "隐私扫描摘要字段（生命周期面板）",
		},
		{
			table:       "request_logs",
			checkColumn: "request_model",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN request_model TEXT DEFAULT ''",
			description: "客户端请求模型字段（模型一致性）",
		},
		{
			table:       "request_logs",
			checkColumn: "session_id",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN session_id TEXT DEFAULT ''",
			description: "客户端会话字段",
		},
		{
			table:       "request_logs",
			checkColumn: "upstream_model",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN upstream_model TEXT DEFAULT ''",
			description: "上游实际模型字段（模型一致性）",
		},
		{
			table:       "request_logs",
			checkColumn: "response_model",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN response_model TEXT DEFAULT ''",
			description: "响应模型字段（模型一致性）",
		},
		{
			table:       "request_logs",
			checkColumn: "request_reasoning",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN request_reasoning TEXT DEFAULT ''",
			description: "请求思考配置字段（模型一致性）",
		},
		{
			table:       "request_logs",
			checkColumn: "response_reasoning",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN response_reasoning TEXT DEFAULT ''",
			description: "响应思考信息字段（模型一致性）",
		},
		{
			table:       "request_logs",
			checkColumn: "request_source",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN request_source TEXT NOT NULL DEFAULT 'legacy_proxy' CHECK (request_source IN ('legacy_proxy', 'codex_subscription_proxy'))",
			description: "请求入口来源字段",
		},
		{
			table:       "request_logs",
			checkColumn: "request_kind",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN request_kind TEXT NOT NULL DEFAULT 'legacy' CHECK (request_kind IN ('legacy', 'generation', 'compaction'))",
			description: "请求种类字段",
		},
		{
			table:       "request_logs",
			checkColumn: "cache_creation_5m_tokens",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN cache_creation_5m_tokens INTEGER DEFAULT 0",
			description: "5分钟缓存创建tokens字段",
		},
		{
			table:       "request_logs",
			checkColumn: "cache_creation_1h_tokens",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN cache_creation_1h_tokens INTEGER DEFAULT 0",
			description: "1小时缓存创建tokens字段",
		},
		{
			table:       "request_logs",
			checkColumn: "cache_creation_5m_cost_usd",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN cache_creation_5m_cost_usd REAL DEFAULT 0",
			description: "5分钟缓存创建成本字段",
		},
		{
			table:       "request_logs",
			checkColumn: "cache_creation_1h_cost_usd",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN cache_creation_1h_cost_usd REAL DEFAULT 0",
			description: "1小时缓存创建成本字段",
		},
		{
			table:       "request_logs",
			checkColumn: "upstream_type",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN upstream_type TEXT DEFAULT 'endpoint'",
			description: "上游来源类型字段",
		},
		{
			table:       "request_logs",
			checkColumn: "upstream_source_name",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN upstream_source_name TEXT DEFAULT ''",
			description: "上游来源名称字段",
		},
		{
			table:       "request_logs",
			checkColumn: "upstream_name",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN upstream_name TEXT DEFAULT ''",
			description: "上游名称字段",
		},
		{
			table:       "request_logs",
			checkColumn: "upstream_id",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN upstream_id INTEGER",
			description: "上游ID字段",
		},
		{
			table:       "request_logs",
			checkColumn: "route_mode",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN route_mode TEXT DEFAULT 'auto'",
			description: "Claude路由模式字段",
		},
		{
			table:       "request_logs",
			checkColumn: "requested_endpoint",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN requested_endpoint TEXT DEFAULT ''",
			description: "Claude手动请求端点字段",
		},
		{
			table:       "request_logs",
			checkColumn: "effective_endpoint",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN effective_endpoint TEXT DEFAULT ''",
			description: "Claude实际路由端点字段",
		},
		{
			table:       "request_logs",
			checkColumn: "fallback_reason",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN fallback_reason TEXT DEFAULT ''",
			description: "Claude路由fallback原因字段",
		},
		{
			table:       "request_logs",
			checkColumn: "route_decision_at",
			alterSQL:    "ALTER TABLE request_logs ADD COLUMN route_decision_at DATETIME",
			description: "Claude路由决策时间字段",
		},
		{
			table:       "endpoints",
			checkColumn: "soft_failure_window_minutes",
			alterSQL:    "ALTER TABLE endpoints ADD COLUMN soft_failure_window_minutes INTEGER",
			description: "独立软失败策略字段",
		},
		{
			table:       "endpoints",
			checkColumn: "soft_failure_threshold",
			alterSQL:    "ALTER TABLE endpoints ADD COLUMN soft_failure_threshold INTEGER",
			description: "独立软失败策略字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "soft_failure_window_minutes",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN soft_failure_window_minutes INTEGER",
			description: "独立软失败策略字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "soft_failure_threshold",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN soft_failure_threshold INTEGER",
			description: "独立软失败策略字段",
		},
		{
			table:       "endpoints",
			checkColumn: "availability_enabled",
			alterSQL:    "ALTER TABLE endpoints ADD COLUMN availability_enabled INTEGER NOT NULL DEFAULT 1",
			description: "端点硬启用字段（v8）",
		},
		{
			table:       "endpoints",
			checkColumn: "model_rewrite_rules",
			alterSQL:    "ALTER TABLE endpoints ADD COLUMN model_rewrite_rules TEXT DEFAULT ''",
			description: "CC端点模型兼容改写规则字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "group_key",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN group_key TEXT DEFAULT ''",
			description: "账号显式组别字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "model_rewrite_rules",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN model_rewrite_rules TEXT DEFAULT ''",
			description: "账号模型兼容改写规则字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "strip_codex_internal_metadata",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN strip_codex_internal_metadata INTEGER NOT NULL DEFAULT 0",
			description: "账号 Codex 内部元数据兼容开关字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "enable_request_compression",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN enable_request_compression INTEGER DEFAULT 0",
			description: "账号 zstd 请求压缩开关字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "cost_multiplier",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN cost_multiplier REAL DEFAULT 1.0",
			description: "账号总成本倍率字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "input_cost_multiplier",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN input_cost_multiplier REAL DEFAULT 1.0",
			description: "账号输入成本倍率字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "output_cost_multiplier",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN output_cost_multiplier REAL DEFAULT 1.0",
			description: "账号输出成本倍率字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "cache_creation_cost_multiplier",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN cache_creation_cost_multiplier REAL DEFAULT 1.0",
			description: "账号5分钟缓存创建倍率字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "cache_creation_cost_multiplier_1h",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN cache_creation_cost_multiplier_1h REAL DEFAULT 1.0",
			description: "账号1小时缓存创建倍率字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "cache_read_cost_multiplier",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN cache_read_cost_multiplier REAL DEFAULT 1.0",
			description: "账号缓存读取倍率字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "plan_type",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN plan_type TEXT DEFAULT ''",
			description: "账号类型画像字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "chatgpt_account_id",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN chatgpt_account_id TEXT DEFAULT ''",
			description: "ChatGPT 账号ID字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "chatgpt_user_id",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN chatgpt_user_id TEXT DEFAULT ''",
			description: "ChatGPT 用户ID字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "organization_id",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN organization_id TEXT DEFAULT ''",
			description: "组织ID字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "quota_5h_used_percent",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN quota_5h_used_percent REAL",
			description: "5小时窗口已用百分比字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "quota_5h_reset_at",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN quota_5h_reset_at DATETIME",
			description: "5小时窗口重置时间字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "quota_weekly_used_percent",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN quota_weekly_used_percent REAL",
			description: "周窗口已用百分比字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "quota_weekly_reset_at",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN quota_weekly_reset_at DATETIME",
			description: "周窗口重置时间字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "quota_status",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN quota_status TEXT DEFAULT ''",
			description: "quota 状态字段",
		},
		{
			table:       "upstream_accounts",
			checkColumn: "quota_refreshed_at",
			alterSQL:    "ALTER TABLE upstream_accounts ADD COLUMN quota_refreshed_at DATETIME",
			description: "quota 最近刷新时间字段",
		},
	}

	for _, m := range migrations {
		exists, err := s.columnExists(ctx, m.table, m.checkColumn)
		if err != nil {
			return fmt.Errorf("failed to check column %s: %w", m.checkColumn, err)
		}

		if !exists {
			s.logger.Info(fmt.Sprintf("🔧 [数据库迁移] 添加 %s", m.description))
			if _, err := s.db.ExecContext(ctx, m.alterSQL); err != nil {
				return fmt.Errorf("failed to add column %s: %w", m.checkColumn, err)
			}
			s.logger.Info(fmt.Sprintf("✅ [数据库迁移] %s 添加成功", m.description))
		}
	}
	if err := s.ensureUsageSummaryRequestKind(ctx); err != nil {
		return err
	}

	// v8：endpoint_runtime_states 运行态表（幂等；旧库升级路径）
	runtimeStateSQL := `CREATE TABLE IF NOT EXISTS endpoint_runtime_states (
		endpoint_id INTEGER NOT NULL,
		scope TEXT NOT NULL CHECK (scope IN ('global', 'messages', 'count_tokens')),
		state TEXT NOT NULL DEFAULT 'active',
		cooldown_until DATETIME,
		cooldown_reason TEXT NOT NULL DEFAULT '',
		revision INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
		PRIMARY KEY (endpoint_id, scope),
		FOREIGN KEY (endpoint_id) REFERENCES endpoints(id) ON DELETE CASCADE
	)`
	if _, err := s.db.ExecContext(ctx, runtimeStateSQL); err != nil {
		return fmt.Errorf("failed to create endpoint_runtime_states: %w", err)
	}

	// 索引迁移（幂等）
	indexSQLs := []string{
		// 旧 request_logs 必须先补 session_id 列，再创建会话索引。
		"CREATE INDEX IF NOT EXISTS idx_request_logs_session_time ON request_logs(session_id, start_time)",
		// 必须在 ensureUsageSummaryRequestKind 之后；旧库执行基础 schema 时尚无此列。
		"CREATE INDEX IF NOT EXISTS idx_usage_summary_kind ON usage_summary(request_kind)",
		"CREATE INDEX IF NOT EXISTS idx_request_logs_upstream_type ON request_logs(upstream_type)",
		"CREATE INDEX IF NOT EXISTS idx_request_logs_upstream_name ON request_logs(upstream_name)",
		"CREATE INDEX IF NOT EXISTS idx_upstream_accounts_group_key ON upstream_accounts(group_key)",
		"CREATE INDEX IF NOT EXISTS idx_endpoint_runtime_states_cooldown ON endpoint_runtime_states(scope, cooldown_until)",
	}
	for _, sqlStmt := range indexSQLs {
		if _, err := s.db.ExecContext(ctx, sqlStmt); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	if err := s.backfillCompletionMs(ctx); err != nil {
		return err
	}
	if err := s.ensurePrivacyExactSecretsSchema(ctx); err != nil {
		return err
	}
	if err := s.migrateDeprecatedAccountPoolSchema(ctx); err != nil {
		return err
	}
	if err := s.migrateModelRewriteRulesToExact(ctx); err != nil {
		return err
	}
	if err := s.ensureModelRoutingSchema(ctx); err != nil {
		return err
	}

	return nil
}

// ensureUsageSummaryRequestKind 事务化重建 summary 唯一键。
// 旧 usage_summary 仅用于发现曾使用过的时区；所有计数、用量与成本必须从 request_logs 回算。
func (s *SQLiteAdapter) ensureUsageSummaryRequestKind(ctx context.Context) error {
	hasKind, err := s.columnExists(ctx, "usage_summary", "request_kind")
	if err != nil {
		return fmt.Errorf("inspect usage_summary request_kind: %w", err)
	}
	var schema string
	if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='usage_summary'`).Scan(&schema); err != nil {
		return fmt.Errorf("inspect usage_summary schema: %w", err)
	}
	if hasKind && strings.Contains(strings.ToLower(schema), "request_family, request_kind, upstream_type") {
		return nil
	}
	hasTimezone, err := s.columnExists(ctx, "usage_summary", "timezone_name")
	if err != nil {
		return fmt.Errorf("inspect usage_summary timezone: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS update_usage_summary_timestamp`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE usage_summary RENAME TO usage_summary_legacy_kind`); err != nil {
		return fmt.Errorf("rename legacy usage_summary: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE usage_summary (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timezone_name TEXT NOT NULL, date TEXT NOT NULL, model_name TEXT NOT NULL,
		request_family TEXT NOT NULL CHECK (request_family IN ('claude','codex','image','other')),
		request_kind TEXT NOT NULL DEFAULT 'legacy' CHECK (request_kind IN ('legacy','generation','compaction')),
		upstream_type TEXT NOT NULL DEFAULT '', upstream_name TEXT NOT NULL DEFAULT '', upstream_id INTEGER NOT NULL DEFAULT 0,
		request_count INTEGER DEFAULT 0, success_count INTEGER DEFAULT 0, error_count INTEGER DEFAULT 0,
		total_input_tokens INTEGER DEFAULT 0, total_output_tokens INTEGER DEFAULT 0,
		total_cache_creation_tokens INTEGER DEFAULT 0, total_cache_read_tokens INTEGER DEFAULT 0,
		total_cost_usd REAL DEFAULT 0, avg_duration_ms REAL DEFAULT 0,
		created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
		updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
		UNIQUE(timezone_name, date, model_name, request_family, request_kind, upstream_type, upstream_name, upstream_id)
	)`); err != nil {
		return fmt.Errorf("create classified usage_summary: %w", err)
	}
	timezoneNames := make([]string, 0, 1)
	if hasTimezone {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT timezone_name FROM usage_summary_legacy_kind WHERE trim(timezone_name) <> '' ORDER BY timezone_name`)
		if err != nil {
			return fmt.Errorf("read legacy usage_summary timezones: %w", err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				return err
			}
			timezoneNames = append(timezoneNames, name)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if len(timezoneNames) == 0 {
		timezoneNames = append(timezoneNames, "UTC")
	}

	var firstRaw, lastRaw sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT CAST(MIN(start_time) AS TEXT), CAST(MAX(start_time) AS TEXT) FROM request_logs`).Scan(&firstRaw, &lastRaw); err != nil {
		return fmt.Errorf("read request history range: %w", err)
	}
	if firstRaw.Valid && lastRaw.Valid {
		first, err := timezonepolicy.ParseStorage(firstRaw.String)
		if err != nil {
			return fmt.Errorf("parse first request time: %w", err)
		}
		last, err := timezonepolicy.ParseStorage(lastRaw.String)
		if err != nil {
			return fmt.Errorf("parse last request time: %w", err)
		}
		end := last.Add(time.Microsecond)
		for _, name := range timezoneNames {
			policy, err := timezonepolicy.New(name)
			if err != nil {
				return fmt.Errorf("load usage_summary timezone %q: %w", name, err)
			}
			summaries, err := AggregateUsageSummaryWithQuerier(ctx, tx, first, end, nil, policy)
			if err != nil {
				return fmt.Errorf("recalculate usage_summary for %q: %w", name, err)
			}
			if err := insertUsageSummaries(ctx, tx, summaries); err != nil {
				return fmt.Errorf("insert recalculated usage_summary for %q: %w", name, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE usage_summary_legacy_kind`); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE INDEX idx_usage_summary_date ON usage_summary(date)`,
		`CREATE INDEX idx_usage_summary_timezone_date ON usage_summary(timezone_name, date)`,
		`CREATE INDEX idx_usage_summary_model ON usage_summary(model_name)`,
		`CREATE INDEX idx_usage_summary_family ON usage_summary(request_family)`,
		`CREATE INDEX idx_usage_summary_upstream ON usage_summary(upstream_type, upstream_name, upstream_id)`,
		`CREATE TRIGGER update_usage_summary_timestamp AFTER UPDATE ON usage_summary FOR EACH ROW WHEN NEW.updated_at = OLD.updated_at BEGIN UPDATE usage_summary SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id; END`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ensureModelRoutingSchema 为未经过全量 schema.sql 的旧数据库补齐模型路由表。
// 所有语句均幂等，和 schema.sql 保持同一结构。
func (s *SQLiteAdapter) ensureModelRoutingSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS model_routing_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			claude_enabled INTEGER NOT NULL DEFAULT 0 CHECK (claude_enabled IN (0, 1)),
			codex_enabled INTEGER NOT NULL DEFAULT 0 CHECK (codex_enabled IN (0, 1)),
			updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
		)`,
		`INSERT OR IGNORE INTO model_routing_settings (id) VALUES (1)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("failed to ensure model routing schema: %w", err)
		}
	}
	// 旧库的 settings 表可能已经存在但还没有 Codex 开关。
	var hasCodex bool
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(model_routing_settings)`)
	if err != nil {
		return fmt.Errorf("inspect model routing settings schema failed: %w", err)
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var defaultValue interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "codex_enabled" {
			hasCodex = true
		}
	}
	_ = rows.Close()
	if !hasCodex {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE model_routing_settings ADD COLUMN codex_enabled INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add codex_enabled setting failed: %w", err)
		}
	}
	statements = []string{
		`CREATE TABLE IF NOT EXISTS model_upstream_groups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			family TEXT NOT NULL DEFAULT 'claude' CHECK (family IN ('claude', 'codex')),
			name TEXT NOT NULL CHECK (length(trim(name)) > 0),
			members_json TEXT NOT NULL CHECK (json_valid(members_json) AND json_type(members_json) = 'array'),
			route_mode TEXT NOT NULL DEFAULT 'auto' CHECK (route_mode IN ('auto', 'manual_preferred', 'manual_fixed')),
			route_target TEXT NOT NULL DEFAULT '',
			route_revision INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
			updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_model_upstream_groups_family_name
			ON model_upstream_groups(family, lower(name))`,
		`CREATE TABLE IF NOT EXISTS model_routing_assignments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			family TEXT NOT NULL DEFAULT 'claude' CHECK (family IN ('claude', 'codex')),
			match_type TEXT NOT NULL DEFAULT 'exact' CHECK (match_type IN ('exact', 'prefix')),
			model_name TEXT NOT NULL CHECK (length(trim(model_name)) > 0),
			group_id INTEGER NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
			note TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
			updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_model_routing_assignments_family_model
			ON model_routing_assignments(family, match_type, lower(model_name))`,
		`CREATE INDEX IF NOT EXISTS idx_model_routing_assignments_group
			ON model_routing_assignments(group_id)`,
		`CREATE TRIGGER IF NOT EXISTS update_model_routing_settings_timestamp
			AFTER UPDATE ON model_routing_settings
			FOR EACH ROW WHEN NEW.updated_at = OLD.updated_at
		BEGIN
			UPDATE model_routing_settings SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS update_model_upstream_groups_timestamp
			AFTER UPDATE ON model_upstream_groups
			FOR EACH ROW WHEN NEW.updated_at = OLD.updated_at
		BEGIN
			UPDATE model_upstream_groups SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS update_model_routing_assignments_timestamp
			AFTER UPDATE ON model_routing_assignments
			FOR EACH ROW WHEN NEW.updated_at = OLD.updated_at
		BEGIN
			UPDATE model_routing_assignments SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
		END`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("failed to ensure model routing schema: %w", err)
		}
	}
	return nil
}

func (s *SQLiteAdapter) backfillCompletionMs(ctx context.Context) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE request_logs
		SET completion_ms = CASE
			WHEN duration_ms - first_token_ms < 0 THEN 0
			ELSE duration_ms - first_token_ms
		END
		WHERE completion_ms IS NULL
			AND duration_ms IS NOT NULL
			AND first_token_ms IS NOT NULL
	`)
	if err != nil {
		return fmt.Errorf("failed to backfill completion_ms: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err == nil && rowsAffected > 0 {
		s.logger.Info(fmt.Sprintf("✅ [数据库迁移] completion_ms 历史数据回填完成: %d 条", rowsAffected))
	}
	return nil
}

func (s *SQLiteAdapter) ensurePrivacyExactSecretsSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS privacy_exact_secrets (
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
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_privacy_exact_secrets_value_hash
			ON privacy_exact_secrets(value_hash)`,
		`CREATE TRIGGER IF NOT EXISTS update_privacy_exact_secrets_timestamp
			AFTER UPDATE ON privacy_exact_secrets
			FOR EACH ROW
			WHEN NEW.updated_at = OLD.updated_at
		BEGIN
			UPDATE privacy_exact_secrets
			SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')
			WHERE id = NEW.id;
		END`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to ensure privacy exact secrets schema: %w", err)
		}
	}
	return nil
}

func (s *SQLiteAdapter) migrateModelRewriteRulesToExact(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, model_rewrite_rules
		FROM upstream_accounts
		WHERE TRIM(COALESCE(model_rewrite_rules, '')) != ''
	`)
	if err != nil {
		return fmt.Errorf("failed to read model rewrite rules: %w", err)
	}
	defer rows.Close()

	updates := make(map[int64]string)
	for rows.Next() {
		var (
			id    int64
			rules string
		)
		if err := rows.Scan(&id, &rules); err != nil {
			return fmt.Errorf("failed to scan model rewrite rules: %w", err)
		}
		if normalized, changed := normalizeModelRewriteRulesToExact(rules); changed {
			updates[id] = normalized
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate model rewrite rules: %w", err)
	}
	for id, rules := range updates {
		if _, err := s.db.ExecContext(ctx, `UPDATE upstream_accounts SET model_rewrite_rules = ? WHERE id = ?`, rules, id); err != nil {
			return fmt.Errorf("failed to migrate model rewrite rules to exact matching: %w", err)
		}
	}
	return nil
}

func normalizeModelRewriteRulesToExact(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw, false
	}

	var rules []map[string]any
	if err := json.Unmarshal([]byte(raw), &rules); err == nil {
		if !rewriteRuleMatchesToExact(rules) {
			return raw, false
		}
		encoded, err := json.Marshal(rules)
		if err != nil {
			return raw, false
		}
		return string(encoded), true
	}

	var single map[string]any
	if err := json.Unmarshal([]byte(raw), &single); err != nil {
		return raw, false
	}
	if !rewriteRuleMatchesToExact([]map[string]any{single}) {
		return raw, false
	}
	encoded, err := json.Marshal(single)
	if err != nil {
		return raw, false
	}
	return string(encoded), true
}

func rewriteRuleMatchesToExact(rules []map[string]any) bool {
	changed := false
	for _, rule := range rules {
		match, ok := rule["match"].(string)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(match), "prefix") {
			rule["match"] = "exact"
			changed = true
		}
	}
	return changed
}

func (s *SQLiteAdapter) migrateDeprecatedAccountPoolSchema(ctx context.Context) (retErr error) {
	hasLegacySourceID, err := s.columnExists(ctx, "upstream_accounts", "source_id")
	if err != nil {
		return fmt.Errorf("failed to inspect upstream_accounts schema: %w", err)
	}
	hasLegacyAccountLogs, err := s.tableExists(ctx, "account_request_logs")
	if err != nil {
		return fmt.Errorf("failed to inspect account_request_logs table: %w", err)
	}
	hasLegacySyncLogs, err := s.tableExists(ctx, "sync_logs")
	if err != nil {
		return fmt.Errorf("failed to inspect sync_logs table: %w", err)
	}
	hasLegacySources, err := s.tableExists(ctx, "subscription_sources")
	if err != nil {
		return fmt.Errorf("failed to inspect subscription_sources table: %w", err)
	}
	hasMirrorInsert, err := s.triggerExists(ctx, "mirror_account_logs_after_insert")
	if err != nil {
		return fmt.Errorf("failed to inspect mirror insert trigger: %w", err)
	}
	hasMirrorUpdate, err := s.triggerExists(ctx, "mirror_account_logs_after_update")
	if err != nil {
		return fmt.Errorf("failed to inspect mirror update trigger: %w", err)
	}
	hasMirrorDelete, err := s.triggerExists(ctx, "mirror_account_logs_after_delete")
	if err != nil {
		return fmt.Errorf("failed to inspect mirror delete trigger: %w", err)
	}

	legacyDetected := hasLegacySourceID || hasLegacyAccountLogs || hasLegacySyncLogs || hasLegacySources ||
		hasMirrorInsert || hasMirrorUpdate || hasMirrorDelete
	if !legacyDetected {
		return nil
	}

	foreignKeysDisabled := false
	if hasLegacySourceID {
		if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
			return fmt.Errorf("failed to disable foreign keys before upstream_accounts rebuild: %w", err)
		}
		foreignKeysDisabled = true
		defer func() {
			if !foreignKeysDisabled {
				return
			}
			if _, err := s.db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil && retErr == nil {
				retErr = fmt.Errorf("failed to re-enable foreign keys after upstream_accounts rebuild: %w", err)
			}
		}()

		s.logger.Info("🔧 [数据库迁移] 重建 upstream_accounts，移除 source_id")
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin upstream_accounts rebuild transaction: %w", err)
		}
		defer tx.Rollback()

		rebuildSQLs := []string{
			`CREATE TABLE IF NOT EXISTS upstream_accounts_new (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				provider_type TEXT NOT NULL DEFAULT 'api_key',
				account_name TEXT NOT NULL,
				credential_raw TEXT NOT NULL,
				base_url TEXT NOT NULL DEFAULT 'https://api.openai.com',
				model_rewrite_rules TEXT DEFAULT '',
				soft_failure_window_minutes INTEGER,
				soft_failure_threshold INTEGER,
				enable_request_compression INTEGER DEFAULT 0,
				strip_codex_internal_metadata INTEGER NOT NULL DEFAULT 0,
				cost_multiplier REAL DEFAULT 1.0,
				input_cost_multiplier REAL DEFAULT 1.0,
				output_cost_multiplier REAL DEFAULT 1.0,
				cache_creation_cost_multiplier REAL DEFAULT 1.0,
				cache_creation_cost_multiplier_1h REAL DEFAULT 1.0,
				cache_read_cost_multiplier REAL DEFAULT 1.0,
				group_key TEXT DEFAULT '',
				priority INTEGER DEFAULT 100,
				enabled INTEGER DEFAULT 1,
				state TEXT DEFAULT 'active',
				cooldown_until DATETIME,
				fail_count INTEGER DEFAULT 0,
				last_success_at DATETIME,
				last_error TEXT DEFAULT '',
				plan_type TEXT DEFAULT '',
				chatgpt_account_id TEXT DEFAULT '',
				chatgpt_user_id TEXT DEFAULT '',
				organization_id TEXT DEFAULT '',
				quota_5h_used_percent REAL,
				quota_5h_reset_at DATETIME,
				quota_weekly_used_percent REAL,
				quota_weekly_reset_at DATETIME,
				quota_status TEXT DEFAULT '',
				quota_refreshed_at DATETIME,
				fingerprint TEXT UNIQUE NOT NULL,
				created_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')),
				updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
			)`,
			`INSERT INTO upstream_accounts_new (
				id, provider_type, account_name, credential_raw, base_url, model_rewrite_rules, soft_failure_window_minutes, soft_failure_threshold, enable_request_compression, strip_codex_internal_metadata,
				cost_multiplier, input_cost_multiplier, output_cost_multiplier,
				cache_creation_cost_multiplier, cache_creation_cost_multiplier_1h, cache_read_cost_multiplier,
				group_key, priority, enabled, state, cooldown_until, fail_count,
				last_success_at, last_error, plan_type, chatgpt_account_id, chatgpt_user_id, organization_id,
				quota_5h_used_percent, quota_5h_reset_at, quota_weekly_used_percent, quota_weekly_reset_at,
				quota_status, quota_refreshed_at, fingerprint, created_at, updated_at
			)
			SELECT
				id, provider_type, account_name, credential_raw, base_url,
				'' AS model_rewrite_rules,
				soft_failure_window_minutes, soft_failure_threshold,
				enable_request_compression,
				strip_codex_internal_metadata,
				1.0 AS cost_multiplier,
				1.0 AS input_cost_multiplier,
				1.0 AS output_cost_multiplier,
				1.0 AS cache_creation_cost_multiplier,
				1.0 AS cache_creation_cost_multiplier_1h,
				1.0 AS cache_read_cost_multiplier,
				'' AS group_key,
				priority, enabled, state, cooldown_until, fail_count,
				last_success_at, last_error,
				'' AS plan_type,
				'' AS chatgpt_account_id,
				'' AS chatgpt_user_id,
				'' AS organization_id,
				NULL AS quota_5h_used_percent,
				NULL AS quota_5h_reset_at,
				NULL AS quota_weekly_used_percent,
				NULL AS quota_weekly_reset_at,
				'' AS quota_status,
				NULL AS quota_refreshed_at,
				fingerprint, created_at, updated_at
			FROM upstream_accounts`,
			"DROP TABLE upstream_accounts",
			"ALTER TABLE upstream_accounts_new RENAME TO upstream_accounts",
			"CREATE INDEX IF NOT EXISTS idx_upstream_accounts_priority ON upstream_accounts(priority)",
			"CREATE INDEX IF NOT EXISTS idx_upstream_accounts_group_key ON upstream_accounts(group_key)",
			"CREATE INDEX IF NOT EXISTS idx_upstream_accounts_enabled ON upstream_accounts(enabled)",
			"CREATE INDEX IF NOT EXISTS idx_upstream_accounts_state ON upstream_accounts(state)",
			"CREATE INDEX IF NOT EXISTS idx_upstream_accounts_cooldown_until ON upstream_accounts(cooldown_until)",
			`CREATE TRIGGER IF NOT EXISTS update_upstream_accounts_timestamp
				AFTER UPDATE ON upstream_accounts
				FOR EACH ROW
				WHEN NEW.updated_at = OLD.updated_at
			BEGIN
				UPDATE upstream_accounts SET updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now') WHERE id = NEW.id;
			END`,
		}
		for _, stmt := range rebuildSQLs {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("failed to rebuild upstream_accounts: %w", err)
			}
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit upstream_accounts rebuild: %w", err)
		}
		s.logger.Info("✅ [数据库迁移] upstream_accounts 已移除 source_id")
	}

	cleanupSQLs := []string{
		"DROP TRIGGER IF EXISTS mirror_account_logs_after_insert",
		"DROP TRIGGER IF EXISTS mirror_account_logs_after_update",
		"DROP TRIGGER IF EXISTS mirror_account_logs_after_delete",
		"DROP TABLE IF EXISTS account_request_logs",
		"DROP TABLE IF EXISTS sync_logs",
		"DROP TABLE IF EXISTS subscription_sources",
	}
	for _, stmt := range cleanupSQLs {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to cleanup deprecated account pool schema: %w", err)
		}
	}

	return nil
}

// columnExists 检查表中是否存在指定列
func (s *SQLiteAdapter) columnExists(ctx context.Context, table, column string) (bool, error) {
	query := fmt.Sprintf("PRAGMA table_info(%s)", table)
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var dataType string
		var notNull int
		var dfltValue interface{}
		var pk int

		if err := rows.Scan(&cid, &name, &dataType, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}

		if name == column {
			return true, nil
		}
	}

	return false, nil
}

func (s *SQLiteAdapter) tableExists(ctx context.Context, table string) (bool, error) {
	return s.sqliteObjectExists(ctx, "table", table)
}

func (s *SQLiteAdapter) triggerExists(ctx context.Context, trigger string) (bool, error) {
	return s.sqliteObjectExists(ctx, "trigger", trigger)
}

func (s *SQLiteAdapter) sqliteObjectExists(ctx context.Context, objectType, name string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?",
		objectType, name,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// BuildInsertOrReplaceQuery 构建插入或更新查询（SQLite语法）
// 使用 INSERT ... ON CONFLICT DO UPDATE 来避免数据丢失
func (s *SQLiteAdapter) BuildInsertOrReplaceQuery(table string, columns []string, values []string) string {
	columnsStr := strings.Join(columns, ", ")
	valuesStr := strings.Join(values, ", ")

	// 构建INSERT部分
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, columnsStr, valuesStr)

	// 根据表结构推断冲突键
	containsColumn := func(name string) bool {
		for _, col := range columns {
			if col == name {
				return true
			}
		}
		return false
	}

	conflictColumns := make([]string, 0)
	switch table {
	case "request_logs":
		if containsColumn("request_id") {
			conflictColumns = append(conflictColumns, "request_id")
		}
	case "usage_summary":
		if containsColumn("timezone_name") && containsColumn("date") && containsColumn("model_name") &&
			containsColumn("request_family") && containsColumn("request_kind") && containsColumn("upstream_type") &&
			containsColumn("upstream_name") && containsColumn("upstream_id") {
			conflictColumns = append(conflictColumns, "timezone_name", "date", "model_name", "request_family", "request_kind", "upstream_type", "upstream_name", "upstream_id")
		}
	default:
		if containsColumn("request_id") {
			conflictColumns = append(conflictColumns, "request_id")
		}
	}

	// 构建ON CONFLICT DO UPDATE部分，对start_time字段进行特殊处理
	// 对于request_logs表，主键冲突时更新提供的字段（除了request_id主键）
	var updatePairs []string
	for _, col := range columns {
		skipInUpdate := false
		for _, conflictCol := range conflictColumns {
			if col == conflictCol {
				skipInUpdate = true
				break
			}
		}
		if !skipInUpdate { // 跳过冲突键字段
			if col == "start_time" {
				// 对start_time使用COALESCE，只在原值为NULL时才更新
				updatePairs = append(updatePairs, fmt.Sprintf("%s = COALESCE(request_logs.%s, EXCLUDED.%s)", col, col, col))
			} else {
				updatePairs = append(updatePairs, fmt.Sprintf("%s = EXCLUDED.%s", col, col))
			}
		}
	}

	if len(updatePairs) > 0 && len(conflictColumns) > 0 {
		query += " ON CONFLICT(" + strings.Join(conflictColumns, ", ") + ") DO UPDATE SET " + strings.Join(updatePairs, ", ")
	} else {
		// 如果没有可更新字段或没有冲突键，则使用IGNORE避免重复插入
		query = fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) VALUES (%s)", table, columnsStr, valuesStr)
	}

	return query
}

// BuildUTCDateTimeNow 返回数据库无关的 UTC 固定格式表达式。
func (s *SQLiteAdapter) BuildUTCDateTimeNow() string {
	return fmt.Sprintf("'%s'", timezonepolicy.FormatStorage(time.Now()))
}

// BuildLimitOffset 构建分页查询
func (s *SQLiteAdapter) BuildLimitOffset(limit, offset int) string {
	if limit <= 0 {
		return ""
	}
	if offset <= 0 {
		return fmt.Sprintf(" LIMIT %d", limit)
	}
	return fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
}

// VacuumDatabase SQLite执行VACUUM操作
func (s *SQLiteAdapter) VacuumDatabase(ctx context.Context) error {
	s.logger.Info("正在执行SQLite VACUUM操作")

	_, err := s.db.ExecContext(ctx, "VACUUM")
	if err != nil {
		return fmt.Errorf("failed to vacuum SQLite database: %w", err)
	}

	s.logger.Info("✅ SQLite VACUUM操作完成")
	return nil
}

// GetDatabaseStats 获取SQLite数据库统计信息
func (s *SQLiteAdapter) GetDatabaseStats(ctx context.Context) (*DatabaseStats, error) {
	stats := &DatabaseStats{}

	// 获取请求记录总数
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM request_logs").Scan(&stats.TotalRequests)
	if err != nil {
		return nil, fmt.Errorf("failed to get total requests count: %w", err)
	}

	// 获取汇总记录总数
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_summary").Scan(&stats.TotalSummaries)
	if err != nil {
		return nil, fmt.Errorf("failed to get total summaries count: %w", err)
	}

	// 获取最早和最新的记录时间
	var earliestStr, latestStr sql.NullString
	err = s.db.QueryRowContext(ctx, "SELECT CAST(MIN(start_time) AS TEXT), CAST(MAX(start_time) AS TEXT) FROM request_logs").Scan(&earliestStr, &latestStr)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get record time range: %w", err)
	}

	if earliestStr.Valid {
		if t, err := timezonepolicy.ParseStorage(earliestStr.String); err == nil {
			stats.EarliestRecord = &t
		}
	}
	if latestStr.Valid {
		if t, err := timezonepolicy.ParseStorage(latestStr.String); err == nil {
			stats.LatestRecord = &t
		}
	}

	// 获取数据库文件大小（SQLite特有）
	var pageCount, pageSize int64
	err = s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount)
	if err == nil {
		err = s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize)
		if err == nil {
			stats.DatabaseSize = pageCount * pageSize
		}
	}

	// 获取总成本
	err = s.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(total_cost_usd), 0) FROM request_logs WHERE total_cost_usd > 0").Scan(&stats.TotalCostUSD)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get total cost: %w", err)
	}

	return stats, nil
}

// GetConnectionStats 获取连接池统计信息
func (s *SQLiteAdapter) GetConnectionStats() ConnectionStats {
	if s.db == nil {
		return ConnectionStats{}
	}

	dbStats := s.db.Stats()
	return ConnectionStats{
		OpenConnections:  dbStats.OpenConnections,
		IdleConnections:  dbStats.Idle,
		InUseConnections: dbStats.InUse,
		WaitCount:        dbStats.WaitCount,
		WaitDuration:     dbStats.WaitDuration,
		MaxLifetime:      0, // SQLite不限制连接生命周期
	}
}

// GetDatabaseType 返回数据库类型标识
func (s *SQLiteAdapter) GetDatabaseType() string {
	return "sqlite"
}
