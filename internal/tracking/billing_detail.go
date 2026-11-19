package tracking

import (
	"database/sql"
	"encoding/json"

	"sinuo/internal/store"
)

// AppliedBillingMultipliers 表示当次结算的有效渠道倍率。分项模式只展示有用量的分项。
type AppliedBillingMultipliers struct {
	Unified          float64 `json:"unified,omitempty"`
	Input            float64 `json:"input,omitempty"`
	Output           float64 `json:"output,omitempty"`
	CacheRead        float64 `json:"cache_read,omitempty"`
	CacheCreation5m  float64 `json:"cache_creation_5m,omitempty"`
	CacheCreation1h  float64 `json:"cache_creation_1h,omitempty"`
	CacheCreation30m float64 `json:"cache_creation_30m,omitempty"`
	CacheCreation12h float64 `json:"cache_creation_12h,omitempty"`
}

func (m EndpointMultiplier) effective() EndpointMultiplier {
	if m.CostMultiplier > 0 {
		return m
	}
	defaultUnset := func(v float64) float64 {
		if v <= 0 {
			return 1
		}
		return v
	}
	m.InputCostMultiplier = defaultUnset(m.InputCostMultiplier)
	m.OutputCostMultiplier = defaultUnset(m.OutputCostMultiplier)
	m.CacheReadCostMultiplier = defaultUnset(m.CacheReadCostMultiplier)
	m.CacheCreationCostMultiplier = defaultUnset(m.CacheCreationCostMultiplier)
	m.CacheCreationCostMultiplier1h = defaultUnset(m.CacheCreationCostMultiplier1h)
	return m
}

func (m EndpointMultiplier) isZeroMultipliers() bool {
	return m.CostMultiplier == 0 &&
		m.InputCostMultiplier == 0 &&
		m.OutputCostMultiplier == 0 &&
		m.CacheCreationCostMultiplier == 0 &&
		m.CacheCreationCostMultiplier1h == 0 &&
		m.CacheReadCostMultiplier == 0
}

func appliedBillingMultipliers(m *EndpointMultiplier, usage *TokenUsage, path string, d *BillingDecision) *AppliedBillingMultipliers {
	if m == nil || (d != nil && d.Channel.Type == "subscription") {
		return nil
	}
	if m.isZeroMultipliers() {
		return &AppliedBillingMultipliers{Unified: 1}
	}
	if m.CostMultiplier > 0 {
		return &AppliedBillingMultipliers{Unified: m.CostMultiplier}
	}
	if usage == nil {
		return nil
	}
	effective := m.effective()
	applied := AppliedBillingMultipliers{}
	includesCache := usage.IncludesCacheInInput(path)
	separateWrites := d != nil && d.SeparateCacheWrites
	if usage.billableInputTokens(includesCache, separateWrites) > 0 {
		applied.Input = effective.InputCostMultiplier
	}
	if usage.OutputTokens > 0 {
		applied.Output = effective.OutputCostMultiplier
	}
	if usage.CacheReadTokens > 0 {
		applied.CacheRead = effective.CacheReadCostMultiplier
	}
	if !includesCache {
		fiveMinutes, oneHour := usage.claudeCacheWriteTokens()
		if fiveMinutes > 0 {
			applied.CacheCreation5m = effective.CacheCreationCostMultiplier
		}
		if oneHour > 0 {
			applied.CacheCreation1h = effective.CacheCreationCostMultiplier1h
		}
	} else if separateWrites {
		if usage.CacheCreation30mTokens != nil && *usage.CacheCreation30mTokens > 0 {
			applied.CacheCreation30m = effective.CacheCreationCostMultiplier
		}
		if usage.CacheCreation12hTokens != nil && *usage.CacheCreation12hTokens > 0 {
			applied.CacheCreation12h = effective.CacheCreationCostMultiplier
		}
	}
	if applied == (AppliedBillingMultipliers{}) {
		return nil
	}
	return &applied
}

type archivedBillingFacts struct {
	Decision     *BillingDecision
	Multiplier   *EndpointMultiplier
	invalidBasis bool
	usageKnown   bool
	rate         archivedRate
}

func (b *archivedBillingFacts) readBasis(raw string) error {
	if raw == "" {
		return nil
	}
	var saved struct {
		Decision   *BillingDecision `json:"decision"`
		Multiplier json.RawMessage  `json:"multiplier"`
	}
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		b.invalidBasis = true
		return err
	}
	b.Decision = saved.Decision
	if len(saved.Multiplier) == 0 {
		return nil
	}
	// 没有保存总倍率字段时，无法确认当时选了统一模式还是分项模式。
	var original struct {
		EndpointMultiplier
		CostMultiplier *float64
	}
	if err := json.Unmarshal(saved.Multiplier, &original); err != nil {
		return err
	}
	if original.CostMultiplier != nil {
		original.EndpointMultiplier.CostMultiplier = *original.CostMultiplier
		b.Multiplier = &original.EndpointMultiplier
	}
	return nil
}

func (b archivedBillingFacts) officialFacts(status string, ended bool, path string, rateID int64, usage *TokenUsage) officialBillingFacts {
	// 旧版终态行可以没有 end_time。
	settled := ended
	if !settled {
		switch status {
		case "completed", "cancelled", "failed", "error", "auth_error", "rate_limited", "server_error", "network_error", "stream_error", "timeout":
			settled = true
		}
	}
	if !b.usageKnown {
		usage = nil
	}
	return officialBillingFacts{settled: settled, invalidBasis: b.invalidBasis, path: path, usage: usage,
		rateID: rateID, rate: b.rate.version(), decision: b.Decision}
}

// 查询单条请求详情时在同一 SELECT 读取原价表。别名避免与请求基础列冲突。
const archivedRateJoin = ` LEFT JOIN (
	SELECT id AS rate_id, source, input_price, output_price, cache_creation_price_5m,
	       cache_creation_price_1h, cache_read_price FROM model_base_rates
) r ON r.rate_id=q.billing_rate_id LEFT JOIN model_rate_sheets s ON s.version_id=r.rate_id`
const archivedRateColumns = `r.source,r.input_price,r.output_price,r.cache_creation_price_5m,r.cache_creation_price_1h,r.cache_read_price,s.sheet`

type archivedRate struct {
	source, sheet sql.NullString
	prices        [5]any
}

func (r *archivedRate) dest() []any {
	return []any{&r.source, &r.prices[0], &r.prices[1], &r.prices[2], &r.prices[3], &r.prices[4], &r.sheet}
}

func (r archivedRate) version() store.BaseRateVersion {
	rate := store.BaseRateVersion{Source: r.source.String}
	if !r.source.Valid || rate.Source != store.RateSourceOfficial {
		return rate
	}
	var input, output sql.NullFloat64
	for i, target := range []*sql.NullFloat64{&input, &output, &rate.Cache5m, &rate.Cache1h, &rate.CacheRead} {
		if err := target.Scan(r.prices[i]); err != nil {
			rate.Invalid = true
			return rate
		}
	}
	rate.Input, rate.Output = input.Float64, output.Float64
	rate.Invalid = !input.Valid || !output.Valid
	if !rate.Invalid && r.sheet.Valid {
		var err error
		rate.Sheet, err = store.ParseRateSheet(r.sheet.String)
		rate.Invalid = err != nil
	}
	return rate
}
