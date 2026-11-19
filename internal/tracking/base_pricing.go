package tracking

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"sinuo/internal/store"
)

// baseRateCatalog 发布后只读，最后一次有效区间变更界定了这个快照可见的版本。
type baseRateCatalog struct {
	rules        map[channelModelKey][]store.ChannelRuleVersion
	watermark    int64
	byModel      map[string][]store.BaseRateVersion
	byID         map[int64]store.BaseRateVersion
	defaultModel string
	lastChangeNS int64
}

// 计费依据中 0 留给未记录默认价的旧请求，负值标识没有数据库行的内置定价版本。
const builtinDefaultRateV1 int64 = -1

// 请求持有实际写出时的目录和倍率，即使到终态才识别出模型也能按原价结算。
type billingSnapshot struct {
	channel      store.BillingChannel
	at           time.Time
	versions     *baseRateCatalog
	prices       map[string]ModelPricing
	endpoints    map[string]EndpointMultiplier
	endpointByID map[int64]EndpointMultiplier
	accounts     map[int64]EndpointMultiplier
}

type baseSettlement struct {
	decision *BillingDecision
	rateID   int64
	known    bool
	cost     CostBreakdown
}

func (ut *UsageTracker) snapshotBilling(at time.Time) billingSnapshot {
	ut.mu.RLock()
	defer ut.mu.RUnlock()
	return billingSnapshot{at: at, versions: ut.baseRates, prices: ut.pricing, endpoints: ut.endpointMu, endpointByID: ut.endpointByID, accounts: ut.accountMu}
}

func priceFromRate(r store.BaseRateVersion) ModelPricing {
	return ModelPricing{Input: r.Input, Output: r.Output, CacheCreation: r.Cache5m.Float64, CacheCreation1h: r.Cache1h.Float64, CacheRead: r.CacheRead.Float64}
}

type billingCatalogReader interface {
	ReadBillingCatalog(context.Context) (store.BillingCatalog, error)
}

// LoadBaseRates 是启动和调价共用的发布入口。串行读取并交换整个目录，避免迟到的加载覆盖新版。
func (ut *UsageTracker) LoadBaseRates(ctx context.Context, source billingCatalogReader) error {
	ut.priceLoadMu.Lock()
	defer ut.priceLoadMu.Unlock()
	catalog, err := source.ReadBillingCatalog(ctx)
	if err != nil {
		ut.mu.Lock()
		if ut.baseRates == nil {
			// 首次加载失败时，新调用仍按版本结算，缺价的费用保留为空。
			ut.baseRates = &baseRateCatalog{}
		}
		ut.mu.Unlock()
		return err
	}
	versions := &baseRateCatalog{
		byModel: make(map[string][]store.BaseRateVersion), byID: make(map[int64]store.BaseRateVersion),
		defaultModel: catalog.DefaultModel,
	}
	versions.rules = make(map[channelModelKey][]store.ChannelRuleVersion)
	for _, v := range catalog.Rules {
		key := channelModelKey{v.Channel.Key(), v.Model}
		versions.rules[key] = append(versions.rules[key], v)
		versions.watermark = max(versions.watermark, v.ID)
	}
	current := make(map[string]ModelPricing)
	for _, r := range catalog.Rates {
		versions.byModel[r.ModelName] = append(versions.byModel[r.ModelName], r)
		versions.byID[r.ID] = r
		versions.lastChangeNS = max(versions.lastChangeNS, r.FromNS, r.UntilNS.Int64)
		if !r.UntilNS.Valid && !r.Invalid {
			current[r.ModelName] = priceFromRate(r)
		}
	}
	ut.mu.Lock()
	defer ut.mu.Unlock()
	ut.baseRates = versions
	ut.setPricingLocked(current)
	return nil
}

func (s billingSnapshot) rateFor(model string) (store.BaseRateVersion, bool) {
	rows := s.versions.byModel[model]
	at := s.at.UnixNano()
	i := sort.Search(len(rows), func(i int) bool { return rows[i].FromNS > at })
	if i == 0 {
		return store.BaseRateVersion{}, false
	}
	r := rows[i-1]
	return r, !r.UntilNS.Valid || at < r.UntilNS.Int64
}

// 估算使用默认模型的基础单价。它未提供的缓存项取内置默认单价；显式零价仍是免费。
func (v *baseRateCatalog) estimateRate(id int64) (store.BaseRateVersion, bool) {
	r, found := v.byID[id]
	if !found && id != builtinDefaultRateV1 {
		return store.BaseRateVersion{}, false
	}
	p := store.BuiltinDefaultPricing()
	if !found {
		r.Input, r.Output = p.InputPrice, p.OutputPrice
	}
	if !r.Cache5m.Valid {
		r.Cache5m = nullablePrice(&p.CacheCreationPrice5m)
	}
	if !r.Cache1h.Valid {
		r.Cache1h = nullablePrice(&p.CacheCreationPrice1h)
	}
	if !r.CacheRead.Valid {
		r.CacheRead = nullablePrice(&p.CacheReadPrice)
	}
	// 借用的版本已由 default_rate_id 固定；billing_rate_id 仅引用实际模型自身的费率。
	r.ID, r.Sheet, r.Source = 0, nil, store.RateSourceCustom
	return r, true
}

func (v *baseRateCatalog) settleUsage(model string, r store.BaseRateVersion, found bool, usage *TokenUsage, path, request, response string, b savedBillingBasis) baseSettlement {
	if !found {
		r, found = v.estimateRate(b.DefaultRateID)
	}
	rule := v.channelRule(b.Channel, model, b.RuleWatermark)
	return settleChannelUsage(r, found, usage, path, request, response, b, rule)
}

func (s billingSnapshot) multiplier(req *ActiveRequest) EndpointMultiplier {
	c := s.channelFor(req)
	switch c.Type {
	case "account":
		if m, ok := s.accounts[c.ID]; ok {
			return m
		}
		return DefaultEndpointMultiplier()
	case "subscription":
		return DefaultEndpointMultiplier()
	}
	if c.ID > 0 && s.endpointByID != nil {
		if m, ok := s.endpointByID[c.ID]; ok {
			return m
		}
		return DefaultEndpointMultiplier()
	}
	if m, ok := s.endpoints[c.Name]; ok {
		return m
	}
	return DefaultEndpointMultiplier()
}

func (s billingSnapshot) channelFor(req *ActiveRequest) store.BillingChannel {
	if s.channel.Type != "" {
		return s.channel
	}
	name := req.UpstreamName
	if req.UpstreamType == "" || req.UpstreamType == "endpoint" {
		name = req.EndpointName
	}
	return store.BillingChannel{Type: req.UpstreamType, ID: req.UpstreamID, Name: name}
}

func (req *ActiveRequest) billingUsage() TokenUsage {
	return TokenUsage{CacheWriteDurations: req.CacheWriteDurations, InputTokens: req.InputTokens, OutputTokens: req.OutputTokens, CacheCreationTokens: req.CacheCreationTokens,
		CacheCreation5mTokens: req.CacheCreation5mTokens, CacheCreation1hTokens: req.CacheCreation1hTokens,
		CacheReadTokens: req.CacheReadTokens}
}

func (s billingSnapshot) settle(req *ActiveRequest) baseSettlement {
	if req.CostOverrideUSD != nil {
		return baseSettlement{known: true, cost: CostBreakdown{TotalCost: *req.CostOverrideUSD}}
	}
	usage := req.billingUsage()
	m := s.multiplier(req)
	if s.versions != nil {
		r, found := s.rateFor(req.ModelName)
		basis := s.billingBasis(req)
		var tokens *TokenUsage
		if req.UsageKnown {
			tokens = &usage
		}
		return s.versions.settleUsage(req.ModelName, r, found, tokens, req.Path, req.RequestReasoning, req.ResponseReasoning, basis)
	}
	p, ok := s.prices[req.ModelName]
	if !ok {
		p = s.prices["_default"]
	}
	return baseSettlement{known: req.CostKnown || req.RequestSource == "", cost: CalculateCostV2(&usage, &p, &m, req.Path)}
}

func (req *ActiveRequest) settleBaseCost() {
	result := req.billing.settle(req)
	if c := req.billing.channel; c.Type != "" {
		req.UpstreamType, req.UpstreamID, req.UpstreamName = c.Type, c.ID, c.Name
		if c.Type == "endpoint" || c.Type == "account" {
			req.EndpointName = c.Name
		}
	}
	req.settled = &result
	req.CostKnown = result.known
	req.TotalCostUSD = result.cost.TotalCost
}

type savedBillingBasis struct {
	DefaultRateID int64                `json:"default_rate_id,omitempty"`
	Written       bool                 `json:"written,omitempty"`
	RuleWatermark int64                `json:"rule_watermark,omitempty"`
	Decision      *BillingDecision     `json:"decision,omitempty"`
	Channel       store.BillingChannel `json:"channel"`
	RateAtNS      int64                `json:"rate_at_ns"`
	AtNS          int64                `json:"at_ns"`
	Multiplier    EndpointMultiplier   `json:"multiplier"`
}

func (req *ActiveRequest) persistedBillingBasis(settled *baseSettlement) (any, error) {
	if req.billing.versions == nil || req.CostOverrideUSD != nil {
		return nil, nil
	}
	b := req.billing.billingBasis(req)
	if settled != nil {
		b.Decision = settled.decision
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func (s billingSnapshot) billingBasis(req *ActiveRequest) savedBillingBasis {
	at := s.at.UnixNano()
	b := savedBillingBasis{Written: s.channel.Type != "", RuleWatermark: s.versions.watermark, Channel: s.channelFor(req), AtNS: at, RateAtNS: min(at, s.versions.lastChangeNS), Multiplier: s.multiplier(req)}
	if s.versions.defaultModel != "" {
		b.DefaultRateID = builtinDefaultRateV1
		if rate, found := s.rateFor(s.versions.defaultModel); found {
			b.DefaultRateID = rate.ID
		}
	}
	return b
}
