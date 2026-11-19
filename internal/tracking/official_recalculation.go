package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"sinuo/internal/store"
)

// OfficialRecalculationAvailability 描述历史事实是否足够支持按官方价复算。
type OfficialRecalculationAvailability struct {
	Available bool   `json:"available"`
	Missing   string `json:"missing,omitempty"`
}

type OfficialReferenceCost struct {
	CostBreakdown
	Decision *BillingDecision `json:"decision,omitempty"`
}

type officialBillingFacts struct {
	settled      bool
	invalidBasis bool
	path         string
	usage        *TokenUsage
	rateID       int64
	rate         store.BaseRateVersion
	decision     *BillingDecision
}

func (req *ActiveRequest) hotOfficialBillingFacts() officialBillingFacts {
	facts := officialBillingFacts{path: req.Path, settled: req.EndTime != nil}
	if !facts.settled || req.settled == nil || req.CostOverrideUSD != nil || req.billing.versions == nil {
		return facts
	}
	facts.rateID, facts.decision = req.settled.rateID, req.settled.decision
	if rate, ok := req.billing.rateFor(req.ModelName); ok && rate.ID == facts.rateID {
		facts.rate = rate
	}
	if req.UsageKnown {
		usage := req.billingUsage()
		facts.usage = &usage
	}
	return facts
}

const officialCostOverflow = "官方参考费用超出数值范围"

func (f officialBillingFacts) availability() *OfficialRecalculationAvailability {
	prices, missing := f.prepare()
	// 单价极高时才校验计算结果。常规详情只检查原始用量和费率覆盖。
	const certainFinitePrice = math.MaxFloat64 / (2 * float64(math.MaxInt64))
	if missing == "" && max(prices.Input, prices.Output, prices.CacheRead, prices.CacheCreation,
		prices.CacheCreation1h, prices.cache30m, prices.cache12h) >= certainFinitePrice {
		if cost := calculateAtRate(f.usage, &prices, nil, f.path); !finiteBillingCost(cost.TotalCost) {
			missing = officialCostOverflow
		}
	}
	return &OfficialRecalculationAvailability{Available: missing == "", Missing: missing}
}

func (f officialBillingFacts) prepare() (ModelPricing, string) {
	var prices ModelPricing
	switch {
	case !f.settled:
		return prices, "请求尚未结算"
	case f.invalidBasis && (f.rate.Source == "" || f.rate.Sheet != nil):
		return prices, "历史计费依据已损坏"
	case f.rateID == 0 && f.decision == nil:
		return prices, "该请求未保留历史官方费率"
	case f.rateID == 0:
		return prices, "缺少当时的模型费率版本"
	case f.rate.Source == "":
		return prices, "找不到请求引用的历史费率版本"
	case f.rate.Source == store.RateSourceCustom:
		return prices, "当时使用自定义价，未保留官方费率"
	case f.rate.Source != store.RateSourceOfficial:
		return prices, "当时使用旧版费率，未保留官方费率"
	case f.rate.Invalid || f.rate.ValidatePrices() != nil:
		return prices, "历史官方费率版本已损坏"
	case f.usage == nil:
		return prices, "缺少计费用量"
	}
	selected := selectedPrice{row: 1, rate: f.rate, factor: 1}
	if sheet := f.rate.Sheet; sheet != nil {
		d := f.decision
		if d == nil || d.OfficialRow <= 0 || d.OfficialRow > len(sheet.Rows) {
			return prices, f.missingOfficialRow()
		}
		selected.row = d.OfficialRow
		selected.rate = pricesForRow(f.rate, &sheet.Rows[selected.row-1])
	}
	separateWrites := f.usage.IncludesCacheInInput(f.path) && f.rate.Sheet.HasResponsesWritePrices()
	return selected.pricesForUsage(*f.usage, f.path, separateWrites)
}

func (f officialBillingFacts) missingOfficialRow() string {
	d, sheet := f.decision, f.rate.Sheet
	if d != nil && d.OfficialRow == 0 {
		if d.unsupportedResponseSpeed() {
			return "官方价表未涵盖响应服务档位 " + d.ResponseSpeed
		}
		if len(sheet.Periods) > 0 && d.OfficialPeriod == "" && d.SentAtNS == 0 {
			return "缺少上游写出时间，无法确定官方时段"
		}
		if d.Speed == "" && sheet.HasFast() {
			standard, _ := sheet.Row(d.OfficialPeriod, d.OfficialTier, false)
			fast, _ := sheet.Row(d.OfficialPeriod, d.OfficialTier, true)
			if standard != fast {
				return "缺少实际服务档位，无法确定官方费率行"
			}
		}
	}
	return "缺少当时的官方费率行"
}

// RecalculateOfficialBaseCost 只读已提交用量和当时的不可变行，不受后续调价、删价影响。
func (ut *UsageTracker) RecalculateOfficialBaseCost(ctx context.Context, requestID string) (CostBreakdown, error) {
	ref, err := ut.RecalculateOfficialCost(ctx, requestID)
	return ref.CostBreakdown, err
}

func (ut *UsageTracker) RecalculateOfficialCost(ctx context.Context, requestID string) (OfficialReferenceCost, error) {
	facts, err := ut.readOfficialBillingFacts(ctx, requestID)
	if err != nil {
		return OfficialReferenceCost{}, err
	}
	prices, missing := facts.prepare()
	if missing != "" {
		return OfficialReferenceCost{}, errors.New(missing)
	}
	cost := calculateAtRate(facts.usage, &prices, nil, facts.path)
	if !finiteBillingCost(cost.TotalCost) {
		return OfficialReferenceCost{}, errors.New(officialCostOverflow)
	}
	return OfficialReferenceCost{CostBreakdown: cost, Decision: facts.decision}, nil
}

func (ut *UsageTracker) readOfficialBillingFacts(ctx context.Context, requestID string) (officialBillingFacts, error) {
	if ut.hotPool != nil {
		if req, ok := ut.hotPool.SnapshotRequest(requestID); ok {
			return req.hotOfficialBillingFacts(), nil
		}
	}
	var facts archivedBillingFacts
	var usage TokenUsage
	var requestStatus, path, rawBasis string
	var ended bool
	var rateID int64
	var input sql.NullInt64
	dest := []any{&requestStatus, &ended, &path, &rateID, &rawBasis, &input, &usage.OutputTokens,
		&usage.CacheCreationTokens, &usage.CacheCreation5mTokens, &usage.CacheCreation1hTokens,
		&usage.CacheCreation30mTokens, &usage.CacheCreation12hTokens, &usage.CacheReadTokens}
	dest = append(dest, facts.rate.dest()...)
	err := ut.readDB.QueryRowContext(ctx, `SELECT q.status, q.end_time IS NOT NULL, q.path, COALESCE(q.billing_rate_id,0),
		COALESCE(q.billing_basis,''), q.input_tokens, COALESCE(q.output_tokens,0), COALESCE(q.cache_creation_tokens,0),
		COALESCE(q.cache_creation_5m_tokens,0), COALESCE(q.cache_creation_1h_tokens,0), q.cache_creation_30m_tokens,
		q.cache_creation_12h_tokens, COALESCE(q.cache_read_tokens,0), `+archivedRateColumns+`
		FROM request_logs q`+archivedRateJoin+` WHERE q.request_id=?`, requestID).Scan(dest...)
	if err == sql.ErrNoRows {
		return officialBillingFacts{}, errors.New("该请求未保留历史官方费率")
	}
	if err != nil {
		return officialBillingFacts{}, fmt.Errorf("读取原官方费率: %w", err)
	}
	facts.readBasis(rawBasis)
	facts.usageKnown, usage.InputTokens = input.Valid, input.Int64
	return facts.officialFacts(requestStatus, ended, path, rateID, &usage), nil
}
