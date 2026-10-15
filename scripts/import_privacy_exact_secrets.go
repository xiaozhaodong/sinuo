// Command import_privacy_exact_secrets imports CSV rows into privacy_exact_secrets.
//
// 用法:
//
//	go run ./scripts/import_privacy_exact_secrets.go --csv /path/to/secrets.csv --dry-run
//	go run ./scripts/import_privacy_exact_secrets.go --csv /path/to/secrets.csv
//
// CSV 需要表头，支持列:
// name,secret_value,category,placeholder,enabled,description,source_type,source_ref
//
// 默认按 sha256(secret_value) 幂等 upsert: 已存在的敏感值会更新元数据，不会删除 CSV 中缺失的旧数据。
package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cc-forwarder/internal/service"
	"cc-forwarder/internal/utils"

	_ "modernc.org/sqlite"
)

const templateCSV = `name,secret_value,category,placeholder,enabled,description,source_ref
生产 OpenAI Key,sk-proj-replace-with-real-key-123456,api_key,[OpenAI密钥],true,生产转发 key,prod-openai
内部服务 Token,tok_replace_with_real_token_123456,token,[Token],true,内部服务 token,internal-token
`

type importRow struct {
	RowNumber   int
	Enabled     bool
	Name        string
	SecretValue string
	ValueHash   string
	Placeholder string
	Category    string
	SourceType  string
	SourceRef   string
	Description string
}

type existingSecret struct {
	ID          int64
	Enabled     bool
	Name        string
	SecretValue string
	ValueHash   string
	Placeholder string
	Category    string
	SourceType  string
	SourceRef   string
	Description string
}

type plannedUpdate struct {
	ID  int64
	Row importRow
}

type importPlan struct {
	Rows              int
	Inserts           []importRow
	Updates           []plannedUpdate
	SkippedUnchanged  int
	SkippedExisting   int
	SkippedEmptyLines int
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "导入失败: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("import_privacy_exact_secrets", flag.ContinueOnError)
	fs.SetOutput(out)

	csvPath := fs.String("csv", "", "CSV 文件路径")
	dbPath := fs.String("db", defaultDBPath(), "usage.db 路径")
	dryRun := fs.Bool("dry-run", false, "只校验和预览，不写入数据库")
	insertOnly := fs.Bool("insert-only", false, "只插入新敏感值，已存在的 value_hash 直接跳过")
	createDB := fs.Bool("create-db", false, "允许在数据库文件不存在时创建新 usage.db")
	printTemplate := fs.Bool("print-template", false, "打印 CSV 模板后退出")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *printTemplate {
		_, err := fmt.Fprint(out, templateCSV)
		return err
	}
	if strings.TrimSpace(*csvPath) == "" {
		return errors.New("必须提供 --csv，或先用 --print-template 查看模板")
	}

	expandedCSVPath, err := expandPath(*csvPath)
	if err != nil {
		return err
	}
	rows, skippedEmpty, err := readImportCSV(expandedCSVPath)
	if err != nil {
		return err
	}

	expandedDBPath, err := expandPath(*dbPath)
	if err != nil {
		return err
	}
	dbExists, err := dbFileExists(expandedDBPath)
	if err != nil {
		return err
	}
	if !dbExists {
		if *dryRun {
			plan := importPlan{
				Rows:              len(rows),
				Inserts:           rows,
				SkippedEmptyLines: skippedEmpty,
			}
			printPlan(out, expandedCSVPath, expandedDBPath, plan, true)
			fmt.Fprintln(out, "提示: dry-run 未创建数据库文件；实际导入时如需新建空库，请加 --create-db。")
			return nil
		}
		if !*createDB {
			return fmt.Errorf("数据库不存在: %s；如确认要新建空库，请加 --create-db", expandedDBPath)
		}
		if err := os.MkdirAll(filepath.Dir(expandedDBPath), 0755); err != nil {
			return fmt.Errorf("创建数据库目录失败: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := openSQLite(expandedDBPath, *dryRun)
	if err != nil {
		return err
	}
	defer db.Close()

	if !*dryRun {
		if err := ensurePrivacySchema(ctx, db); err != nil {
			return err
		}
	}
	if *dryRun {
		exists, err := privacyExactSecretsTableExists(ctx, db)
		if err != nil {
			return err
		}
		if !exists {
			plan := importPlan{
				Rows:              len(rows),
				Inserts:           rows,
				SkippedEmptyLines: skippedEmpty,
			}
			printPlan(out, expandedCSVPath, expandedDBPath, plan, true)
			fmt.Fprintln(out, "提示: 目标库尚未包含 privacy_exact_secrets 表；dry-run 未初始化 schema。")
			return nil
		}
	}

	plan, err := buildImportPlan(ctx, db, rows, !*insertOnly)
	if err != nil {
		return err
	}
	plan.Rows = len(rows)
	plan.SkippedEmptyLines = skippedEmpty

	printPlan(out, expandedCSVPath, expandedDBPath, plan, *dryRun)
	if *dryRun {
		return nil
	}
	if err := applyImportPlan(ctx, db, plan); err != nil {
		return err
	}
	fmt.Fprintln(out, "导入完成。")
	fmt.Fprintln(out, "提示: 如果 Sinuo 桌面端正在运行，请重启应用，或在隐私保护页面保存一次设置以重建运行时快照。")
	return nil
}

func defaultDBPath() string {
	return filepath.Join(utils.GetDataDir(), "usage.db")
}

func expandPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("读取用户目录失败: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	return filepath.Abs(path)
}

func dbFileExists(dbPath string) (bool, error) {
	info, err := os.Stat(dbPath)
	if err == nil {
		if info.IsDir() {
			return false, fmt.Errorf("数据库路径是目录: %s", dbPath)
		}
		return true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("检查数据库失败: %w", err)
	}
	return false, nil
}

func openSQLite(dbPath string, readOnly bool) (*sql.DB, error) {
	// modernc 只识别 _pragma / _txlock；与运行中的 App 同时访问时靠 busy_timeout 等待
	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	if readOnly {
		dsn = dbPath + "?_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func ensurePrivacySchema(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS privacy_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			mode TEXT NOT NULL DEFAULT 'disabled',
			scan_max_bytes INTEGER NOT NULL DEFAULT 4194304,
			over_limit_action TEXT NOT NULL DEFAULT 'scan_prefix',
			on_error TEXT NOT NULL DEFAULT 'fail_open',
			updated_at DATETIME DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
		)`,
		`INSERT OR IGNORE INTO privacy_settings (id) VALUES (1)`,
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
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("初始化隐私保护 schema 失败: %w", err)
		}
	}
	return nil
}

func privacyExactSecretsTableExists(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name = 'privacy_exact_secrets'
	`).Scan(&count); err != nil {
		return false, fmt.Errorf("检查隐私保护表失败: %w", err)
	}
	return count > 0, nil
}

func readImportCSV(path string) ([]importRow, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("打开 CSV 失败: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return nil, 0, fmt.Errorf("读取 CSV 表头失败: %w", err)
	}
	index := headerIndex(header)
	if _, ok := index["secret_value"]; !ok {
		return nil, 0, errors.New("CSV 缺少 secret_value 列；可用 --print-template 查看模板")
	}

	var rows []importRow
	seenHashes := map[string]int{}
	skippedEmpty := 0
	for rowNumber := 2; ; rowNumber++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, skippedEmpty, fmt.Errorf("读取 CSV 第 %d 行失败: %w", rowNumber, err)
		}
		if isBlankRecord(record) {
			skippedEmpty++
			continue
		}
		row, err := normalizeCSVRow(record, index, rowNumber)
		if err != nil {
			return nil, skippedEmpty, err
		}
		if firstRow, ok := seenHashes[row.ValueHash]; ok {
			return nil, skippedEmpty, fmt.Errorf("CSV 第 %d 行与第 %d 行敏感值重复", rowNumber, firstRow)
		}
		seenHashes[row.ValueHash] = rowNumber
		rows = append(rows, row)
	}
	return rows, skippedEmpty, nil
}

func headerIndex(header []string) map[string]int {
	index := make(map[string]int, len(header))
	for i, raw := range header {
		key := canonicalHeader(raw)
		if key == "" {
			continue
		}
		if _, exists := index[key]; !exists {
			index[key] = i
		}
	}
	return index
}

func canonicalHeader(raw string) string {
	key := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	switch key {
	case "name", "名称", "名字":
		return "name"
	case "secret_value", "secret", "value", "sensitive_value", "敏感值", "明文":
		return "secret_value"
	case "category", "分类", "类型":
		return "category"
	case "placeholder", "占位符", "替换值":
		return "placeholder"
	case "enabled", "enable", "启用", "是否启用":
		return "enabled"
	case "description", "desc", "描述", "备注":
		return "description"
	case "source_type", "来源类型":
		return "source_type"
	case "source_ref", "source", "来源", "来源标识":
		return "source_ref"
	default:
		return ""
	}
}

func normalizeCSVRow(record []string, index map[string]int, rowNumber int) (importRow, error) {
	get := func(key string) string {
		i, ok := index[key]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}

	category, err := normalizeCategory(get("category"))
	if err != nil {
		return importRow{}, fmt.Errorf("CSV 第 %d 行分类无效: %w", rowNumber, err)
	}
	sourceType, err := normalizeSourceType(get("source_type"))
	if err != nil {
		return importRow{}, fmt.Errorf("CSV 第 %d 行来源类型无效: %w", rowNumber, err)
	}
	enabled, err := parseEnabled(get("enabled"))
	if err != nil {
		return importRow{}, fmt.Errorf("CSV 第 %d 行 enabled 无效: %w", rowNumber, err)
	}

	secretValue := get("secret_value")
	if secretValue == "" {
		return importRow{}, fmt.Errorf("CSV 第 %d 行 secret_value 不能为空", rowNumber)
	}
	if minLength := exactSecretMinLength(category); len(secretValue) < minLength {
		return importRow{}, fmt.Errorf("CSV 第 %d 行 secret_value 过短: category=%s min_length=%d", rowNumber, category, minLength)
	}

	name := get("name")
	if name == "" {
		name = "本地敏感值"
	}
	placeholder := get("placeholder")
	if placeholder == "" {
		placeholder = "[敏感值]"
	}

	return importRow{
		RowNumber:   rowNumber,
		Enabled:     enabled,
		Name:        name,
		SecretValue: secretValue,
		ValueHash:   service.HashPrivacySecretValue(secretValue),
		Placeholder: placeholder,
		Category:    category,
		SourceType:  sourceType,
		SourceRef:   get("source_ref"),
		Description: get("description"),
	}, nil
}

func isBlankRecord(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

func normalizeCategory(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "custom", "自定义":
		return "custom", nil
	case "api_key", "apikey", "api-key", "api", "key", "密钥", "api密钥":
		return "api_key", nil
	case "token", "bearer", "令牌":
		return "token", nil
	case "password", "pwd", "pass", "密码":
		return "password", nil
	default:
		return "", fmt.Errorf("支持 api_key/token/password/custom，got %q", value)
	}
}

func normalizeSourceType(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "imported", "import", "导入":
		return "imported", nil
	case "manual", "手动":
		return "manual", nil
	case "endpoint_token":
		return "endpoint_token", nil
	case "endpoint_api_key":
		return "endpoint_api_key", nil
	case "upstream_account", "account":
		return "upstream_account", nil
	default:
		return "", fmt.Errorf("支持 imported/manual/endpoint_token/endpoint_api_key/upstream_account，got %q", value)
	}
}

func parseEnabled(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "1", "true", "yes", "y", "on", "enabled", "enable", "启用", "是":
		return true, nil
	case "0", "false", "no", "n", "off", "disabled", "disable", "禁用", "否":
		return false, nil
	default:
		return false, fmt.Errorf("支持 true/false/1/0/启用/禁用，got %q", value)
	}
}

func exactSecretMinLength(category string) int {
	switch category {
	case "api_key", "token":
		return 12
	case "password":
		return 8
	default:
		return 4
	}
}

func buildImportPlan(ctx context.Context, db *sql.DB, rows []importRow, upsert bool) (importPlan, error) {
	existing, err := loadExistingSecrets(ctx, db)
	if err != nil {
		return importPlan{}, err
	}
	plan := importPlan{}
	for _, row := range rows {
		current := existing[row.ValueHash]
		if current == nil {
			plan.Inserts = append(plan.Inserts, row)
			continue
		}
		if !upsert {
			plan.SkippedExisting++
			continue
		}
		if sameSecretMetadata(*current, row) {
			plan.SkippedUnchanged++
			continue
		}
		plan.Updates = append(plan.Updates, plannedUpdate{ID: current.ID, Row: row})
	}
	return plan, nil
}

func loadExistingSecrets(ctx context.Context, db *sql.DB) (map[string]*existingSecret, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, enabled, name, secret_value, value_hash, placeholder,
		       category, source_type, source_ref, description
		FROM privacy_exact_secrets
	`)
	if err != nil {
		return nil, fmt.Errorf("读取现有本地敏感值失败: %w", err)
	}
	defer rows.Close()

	out := map[string]*existingSecret{}
	for rows.Next() {
		record := &existingSecret{}
		if err := rows.Scan(
			&record.ID, &record.Enabled, &record.Name, &record.SecretValue,
			&record.ValueHash, &record.Placeholder, &record.Category, &record.SourceType,
			&record.SourceRef, &record.Description,
		); err != nil {
			return nil, fmt.Errorf("扫描现有本地敏感值失败: %w", err)
		}
		out[record.ValueHash] = record
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历现有本地敏感值失败: %w", err)
	}
	return out, nil
}

func sameSecretMetadata(existing existingSecret, row importRow) bool {
	return existing.Enabled == row.Enabled &&
		existing.Name == row.Name &&
		existing.SecretValue == row.SecretValue &&
		existing.Placeholder == row.Placeholder &&
		existing.Category == row.Category &&
		existing.SourceType == row.SourceType &&
		existing.SourceRef == row.SourceRef &&
		existing.Description == row.Description
}

func applyImportPlan(ctx context.Context, db *sql.DB, plan importPlan) error {
	if len(plan.Inserts) == 0 && len(plan.Updates) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始导入事务失败: %w", err)
	}
	defer tx.Rollback()

	for _, row := range plan.Inserts {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO privacy_exact_secrets (
				enabled, name, secret_value, value_hash, placeholder,
				category, source_type, source_ref, description
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, row.Enabled, row.Name, row.SecretValue, row.ValueHash, row.Placeholder,
			row.Category, row.SourceType, row.SourceRef, row.Description); err != nil {
			return fmt.Errorf("插入 CSV 第 %d 行失败: %w", row.RowNumber, err)
		}
	}

	for _, update := range plan.Updates {
		row := update.Row
		result, err := tx.ExecContext(ctx, `
			UPDATE privacy_exact_secrets
			SET enabled = ?, name = ?, secret_value = ?, value_hash = ?, placeholder = ?,
			    category = ?, source_type = ?, source_ref = ?, description = ?,
			    updated_at = strftime('%Y-%m-%dT%H:%M:%f000Z', 'now')
			WHERE id = ?
		`, row.Enabled, row.Name, row.SecretValue, row.ValueHash, row.Placeholder,
			row.Category, row.SourceType, row.SourceRef, row.Description, update.ID)
		if err != nil {
			return fmt.Errorf("更新 CSV 第 %d 行失败: %w", row.RowNumber, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("读取 CSV 第 %d 行更新结果失败: %w", row.RowNumber, err)
		}
		if affected == 0 {
			return fmt.Errorf("更新 CSV 第 %d 行失败: 目标记录不存在", row.RowNumber)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交导入事务失败: %w", err)
	}
	return nil
}

func printPlan(out io.Writer, csvPath, dbPath string, plan importPlan, dryRun bool) {
	mode := "执行"
	if dryRun {
		mode = "预览"
	}
	fmt.Fprintf(out, "模式: %s\n", mode)
	fmt.Fprintf(out, "CSV: %s\n", csvPath)
	fmt.Fprintf(out, "数据库: %s\n", dbPath)
	fmt.Fprintf(out, "有效行: %d", plan.Rows)
	if plan.SkippedEmptyLines > 0 {
		fmt.Fprintf(out, "（跳过空行 %d）", plan.SkippedEmptyLines)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "新增: %d\n", len(plan.Inserts))
	fmt.Fprintf(out, "更新: %d\n", len(plan.Updates))
	fmt.Fprintf(out, "跳过未变化: %d\n", plan.SkippedUnchanged)
	if plan.SkippedExisting > 0 {
		fmt.Fprintf(out, "跳过已存在: %d\n", plan.SkippedExisting)
	}
}
