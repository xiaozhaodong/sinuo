package tracking

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// 入队时保留配置；数据库事件可能直到调价后才消费。
func (ut *UsageTracker) captureEventBilling(event *RequestEvent) {
	at := event.Timestamp
	switch event.Type {
	case "start":
	case "flexible_update":
		opts := event.Data.(UpdateOptions)
		if opts.FirstTokenStartAt != nil && opts.UpstreamWriteMs != nil {
			at = *opts.FirstTokenStartAt
		} else if opts.EndpointName == nil && opts.UpstreamID == nil {
			return
		}
	default:
		return
	}
	snapshot := ut.snapshotBilling(at)
	if opts, ok := event.Data.(UpdateOptions); ok {
		snapshot.channel = opts.BillingChannel
	}
	if snapshot.versions != nil {
		event.billing = &snapshot
	}
}

// eventBaseRate 将异步事件的费用、依据和用量写进同一事务。
type eventBaseRate struct {
	basis   savedBillingBasis
	catalog billingSnapshot
	model   string
	rateID  int64
}

func (ut *UsageTracker) readEventBaseRate(event RequestEvent) (*eventBaseRate, error) {
	switch event.Type {
	case "success", "final_failure", "complete", "failed_request_tokens", "subscription_settlement", "token_recovery":
	default:
		return nil, nil
	}
	snapshot := ut.snapshotBilling(event.Timestamp)
	if snapshot.versions == nil {
		return nil, nil
	}
	var raw sql.NullString
	p := eventBaseRate{catalog: snapshot}
	err := ut.readDB.QueryRow(`SELECT COALESCE(model_name,''),billing_basis,COALESCE(billing_rate_id,0)
		FROM request_logs WHERE request_id=?`, event.RequestID).Scan(&p.model, &raw, &p.rateID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !raw.Valid {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(raw.String), &p.basis); err != nil {
		return nil, fmt.Errorf("读取原计费依据: %w", err)
	}
	p.catalog.at = time.Unix(0, p.basis.RateAtNS)
	return &p, nil
}

// 已有版本依据的事件在事务内按合并后的事实结算。旧请求沿用原有费用口径。
func (ut *UsageTracker) legacyEventCost(event RequestEvent, model string, usage *TokenUsage, path string, legacyAccount bool) (CostBreakdown, error) {
	if event.baseRate != nil {
		return CostBreakdown{}, nil
	}
	price := ut.GetPricing(model)
	var multiplier *EndpointMultiplier
	if legacyAccount {
		var err error
		multiplier, err = ut.requestAccountMultiplier(event.RequestID)
		if err != nil {
			return CostBreakdown{}, err
		}
	}
	return CalculateCostV2(usage, &price, multiplier, path), nil
}

// 先应用事件的字段更新，再读取同一事务内的用量和诊断，终态无需重复携带 Token。
func (p *eventBaseRate) settle(ctx context.Context, tx *sql.Tx, requestID string) (baseSettlement, error) {
	var model, path, request, response string
	var u TokenUsage
	var input sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(model_name,''),path,COALESCE(request_reasoning,''),COALESCE(response_reasoning,''),
		input_tokens,COALESCE(output_tokens,0),COALESCE(cache_creation_tokens,0),COALESCE(cache_creation_5m_tokens,0),
		COALESCE(cache_creation_1h_tokens,0),cache_creation_30m_tokens,cache_creation_12h_tokens,COALESCE(cache_read_tokens,0) FROM request_logs WHERE request_id=?`, requestID).Scan(
		&model, &path, &request, &response, &input, &u.OutputTokens, &u.CacheCreationTokens, &u.CacheCreation5mTokens, &u.CacheCreation1hTokens, &u.CacheCreation30mTokens, &u.CacheCreation12hTokens, &u.CacheReadTokens)
	if err != nil {
		return baseSettlement{}, err
	}
	if model == "" || model == "unknown" || model == "default" {
		model = p.model
	}
	rate, found := p.catalog.rateFor(model)
	if model == p.model && p.rateID != 0 && (!found || rate.ID != p.rateID) {
		rate, found = p.catalog.versions.byID[p.rateID]
		if !found || rate.ModelName != model {
			return baseSettlement{}, fmt.Errorf("请求 %s 引用的费率行 %d 不存在", requestID, p.rateID)
		}
	}
	var usage *TokenUsage
	if input.Valid {
		u.InputTokens = input.Int64
		usage = &u
	}
	return p.catalog.versions.settleUsage(model, rate, found, usage, path, request, response, p.basis), nil
}

func (ut *UsageTracker) writeEventBilling(ctx context.Context, tx *sql.Tx, event RequestEvent) error {
	if snap := event.billing; snap != nil {
		req := ActiveRequest{billing: *snap}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(endpoint_name,''),COALESCE(upstream_name,''),upstream_type,upstream_id FROM request_logs WHERE request_id=?`, event.RequestID).Scan(
			&req.EndpointName, &req.UpstreamName, &req.UpstreamType, &req.UpstreamID); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		basis, err := req.persistedBillingBasis(nil)
		if err != nil {
			return err
		}
		query := "UPDATE request_logs SET billing_basis=? WHERE request_id=? AND end_time IS NULL"
		if opts, ok := event.Data.(UpdateOptions); ok {
			query += " AND billing_basis IS NOT NULL AND first_token_ms IS NULL"
			if opts.FirstTokenStartAt == nil || opts.UpstreamWriteMs == nil {
				query += " AND upstream_write_ms IS NULL"
			}
		}
		_, err = tx.ExecContext(ctx, query, basis, event.RequestID)
		return err
	}
	p := event.baseRate
	if p == nil {
		return nil
	}
	if data, ok := event.Data.(RequestCompleteData); ok && data.CostOverrideUSD != nil {
		_, err := tx.ExecContext(ctx, "UPDATE request_logs SET billing_rate_id=NULL,billing_basis=NULL WHERE request_id=?", event.RequestID)
		return err
	}
	result, err := p.settle(ctx, tx, event.RequestID)
	if err != nil {
		return err
	}
	p.basis.Decision = result.decision
	raw, err := json.Marshal(p.basis)
	if err != nil {
		return err
	}
	if c := p.basis.Channel; p.basis.Written && c.Type != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE request_logs SET upstream_type=?,upstream_name=?,upstream_id=?,
            endpoint_name=CASE WHEN ? IN ('endpoint','account') THEN ? ELSE endpoint_name END WHERE request_id=?`, c.Type, c.Name, c.ID, c.Type, c.Name, event.RequestID); err != nil {
			return err
		}
	}
	cost := result.cost
	_, err = tx.ExecContext(ctx, `UPDATE request_logs SET billing_rate_id=?,billing_basis=?,
		input_cost_usd=?,output_cost_usd=?,cache_creation_cost_usd=?,cache_creation_5m_cost_usd=?,cache_creation_1h_cost_usd=?,
		cache_read_cost_usd=?,total_cost_usd=? WHERE request_id=?`, nullInt64(result.rateID), string(raw),
		nullableFloat64(cost.InputCost, result.known), nullableFloat64(cost.OutputCost, result.known), nullableFloat64(cost.CacheCreationCost, result.known),
		nullableFloat64(cost.CacheCreation5mCost, result.known), nullableFloat64(cost.CacheCreation1hCost, result.known),
		nullableFloat64(cost.CacheReadCost, result.known), nullableFloat64(cost.TotalCost, result.known), event.RequestID)
	return err
}
