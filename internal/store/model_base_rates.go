package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	RateSourceOfficial = "official"
	RateSourceCustom   = "custom"

	insertPricingSQL = `INSERT INTO model_pricing (model_name, input_price, output_price,
		cache_creation_price_5m, cache_creation_price_1h, cache_read_price, display_name, description, is_default)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)`
	updatePricingSQL = `UPDATE model_pricing SET input_price=?2, output_price=?3, cache_creation_price_5m=?4,
		cache_creation_price_1h=?5, cache_read_price=?6, display_name=?7, description=?8, is_default=?9 WHERE model_name=?1`
	updatePricingMetadataSQL = `UPDATE model_pricing SET display_name=?, description=?, is_default=? WHERE model_name=?`
	upsertPricingSQL         = insertPricingSQL + ` ON CONFLICT(model_name) DO UPDATE SET input_price=excluded.input_price,
		output_price=excluded.output_price, cache_creation_price_5m=excluded.cache_creation_price_5m,
		cache_creation_price_1h=excluded.cache_creation_price_1h, cache_read_price=excluded.cache_read_price,
		display_name=excluded.display_name, description=excluded.description, is_default=excluded.is_default`
	closeBaseRateSQL = `UPDATE model_base_rates SET effective_until_ns=MAX(?, effective_from_ns+1)
		WHERE model_name=? AND effective_until_ns IS NULL`
)

// BaseRateVersion 是一条已发布的基础费率。旧库允许缓存单价为 NULL，保留其未知状态。
type BaseRateVersion struct {
	Invalid                     bool // 保留损坏版本的 ID 和区间，避免改用其他版本结算。
	Sheet                       *RateSheet
	ID                          int64
	ModelName, Source           string
	FromNS                      int64
	UntilNS                     sql.NullInt64
	Input, Output               float64
	Cache5m, Cache1h, CacheRead sql.NullFloat64
}

const selectBaseRate = `SELECT r.id,r.model_name,r.source,r.effective_from_ns,r.effective_until_ns,
    r.input_price,r.output_price,r.cache_creation_price_5m,r.cache_creation_price_1h,r.cache_read_price,s.sheet
    FROM model_base_rates r LEFT JOIN model_rate_sheets s ON s.version_id=r.id`

// BillingCatalog 包含同一读快照中的费率、渠道规则和当前默认模型。
type BillingCatalog struct {
	Rates        []BaseRateVersion
	Rules        []ChannelRuleVersion
	DefaultModel string
}

func (s *SQLiteModelPricingStore) ReadBillingCatalog(ctx context.Context) (BillingCatalog, error) {
	if s.tx == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	var catalog BillingCatalog
	err := s.writeTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		catalog.Rates, catalog.Rules, err = s.WithTx(tx).ReadPricingCatalog(ctx)
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT COALESCE(
			(SELECT model_name FROM model_pricing WHERE is_default=1 LIMIT 1), '_default')`).Scan(&catalog.DefaultModel)
	})
	return catalog, err
}

// BuiltinDefaultPricing 供首次初始化和没有配置默认价格的旧库使用。
func BuiltinDefaultPricing() *ModelPricingRecord {
	return &ModelPricingRecord{
		ModelName: "_default", DisplayName: "默认定价", IsDefault: true,
		InputPrice: 3, OutputPrice: 15, CacheCreationPrice5m: 3.75,
		CacheCreationPrice1h: 6, CacheReadPrice: 0.30,
	}
}

// ParseRateSheet 对目录加载与历史官方费用复算使用相同的价表校验。
func ParseRateSheet(raw string) (*RateSheet, error) {
	var sheet RateSheet
	if err := json.Unmarshal([]byte(raw), &sheet); err != nil {
		return nil, err
	}
	if err := sheet.validate(); err != nil {
		return nil, err
	}
	return &sheet, nil
}

// ValidatePrices 校验版本的基础单价，包括从 SQLite 读出的缓存价。
func (r BaseRateVersion) ValidatePrices() error {
	for _, price := range []float64{r.Input, r.Output, r.Cache5m.Float64, r.Cache1h.Float64, r.CacheRead.Float64} {
		if !validAmount(&price) {
			return fmt.Errorf("基础单价必须为非负有限数")
		}
	}
	return nil
}

func scanBaseRate(row interface{ Scan(...any) error }) (BaseRateVersion, error) {
	var rate BaseRateVersion
	var raw sql.NullString
	err := row.Scan(&rate.ID, &rate.ModelName, &rate.Source, &rate.FromNS, &rate.UntilNS, &rate.Input, &rate.Output, &rate.Cache5m, &rate.Cache1h, &rate.CacheRead, &raw)
	if err != nil {
		return rate, err
	}
	if raw.Valid {
		rate.Sheet, err = ParseRateSheet(raw.String)
		if err != nil {
			return rate, err
		}
	} else if rate.Source == "legacy" && rate.Cache1h.Valid && rate.Cache1h.Float64 <= 0 {
		// 旧入口将非正 1h 价格按输入价的两倍计费，已迁移的旧行也使用该口径。
		rate.Cache1h.Float64 = rate.Input * 2
	}
	return rate, rate.ValidatePrices()
}

var ErrModelRateMissing = errors.New("该模型尚未配置价表，请先在基础定价页添加")

func (s *SQLiteModelPricingStore) CurrentBaseRate(ctx context.Context, model string) (BaseRateVersion, error) {
	if s.tx == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	rate, err := scanBaseRate(s.getQuerier().QueryRowContext(ctx, selectBaseRate+" WHERE r.model_name=? AND r.effective_until_ns IS NULL", model))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrModelRateMissing
	}
	return rate, err
}

func (s *SQLiteModelPricingStore) ListBaseRates(ctx context.Context) ([]BaseRateVersion, error) {
	if s.tx == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	rows, err := s.getQuerier().QueryContext(ctx, selectBaseRate+" ORDER BY r.model_name,r.effective_from_ns")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rates []BaseRateVersion
	for rows.Next() {
		r, err := scanBaseRate(rows)
		if err != nil {
			r.Invalid = true
			slog.WarnContext(ctx, "模型费率版本不可用", "model", r.ModelName, "version_id", r.ID, "error", err)
		}
		rates = append(rates, r)
	}
	return rates, rows.Err()
}

func (s *SQLiteModelPricingStore) writeTransaction(ctx context.Context, fn func(*sql.Tx) error) error {
	if s.tx != nil {
		return fn(s.tx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteModelPricingStore) writePricingBatch(ctx context.Context, query string, records []*ModelPricingRecord) error {
	if s.tx == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	return s.writeTransaction(ctx, func(tx *sql.Tx) error {
		var defaultRecord *ModelPricingRecord
		for _, record := range records {
			if record != nil && record.IsDefault {
				if defaultRecord != nil && defaultRecord.ModelName != record.ModelName {
					return fmt.Errorf("批量写入只能指定一个默认模型定价")
				}
				defaultRecord = record
			}
			if _, err := writePricing(ctx, tx, query, record); err != nil {
				return err
			}
		}
		if defaultRecord != nil {
			return clearOtherPricingDefaults(ctx, tx, defaultRecord)
		}
		return nil
	})
}

func requirePricingRow(result sql.Result, name string) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("模型定价不存在: %s", name)
	}
	return nil
}

// writePricing 在同一写事务中保存表单并发布费率。只改展示信息不产生新版本。
func writePricing(ctx context.Context, tx *sql.Tx, query string, r *ModelPricingRecord) (sql.Result, error) {
	if r == nil || r.ModelName == "" {
		return nil, fmt.Errorf("模型名称不能为空")
	}
	if r.MetadataOnly {
		if query != updatePricingSQL {
			return nil, fmt.Errorf("只修改展示信息时必须使用更新接口")
		}
		result, err := tx.ExecContext(ctx, updatePricingMetadataSQL, r.DisplayName, r.Description, boolToInt(r.IsDefault), r.ModelName)
		if err != nil {
			return nil, err
		}
		return result, requirePricingRow(result, r.ModelName)
	}
	if r.RateSource != "" && r.RateSource != RateSourceOfficial && r.RateSource != RateSourceCustom {
		return nil, fmt.Errorf("未知的价格来源 %q", r.RateSource)
	}
	for _, price := range []float64{r.InputPrice, r.OutputPrice, r.CacheCreationPrice5m, r.CacheCreationPrice1h, r.CacheReadPrice} {
		if !validAmount(&price) {
			return nil, fmt.Errorf("模型 %s 的单价必须为非负有限数", r.ModelName)
		}
	}

	// 普通基础价按费率版本去重，NULL 与显式零价不同。结构化价表的表单只编辑元信息，单价必须匹配其展示值。
	var activeSheet, unchanged bool
	err := tx.QueryRowContext(ctx, `SELECT s.version_id IS NOT NULL,
		(?7='' OR r.source=?7) AND CASE WHEN s.version_id IS NULL THEN
			(r.input_price, r.output_price, r.cache_creation_price_5m, r.cache_creation_price_1h, r.cache_read_price) IS (?2, ?3, ?4, ?5, ?6)
		ELSE
			(p.input_price, p.output_price, p.cache_creation_price_5m, p.cache_creation_price_1h, p.cache_read_price) IS (?2, ?3, ?4, ?5, ?6)
		END
		FROM model_base_rates r
		LEFT JOIN model_rate_sheets s ON s.version_id=r.id
		LEFT JOIN model_pricing p ON p.model_name=r.model_name
		WHERE r.model_name=?1 AND r.effective_until_ns IS NULL`, r.ModelName, r.InputPrice, r.OutputPrice, r.CacheCreationPrice5m,
		r.CacheCreationPrice1h, r.CacheReadPrice, r.RateSource).Scan(&activeSheet, &unchanged)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if activeSheet && !unchanged {
		return nil, fmt.Errorf("模型 %s 使用分档价表，请通过价表发布接口调整单价", r.ModelName)
	}
	if !unchanged {
		if err := validateActiveChannelRules(ctx, tx, r.ModelName, nil); err != nil {
			return nil, err
		}
	}

	result, err := tx.ExecContext(ctx, query, r.ModelName, r.InputPrice, r.OutputPrice, r.CacheCreationPrice5m,
		r.CacheCreationPrice1h, r.CacheReadPrice, r.DisplayName, r.Description, boolToInt(r.IsDefault))
	if err != nil {
		return nil, err
	}
	if err := requirePricingRow(result, r.ModelName); err != nil {
		return nil, err
	}
	if unchanged {
		return result, nil
	}
	now := time.Now().UnixNano()
	if _, err := tx.ExecContext(ctx, closeBaseRateSQL, now, r.ModelName); err != nil {
		return nil, err
	}
	source := r.RateSource
	if source == "" {
		source = RateSourceCustom
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO model_base_rates (model_name, source, effective_from_ns,
		input_price, output_price, cache_creation_price_5m, cache_creation_price_1h, cache_read_price)
		VALUES (?1, ?2, MAX(?3, COALESCE((SELECT MAX(effective_until_ns) FROM model_base_rates WHERE model_name=?1), 0)),
		?4, ?5, ?6, ?7, ?8)`, r.ModelName, source, now, r.InputPrice, r.OutputPrice, r.CacheCreationPrice5m, r.CacheCreationPrice1h, r.CacheReadPrice)
	return result, err
}
