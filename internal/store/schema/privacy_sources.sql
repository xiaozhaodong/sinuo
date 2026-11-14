CREATE TABLE IF NOT EXISTS privacy_config_sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    path TEXT NOT NULL UNIQUE,
    resolved_path TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('file', 'directory')),
    patterns TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    content_hash TEXT NOT NULL DEFAULT '',
    last_scan_at TEXT NOT NULL DEFAULT '',
    last_success_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    file_count INTEGER NOT NULL DEFAULT 0,
    value_count INTEGER NOT NULL DEFAULT 0,
    excluded_count INTEGER NOT NULL DEFAULT 0
);
-- 仅保存最新候选的值摘要；待确认数量由它与本地敏感值库实时关联计算。
CREATE TABLE IF NOT EXISTS privacy_config_source_reviews (
    source_id INTEGER NOT NULL,
    value_hash TEXT NOT NULL,
    PRIMARY KEY(source_id, value_hash)
);
CREATE TABLE IF NOT EXISTS privacy_config_source_values (
    source_id INTEGER NOT NULL,
    file_path TEXT NOT NULL,
    field_path TEXT NOT NULL,
    value_hash TEXT NOT NULL,
    active INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY(source_id, file_path, field_path, value_hash)
);
