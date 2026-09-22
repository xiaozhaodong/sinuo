package tracking

import "strings"

// ResponseDiagnostics 是终态时随 success / final_failure / settlement 事件一起原子落库的
// 上游侧与响应侧模型与思考诊断字段（v6.3 模型一致性监控）。
//
// 写规则（§3.1）：
//   - UpstreamModel：last-write-wins，允许写回空串（空 = 与 request_model 相同）
//   - ResponseModel / ResponseReasoning：非空才覆盖
type ResponseDiagnostics struct {
	UpstreamModel     string `json:"upstream_model"`
	ResponseModel     string `json:"response_model"`
	ResponseReasoning string `json:"response_reasoning"`
}

// applyTo 按写规则把诊断字段合并进热池中的 ActiveRequest；diag 为 nil 时不动任何字段。
func (d *ResponseDiagnostics) applyTo(req *ActiveRequest) {
	if d == nil || req == nil {
		return
	}
	req.UpstreamModel = strings.TrimSpace(d.UpstreamModel)
	if value := strings.TrimSpace(d.ResponseModel); value != "" {
		req.ResponseModel = value
	}
	if value := strings.TrimSpace(d.ResponseReasoning); value != "" {
		req.ResponseReasoning = value
	}
}

// responseDiagnosticsSetClause 是终态 UPDATE 语句里三列的 SET 片段，与 responseDiagnosticsArgs 一一对应。
// upstream_model 传 NULL 表示不改（无诊断），传空串表示"与请求相同"；后两列只在非空时覆盖。
const responseDiagnosticsSetClause = `upstream_model = COALESCE(?, upstream_model),
		response_model = COALESCE(NULLIF(?, ''), response_model),
		response_reasoning = COALESCE(NULLIF(?, ''), response_reasoning)`

// responseDiagnosticsArgs 生成与 responseDiagnosticsSetClause 对应的三个参数。
func responseDiagnosticsArgs(d *ResponseDiagnostics) []interface{} {
	if d == nil {
		return []interface{}{nil, nil, nil}
	}
	return []interface{}{
		strings.TrimSpace(d.UpstreamModel),
		strings.TrimSpace(d.ResponseModel),
		strings.TrimSpace(d.ResponseReasoning),
	}
}
