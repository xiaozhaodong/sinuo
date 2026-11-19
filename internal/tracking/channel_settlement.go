package tracking

import (
	"database/sql"
	"math"
	"sort"
	"strings"
	"time"

	"sinuo/internal/store"
)

// BillingDecision 留下选行事实。行号从 1 起，与 billing_rate_id 组成不可变引用。
type BillingDecision struct {
	Channel             store.BillingChannel `json:"channel"`
	SentAtNS            int64                `json:"sent_at_ns,omitempty"`
	RequestedSpeed      string               `json:"requested_speed,omitempty"`
	ResponseSpeed       string               `json:"response_speed,omitempty"`
	Speed               string               `json:"speed,omitempty"`
	SeparateCacheWrites bool                 `json:"separate_cache_writes,omitempty"`
	Estimated           bool                 `json:"estimated,omitempty"`
	FullInput           int64                `json:"full_input"`
	RuleID              int64                `json:"rule_id,omitempty"`
	ChannelRow          int                  `json:"channel_row,omitempty"`
	OfficialRow         int                  `json:"official_row,omitempty"`
	ChannelPeriod       string               `json:"channel_period,omitempty"`
	OfficialPeriod      string               `json:"official_period,omitempty"`
	ChannelTier         int                  `json:"channel_tier"`
	OfficialTier        int                  `json:"official_tier"`
	Factor              float64              `json:"model_multiplier"`
	Missing             string               `json:"missing,omitempty"`
}
type channelModelKey struct {
	channel store.BillingChannel
	model   string
}

func speedValue(reasoning string) string {
	for _, part := range strings.Fields(reasoning) {
		if v, ok := strings.CutPrefix(part, "speed:"); ok {
			return v
		}
	}
	return ""
}
func (d *BillingDecision) resolveSpeed() {
	sub := d.Channel.SubscriptionAccount || (d.Channel.Type == "subscription" && d.Channel.Name == "chatgpt-subscription")
	if sub && (d.ResponseSpeed == "" || d.ResponseSpeed == "default" || d.ResponseSpeed == "auto") {
		if d.RequestedSpeed == "priority" || d.RequestedSpeed == "fast" {
			d.Speed = "fast"
			d.Estimated = true
			return
		}
		if d.ResponseSpeed == "default" && (d.RequestedSpeed == "" || d.RequestedSpeed == "default" || d.RequestedSpeed == "auto" || d.RequestedSpeed == "standard") {
			d.Speed = "standard"
			d.Estimated = true
			return
		}
		return
	}
	switch d.ResponseSpeed {
	case "fast", "priority":
		d.Speed = "fast"
	case "default", "standard":
		d.Speed = "standard"
	}
}
func (d *BillingDecision) unsupportedResponseSpeed() bool {
	return d.Speed == "" && d.ResponseSpeed != "" && d.ResponseSpeed != "auto" && d.ResponseSpeed != "default"
}
func (v *baseRateCatalog) channelRule(c store.BillingChannel, model string, watermark int64) store.ChannelRuleVersion {
	rows := v.rules[channelModelKey{c.Key(), model}]
	i := sort.Search(len(rows), func(i int) bool { return rows[i].ID > watermark })
	if i == 0 {
		return store.ChannelRuleVersion{}
	}
	return rows[i-1]
}
func nullablePrice(v *float64) sql.NullFloat64 {
	if v == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *v, Valid: true}
}
func pricesForRow(base store.BaseRateVersion, r *store.RateRow) store.BaseRateVersion {
	base.Input, base.Output = *r.Input, *r.Output
	base.Cache5m, base.Cache1h, base.CacheRead = nullablePrice(r.Cache5m), nullablePrice(r.Cache1h), nullablePrice(r.CacheRead)
	return base
}

type selectedPrice struct {
	row    int
	rate   store.BaseRateVersion
	factor float64
}

func selectPrice(base store.BaseRateVersion, period string, tier int, fast bool, rule store.ChannelModelRule) selectedPrice {
	factor := 1.0
	if fast && rule.FastMultiplier != nil {
		fast = false
		factor = *rule.FastMultiplier
	}
	if tier > 0 && tier <= len(rule.Tiers) && rule.Tiers[tier-1].Multiplier != nil {
		factor *= *rule.Tiers[tier-1].Multiplier
		tier = 0
	}
	result := selectedPrice{factor: factor}
	if base.Sheet == nil {
		if tier == 0 {
			result.row = 1
			result.rate = base
		}
		return result
	}
	// 只有标准行的价表没有速度维度。渠道仍可显式增加 Fast 倍率。
	if fast && !base.Sheet.HasFast() {
		fast = false
	}
	i, r := base.Sheet.Row(period, tier, fast)
	if r != nil {
		result.row = i
		result.rate = pricesForRow(base, r)
	}
	return result
}
func (p selectedPrice) charge(u *TokenUsage, path string, m EndpointMultiplier, separateWrites bool) (CostBreakdown, string) {
	if u == nil {
		return CostBreakdown{}, "缺少计费用量"
	}
	if p.factor == 0 {
		return CostBreakdown{}, ""
	}
	prices, missing := p.pricesForUsage(*u, path, separateWrites)
	if missing != "" {
		return CostBreakdown{}, missing
	}
	cost := calculateAtRate(u, &prices, &m, path)
	if !finiteBillingCost(cost.TotalCost) {
		return CostBreakdown{}, "费用超出数值范围"
	}
	return cost, ""
}

func finiteBillingCost(cost float64) bool {
	return !math.IsNaN(cost) && !math.IsInf(cost, 0)
}

// 结算和官方复算使用同一套分项覆盖校验。只有实际用过的缓存种类才需要单价。
func (p selectedPrice) pricesForUsage(u TokenUsage, path string, separateWrites bool) (ModelPricing, string) {
	if p.row == 0 {
		return ModelPricing{}, "缺少命中档位的费率行"
	}
	if u.CacheReadTokens > 0 && !p.rate.CacheRead.Valid {
		return ModelPricing{}, "缺少缓存读取单价"
	}
	if !u.IncludesCacheInInput(path) {
		fiveMinutes, oneHour := u.claudeCacheWriteTokens()
		if fiveMinutes > 0 && !p.rate.Cache5m.Valid {
			return ModelPricing{}, "缺少 5m 缓存写入单价"
		}
		if oneHour > 0 && !p.rate.Cache1h.Valid {
			return ModelPricing{}, "缺少 1h 缓存写入单价"
		}
	}
	prices := priceFromRate(p.rate)
	if separateWrites {
		if !u.hasCompleteResponsesWriteUsage() {
			return ModelPricing{}, "缓存写入分项用量不完整"
		}
		row := p.rate.Sheet.Rows[p.row-1]
		prices.responsesWrites = true
		if u.CacheCreation30mTokens != nil && *u.CacheCreation30mTokens > 0 {
			if row.Cache30m == nil {
				return ModelPricing{}, "缺少 30m 缓存写入单价"
			}
			prices.cache30m = *row.Cache30m * p.factor
		}
		if u.CacheCreation12hTokens != nil && *u.CacheCreation12hTokens > 0 {
			if row.Cache12h == nil {
				return ModelPricing{}, "缺少 12h 缓存写入单价"
			}
			prices.cache12h = *row.Cache12h * p.factor
		}
	}
	prices.Input *= p.factor
	prices.Output *= p.factor
	prices.CacheRead *= p.factor
	prices.CacheCreation *= p.factor
	prices.CacheCreation1h *= p.factor
	return prices, ""
}

func (u TokenUsage) hasCompleteResponsesWriteUsage() bool {
	if u.CacheReadTokens < 0 || u.CacheReadTokens > u.InputTokens || u.CacheCreationTokens < 0 || u.CacheCreationTokens > u.InputTokens-u.CacheReadTokens {
		return false
	}
	short, long := u.CacheCreation30mTokens, u.CacheCreation12hTokens
	if short == nil && long == nil {
		return u.CacheCreationTokens == 0
	}
	return short != nil && long != nil && *short >= 0 && *long >= 0 && *short <= u.CacheCreationTokens &&
		*long == u.CacheCreationTokens-*short
}

func settleChannelUsage(base store.BaseRateVersion, found bool, u *TokenUsage, path, request, response string, b savedBillingBasis, rule store.ChannelRuleVersion) baseSettlement {
	d := BillingDecision{Channel: b.Channel, RequestedSpeed: speedValue(request), ResponseSpeed: speedValue(response), RuleID: rule.ID, Factor: 1}
	if b.Written {
		d.SentAtNS = b.AtNS
	}
	d.resolveSpeed()
	result := baseSettlement{decision: &d}
	if found {
		result.rateID = base.ID
	}
	if u != nil {
		d.FullInput = u.InputTokens
		if !u.IncludesCacheInInput(path) {
			d.FullInput += u.CacheReadTokens + max(u.CacheCreationTokens, u.CacheCreation5mTokens+u.CacheCreation1hTokens)
		}
	}
	var ruleProblem string
	if rule.Invalid {
		ruleProblem = "渠道模型规则已损坏"
	} else if rule.ID != 0 && found && !base.Invalid {
		if err := rule.Rule.ValidateForRate(base.Sheet); err != nil {
			ruleProblem = "渠道模型规则无效：" + err.Error()
		}
	}
	fastFree := d.Speed == "fast" && rule.Rule.FastMultiplier != nil && *rule.Rule.FastMultiplier == 0
	if !found || base.Invalid || u == nil {
		switch {
		case ruleProblem != "":
			d.Missing = ruleProblem
		case base.Invalid:
			d.Missing = "模型费率版本已损坏"
		case !found:
			d.Missing = "缺少模型费率版本"
		case fastFree:
			d.Factor, result.known = 0, true
		default:
			d.Missing = "缺少用量"
		}
		return result
	}
	officialWrites := u.IncludesCacheInInput(path) && base.Sheet.HasResponsesWritePrices()
	d.SeparateCacheWrites = officialWrites && !b.Channel.SubscriptionAccount && !(b.Channel.Type == "subscription" && b.Channel.Name == "chatgpt-subscription")
	var limits []int64
	timeKnown := true
	if sheet := base.Sheet; sheet != nil {
		limits = sheet.ContextLimits
		if len(sheet.Periods) > 0 && !b.Written {
			timeKnown = false
		} else {
			d.OfficialPeriod = sheet.PeriodAt(time.Unix(0, b.AtNS))
		}
	}
	tier := func(limits []int64) int {
		return sort.Search(len(limits), func(i int) bool { return d.FullInput <= limits[i] })
	}
	d.OfficialTier = tier(limits)
	fast := d.Speed == "fast"
	pick := func(period string, tier int, r store.ChannelModelRule, m EndpointMultiplier, separateWrites bool) (selectedPrice, bool) {
		p := selectPrice(base, period, tier, fast, r)
		if d.Speed == "" {
			other := selectPrice(base, period, tier, true, r)
			if p.row != other.row || p.factor != other.factor {
				a, missing := p.charge(u, path, m, separateWrites)
				bb, otherMissing := other.charge(u, path, m, separateWrites)
				if missing != "" || otherMissing != "" || a != bb {
					return selectedPrice{}, false
				}
			}
		}
		return p, true
	}
	unsupportedSpeed := base.Sheet != nil && d.unsupportedResponseSpeed()
	if base.Source == store.RateSourceOfficial && timeKnown && !unsupportedSpeed {
		if p, ok := pick(d.OfficialPeriod, d.OfficialTier, store.ChannelModelRule{}, EndpointMultiplier{}, officialWrites); ok {
			d.OfficialRow = p.row
		}
	}
	if ruleProblem != "" {
		d.Missing = ruleProblem
		return result
	}
	d.ChannelTier = tier(rule.Rule.Limits(limits))
	d.ChannelPeriod = d.OfficialPeriod
	if rule.Rule.Period != "" {
		d.ChannelPeriod = rule.Rule.Period
	}
	if unsupportedSpeed || (!timeKnown && rule.Rule.Period == "") {
		// 速度或时段缺少对应的费率行时，已命中的零倍率仍能确定渠道金额。
		switch {
		case selectPrice(base, d.ChannelPeriod, d.ChannelTier, fast, rule.Rule).factor == 0:
			d.Factor, result.known = 0, true
		case unsupportedSpeed:
			d.Missing = "价表未涵盖响应服务档位 " + d.ResponseSpeed
		default:
			d.Missing = "缺少上游写出时间"
		}
		return result
	}
	p, resolved := pick(d.ChannelPeriod, d.ChannelTier, rule.Rule, b.Multiplier, d.SeparateCacheWrites)
	if !resolved {
		d.Missing = "缺少实际服务档位"
		return result
	}
	if math.IsNaN(p.factor) || math.IsInf(p.factor, 0) {
		d.Missing = "倍率乘积超出数值范围"
		return result
	}
	d.ChannelRow, d.Factor = p.row, p.factor
	result.cost, d.Missing = p.charge(u, path, b.Multiplier, d.SeparateCacheWrites)
	result.known = d.Missing == ""
	return result
}
