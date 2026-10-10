package proxy

import (
	"strings"

	"cc-forwarder/internal/tracking"
)

// v6.3 模型一致性监控：请求 / 上游 / 响应模型与思考等级诊断字段。
// 全部在 modelMu 下读写；model_name（计费模型）语义由 SetModel / SetModelWithComparison 维持，不在此改动。
// 终态没有响应模型且计费模型仍是请求模型时，回落到最终 attempt 的上游模型。

// SetClientSession 仅在入口设置一次，供 StartRequest 写入客户端原始会话。
func (rlm *RequestLifecycleManager) SetClientSession(sessionID string) {
	rlm.modelMu.Lock()
	defer rlm.modelMu.Unlock()
	rlm.clientSessionID = sessionID
}

// SetRequestProfile 记录客户端原始请求模型与思考配置；仅入口调用一次，供 StartRequest 随 start 事件写入。
func (rlm *RequestLifecycleManager) SetRequestProfile(model, reasoning string) {
	rlm.modelMu.Lock()
	defer rlm.modelMu.Unlock()
	rlm.requestModel = strings.TrimSpace(model)
	rlm.requestReasoning = strings.TrimSpace(reasoning)
}

// SetUpstreamModel 记录本 attempt 实际发给上游的模型；每次 attempt 覆盖，空串表示与请求模型相同。
func (rlm *RequestLifecycleManager) SetUpstreamModel(model string) {
	rlm.modelMu.Lock()
	defer rlm.modelMu.Unlock()
	rlm.upstreamModel = strings.TrimSpace(model)
}

// SetResponseModel 记录仅由响应解析得到的模型；非空才覆盖。与 SetModelWithComparison 并列调用。
func (rlm *RequestLifecycleManager) SetResponseModel(model string) {
	model = strings.TrimSpace(model)
	if model == "" || model == "unknown" || model == "default" {
		return
	}
	rlm.modelMu.Lock()
	defer rlm.modelMu.Unlock()
	rlm.responseModel = model
}

// SetResponseReasoning 记录响应侧思考信息累计串；非空才覆盖。
func (rlm *RequestLifecycleManager) SetResponseReasoning(reasoning string) {
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		return
	}
	rlm.modelMu.Lock()
	defer rlm.modelMu.Unlock()
	rlm.responseReasoning = reasoning
}

// requestProfile 返回请求侧模型、思考配置与会话快照。
func (rlm *RequestLifecycleManager) requestProfile() (model, reasoning, sessionID string) {
	rlm.modelMu.RLock()
	defer rlm.modelMu.RUnlock()
	return rlm.requestModel, rlm.requestReasoning, rlm.clientSessionID
}

// responseDiagnostics 组装终态随事件原子落库的诊断字段（取快照后再交给 tracker，不持锁）。
func (rlm *RequestLifecycleManager) responseDiagnostics() *tracking.ResponseDiagnostics {
	rlm.modelMu.RLock()
	defer rlm.modelMu.RUnlock()
	return &tracking.ResponseDiagnostics{
		UpstreamModel:     rlm.upstreamModel,
		ResponseModel:     rlm.responseModel,
		ResponseReasoning: rlm.responseReasoning,
	}
}

// attachResponseDiagnostics 在中间态 UpdateStatus 上尽力挂载诊断字段（供在途展示；终态会覆盖）。
func (rlm *RequestLifecycleManager) attachResponseDiagnostics(opts *tracking.UpdateOptions) {
	if opts == nil {
		return
	}
	diag := rlm.responseDiagnostics()
	opts.UpstreamModel = &diag.UpstreamModel
	if diag.ResponseModel != "" {
		opts.ResponseModel = &diag.ResponseModel
	}
	if diag.ResponseReasoning != "" {
		opts.ResponseReasoning = &diag.ResponseReasoning
	}
}
