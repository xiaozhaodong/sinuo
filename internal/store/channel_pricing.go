package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// BillingChannel 是实际出站目标。端点和账号按数据库 ID 匹配，订阅直通按固定名称匹配。
type BillingChannel struct {
	Type                string `json:"type"`
	SubscriptionAccount bool   `json:"subscription_account,omitempty"`
	ID                  int64  `json:"id"`
	Name                string `json:"name"`
}

func (c BillingChannel) Key() BillingChannel {
	c.SubscriptionAccount = false
	if c.Type != "subscription" {
		c.Name = ""
	}
	return c
}

type ChannelRuleVersion struct {
	ID      int64
	Channel BillingChannel
	Model   string
	Rule    ChannelModelRule
	Invalid bool // 保留规则版本的位置，损坏规则不能回退到旧规则或官方倍率。
}

func decodeChannelRule(raw string) (ChannelModelRule, error) {
	var rule *ChannelModelRule
	if err := json.Unmarshal([]byte(raw), &rule); err != nil {
		return ChannelModelRule{}, err
	}
	if rule == nil {
		return ChannelModelRule{}, fmt.Errorf("渠道模型规则必须是对象")
	}
	return *rule, nil
}

// ValidateForRate 使用该次调用的价表校验规则，历史规则可能引用已经移除的时段。
func (r ChannelModelRule) ValidateForRate(sheet *RateSheet) error {
	if sheet == nil {
		sheet = &RateSheet{}
	}
	return r.validate(sheet)
}

func (s *SQLiteModelPricingStore) ListChannelRules(ctx context.Context) ([]ChannelRuleVersion, error) {
	if s.tx == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	rows, err := s.getQuerier().QueryContext(ctx, "SELECT id,channel_type,channel_id,channel_name,model_name,rule FROM channel_model_rules ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ChannelRuleVersion
	for rows.Next() {
		var v ChannelRuleVersion
		var raw string
		err := rows.Scan(&v.ID, &v.Channel.Type, &v.Channel.ID, &v.Channel.Name, &v.Model, &raw)
		if err == nil {
			v.Rule, err = decodeChannelRule(raw)
		}
		if err != nil {
			v.Invalid = true
			slog.WarnContext(ctx, "渠道模型规则不可用", "channel", v.Channel, "model", v.Model, "rule_id", v.ID, "error", err)
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

func (s *SQLiteModelPricingStore) CurrentRateSheet(ctx context.Context, model string) (*RateSheet, error) {
	r, err := s.CurrentBaseRate(ctx, model)
	return r.Sheet, err
}
func (s *SQLiteModelPricingStore) CurrentChannelRule(ctx context.Context, c BillingChannel, model string) (ChannelModelRule, error) {
	if s.tx == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	c = c.Key()
	var raw string
	var rule ChannelModelRule
	err := s.getQuerier().QueryRowContext(ctx, `SELECT rule FROM channel_model_rules WHERE channel_type=? AND channel_id=? AND channel_name=? AND model_name=? ORDER BY id DESC LIMIT 1`, c.Type, c.ID, c.Name, model).Scan(&raw)
	if err == sql.ErrNoRows {
		return rule, nil
	}
	if err != nil {
		return rule, err
	}
	return decodeChannelRule(raw)
}

// CurrentChannelRules 只展示仍在价表中的模型。先选最新版本，再排除已恢复跟随官方的规则。
func (s *SQLiteModelPricingStore) CurrentChannelRules(ctx context.Context, c BillingChannel) (map[string]ChannelModelRule, error) {
	if s.tx == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	c = c.Key()
	rows, err := s.getQuerier().QueryContext(ctx, `SELECT r.model_name, r.rule FROM channel_model_rules r
		JOIN model_pricing p ON p.model_name = r.model_name
		WHERE r.id IN (SELECT MAX(id) FROM channel_model_rules
			WHERE channel_type=? AND channel_id=? AND channel_name=? GROUP BY model_name)`, c.Type, c.ID, c.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make(map[string]ChannelModelRule)
	for rows.Next() {
		var model, raw string
		if err := rows.Scan(&model, &raw); err != nil {
			return nil, err
		}
		rule, err := decodeChannelRule(raw)
		if err != nil {
			return nil, fmt.Errorf("%s 渠道规则无法读取: %w", model, err)
		}
		if rule.FastMultiplier != nil || rule.Period != "" || slices.ContainsFunc(rule.Tiers, func(t ChannelTier) bool {
			return t.Multiplier != nil || t.Threshold != nil
		}) {
			rules[model] = rule
		}
	}
	return rules, rows.Err()
}

func (s *SQLiteModelPricingStore) SaveChannelModelRule(ctx context.Context, c BillingChannel, model string, r ChannelModelRule) error {
	if s.tx == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	c = c.Key()
	switch c.Type {
	case "subscription":
		if c.ID != 0 || (c.Name != "claude-subscription" && c.Name != "chatgpt-subscription") {
			return fmt.Errorf("无效的订阅渠道")
		}
	case "endpoint", "account":
		if c.ID <= 0 {
			return fmt.Errorf("渠道 ID 必须为正数")
		}
	default:
		return fmt.Errorf("未知渠道类型 %q", c.Type)
	}
	return s.writeTransaction(ctx, func(tx *sql.Tx) error {
		sheet, err := s.WithTx(tx).CurrentRateSheet(ctx, model)
		if err == ErrModelRateMissing && r.Period == "" && r.FastMultiplier == nil && len(r.Tiers) == 0 {
			err = nil
		}
		if err != nil {
			return err
		}
		if err := r.ValidateForRate(sheet); err != nil {
			return err
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO channel_model_rules(channel_type,channel_id,channel_name,model_name,rule) VALUES(?,?,?,?,?)`, c.Type, c.ID, c.Name, model, string(raw))
		return err
	})
}

func validateActiveChannelRules(ctx context.Context, tx *sql.Tx, model string, sheet *RateSheet) error {
	rules, err := tx.QueryContext(ctx, `SELECT r.rule FROM channel_model_rules AS r
		WHERE r.id IN (SELECT MAX(id) FROM channel_model_rules WHERE model_name=? GROUP BY channel_type,channel_id,channel_name)
		AND (r.channel_type='subscription'
			OR (r.channel_type='endpoint' AND EXISTS (SELECT 1 FROM endpoints e WHERE e.id=r.channel_id))
			OR (r.channel_type='account' AND EXISTS (SELECT 1 FROM upstream_accounts a WHERE a.id=r.channel_id)))`, model)
	if err != nil {
		return err
	}
	for rules.Next() {
		var v ChannelModelRule
		var data string
		if err = rules.Scan(&data); err == nil {
			v, err = decodeChannelRule(data)
		}
		if err == nil {
			err = v.ValidateForRate(sheet)
		}
		if err != nil {
			rules.Close()
			return fmt.Errorf("已有渠道规则与新价表不兼容: %w", err)
		}
	}
	if err = rules.Err(); err != nil {
		rules.Close()
		return err
	}
	return rules.Close()
}

// PublishRateSheet 在同一事务里更新基础价展示、关闭旧版本并保存整张只读价表。
func (s *SQLiteModelPricingStore) PublishRateSheet(ctx context.Context, model, source string, sheet RateSheet) error {
	if s.tx == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	if model == "" || strings.TrimSpace(model) != model || (source != RateSourceOfficial && source != RateSourceCustom) {
		return fmt.Errorf("模型或价表来源无效")
	}
	if err := sheet.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(sheet)
	if err != nil {
		return err
	}
	period := ""
	if len(sheet.Periods) > 0 {
		period = sheet.Periods[0].Name
	}
	_, base := sheet.Row(period, 0, false)
	return s.writeTransaction(ctx, func(tx *sql.Tx) error {
		if err := validateActiveChannelRules(ctx, tx, model, &sheet); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO model_pricing(model_name,input_price,output_price,cache_creation_price_5m,cache_creation_price_1h,cache_read_price) VALUES(?,?,?,COALESCE(?,0),COALESCE(?,0),COALESCE(?,0))
            ON CONFLICT(model_name) DO UPDATE SET input_price=excluded.input_price,output_price=excluded.output_price,cache_creation_price_5m=excluded.cache_creation_price_5m,cache_creation_price_1h=excluded.cache_creation_price_1h,cache_read_price=excluded.cache_read_price`, model, base.Input, base.Output, base.Cache5m, base.Cache1h, base.CacheRead)
		if err != nil {
			return err
		}
		now := time.Now().UnixNano()
		if _, err = tx.ExecContext(ctx, closeBaseRateSQL, now, model); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO model_base_rates(model_name,source,effective_from_ns,input_price,output_price,cache_creation_price_5m,cache_creation_price_1h,cache_read_price) VALUES(?1,?2,MAX(?3,COALESCE((SELECT MAX(effective_until_ns) FROM model_base_rates WHERE model_name=?1),0)),?4,?5,?6,?7,?8)`, model, source, now, base.Input, base.Output, base.Cache5m, base.Cache1h, base.CacheRead)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO model_rate_sheets(version_id,sheet) VALUES(?,?)", id, string(raw))
		return err
	})
}

// ReadPricingCatalog 让渠道规则和价表来自同一个 SQLite 读快照。
func (s *SQLiteModelPricingStore) ReadPricingCatalog(ctx context.Context) (rates []BaseRateVersion, rules []ChannelRuleVersion, err error) {
	if s.tx == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	err = s.writeTransaction(ctx, func(tx *sql.Tx) error {
		view := s.WithTx(tx)
		rates, err = view.ListBaseRates(ctx)
		if err != nil {
			return err
		}
		rules, err = view.ListChannelRules(ctx)
		return err
	})
	return
}

func (s *SQLiteModelPricingStore) SeedRateSheet(ctx context.Context, model string, sheet RateSheet) error {
	if s.tx == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	return s.writeTransaction(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM model_base_rates WHERE model_name=?1) OR EXISTS(SELECT 1 FROM model_pricing WHERE model_name=?1)`, model).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		return s.WithTx(tx).PublishRateSheet(ctx, model, RateSourceOfficial, sheet)
	})
}

// SeedResponsesRateSheet 只升级仍精确匹配内置官方平价的版本，保留历史、自定义价和已发布的价表。
func (s *SQLiteModelPricingStore) SeedResponsesRateSheet(ctx context.Context, defaults *ModelPricingRecord, sheet RateSheet) (bool, error) {
	if s.tx == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	inserted := false
	err := s.writeTransaction(ctx, func(tx *sql.Tx) error {
		var exists, matches bool
		err := tx.QueryRowContext(ctx, `SELECT
			EXISTS(SELECT 1 FROM model_pricing WHERE model_name=?1),
			EXISTS(SELECT 1 FROM model_base_rates r WHERE r.model_name=?1 AND r.effective_until_ns IS NULL
			  AND r.source='official' AND NOT EXISTS(SELECT 1 FROM model_rate_sheets WHERE version_id=r.id)
			  AND input_price=?2 AND output_price=?3 AND cache_creation_price_5m=?4 AND cache_creation_price_1h=?5 AND cache_read_price=?6)`,
			defaults.ModelName, defaults.InputPrice, defaults.OutputPrice, defaults.CacheCreationPrice5m, defaults.CacheCreationPrice1h, defaults.CacheReadPrice).Scan(&exists, &matches)
		if err != nil || (exists && !matches) {
			return err
		}
		if err := s.WithTx(tx).PublishRateSheet(ctx, defaults.ModelName, RateSourceOfficial, sheet); err != nil {
			return err
		}
		if !exists {
			_, err = tx.ExecContext(ctx, "UPDATE model_pricing SET display_name=?, description=? WHERE model_name=?", defaults.DisplayName, defaults.Description, defaults.ModelName)
			inserted = err == nil
		}
		return err
	})
	return inserted && err == nil, err
}
