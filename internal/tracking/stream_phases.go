package tracking

import (
	"context"
	"fmt"
)

// StreamPhases 只保存最终尝试的观测；nil 表示未观测，不回填历史。
type StreamPhases struct {
	FirstContentMs    *int64  `json:"first_content_ms"`    // 首个正文、思考或工具内容相对请求开始的偏移（毫秒），只写一次
	FirstTextMs       *int64  `json:"first_text_ms"`       // 首个正文偏移（毫秒），只写一次
	FirstReasoningMs  *int64  `json:"first_reasoning_ms"`  // 首个可见思考偏移（毫秒），只写一次
	ReasoningHidden   *bool   `json:"reasoning_hidden"`    // 是否观测到不可见思考，只写一次
	StreamFlowMs      *int64  `json:"stream_flow_ms"`      // 内容字节中间80%到达跨度乘10/8（毫秒）；超限或未知为null
	StopReason        *string `json:"stop_reason"`         // 上游结束原因原文，最终尝试覆盖写
	UpstreamRequestID *string `json:"upstream_request_id"` // 最终尝试响应头的请求ID，覆盖写
}

func phaseCopy[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// Clone 隔离热池快照与调用方指针。
func (p StreamPhases) Clone() StreamPhases {
	return StreamPhases{
		FirstContentMs: phaseCopy(p.FirstContentMs), FirstTextMs: phaseCopy(p.FirstTextMs),
		FirstReasoningMs: phaseCopy(p.FirstReasoningMs), ReasoningHidden: phaseCopy(p.ReasoningHidden),
		StreamFlowMs: phaseCopy(p.StreamFlowMs), StopReason: phaseCopy(p.StopReason),
		UpstreamRequestID: phaseCopy(p.UpstreamRequestID),
	}
}

// merge 保持时间点只写一次，字符串允许最终尝试清空旧值。
func (p *StreamPhases) merge(next StreamPhases) {
	for _, pair := range [][2]**int64{
		{&p.FirstContentMs, &next.FirstContentMs}, {&p.FirstTextMs, &next.FirstTextMs},
		{&p.FirstReasoningMs, &next.FirstReasoningMs}, {&p.StreamFlowMs, &next.StreamFlowMs},
	} {
		if *pair[0] == nil && *pair[1] != nil && **pair[1] >= 0 {
			*pair[0] = phaseCopy(*pair[1])
		}
	}
	if p.ReasoningHidden == nil {
		p.ReasoningHidden = phaseCopy(next.ReasoningHidden)
	}
	if next.StopReason != nil {
		p.StopReason = phaseCopy(next.StopReason)
	}
	if next.UpstreamRequestID != nil {
		p.UpstreamRequestID = phaseCopy(next.UpstreamRequestID)
	}
}

func streamPhaseUpdateSQL(p StreamPhases) ([]string, []interface{}) {
	var parts []string
	var args []interface{}
	for _, field := range []struct {
		name  string
		value *int64
	}{
		{"first_content_ms", p.FirstContentMs}, {"first_text_ms", p.FirstTextMs},
		{"first_reasoning_ms", p.FirstReasoningMs}, {"stream_flow_ms", p.StreamFlowMs},
	} {
		if field.value != nil && *field.value >= 0 {
			parts = append(parts, field.name+" = CASE WHEN "+field.name+" IS NULL THEN ? ELSE "+field.name+" END")
			args = append(args, *field.value)
		}
	}
	if p.ReasoningHidden != nil {
		parts = append(parts, "reasoning_hidden = CASE WHEN reasoning_hidden IS NULL THEN ? ELSE reasoning_hidden END")
		args = append(args, *p.ReasoningHidden)
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"stop_reason", p.StopReason}, {"upstream_request_id", p.UpstreamRequestID},
	} {
		if field.value != nil {
			parts = append(parts, field.name+" = ?")
			args = append(args, *field.value)
		}
	}
	return parts, args
}

func (s *SQLiteAdapter) migrateStreamPhaseColumns(ctx context.Context) error {
	for _, field := range []struct{ name, kind string }{
		{"first_content_ms", "INTEGER"}, {"first_text_ms", "INTEGER"},
		{"first_reasoning_ms", "INTEGER"}, {"reasoning_hidden", "INTEGER"},
		{"stream_flow_ms", "INTEGER"}, {"stop_reason", "TEXT"}, {"upstream_request_id", "TEXT"},
	} {
		exists, err := s.columnExists(ctx, "request_logs", field.name)
		if err != nil {
			return fmt.Errorf("检查流阶段列 %s: %w", field.name, err)
		}
		if exists {
			continue
		}
		if _, err := s.db.ExecContext(ctx, "ALTER TABLE request_logs ADD COLUMN "+field.name+" "+field.kind); err != nil {
			return fmt.Errorf("添加流阶段列 %s: %w", field.name, err)
		}
	}
	return nil
}
