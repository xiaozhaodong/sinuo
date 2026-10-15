package proxy

import (
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/proxy/response"
)

// v6.3 模型一致性监控：端点管线与账号池管线共用的响应侧诊断接入点。

// recordEndpointUpstreamModel 把本 attempt 实际发出的模型写回生命周期管理器；
// 每次 attempt 覆盖（故障转移 A 改写 → B 未改写时 B 写回空串）。traceState 为 nil（无 attempt）时不写。
func recordEndpointUpstreamModel(lifecycleManager *RequestLifecycleManager, traceState *handlers.UpstreamTraceState) {
	if lifecycleManager == nil || traceState == nil {
		return
	}
	lifecycleManager.SetUpstreamModel(traceState.UpstreamModel())
}

// applyStreamResponseDiagnostics 流式结束后从解析器终态读取响应模型与思考串（非空覆盖）。
func applyStreamResponseDiagnostics(lifecycleManager *RequestLifecycleManager, processor *StreamProcessor) {
	if lifecycleManager == nil || processor == nil {
		return
	}
	model, reasoning := processor.ResponseDiagnostics()
	lifecycleManager.SetResponseModel(model)
	lifecycleManager.SetResponseReasoning(reasoning)
}

// applyResponseAnalysisDiagnostics 非流式响应：无条件应用响应模型（SetModelWithComparison + SetResponseModel）
// 与思考串，然后由调用方再按 usage 是否为 nil 分流终态。
func applyResponseAnalysisDiagnostics(lifecycleManager *RequestLifecycleManager, analysis response.ResponseAnalysis, source string) {
	if lifecycleManager == nil {
		return
	}
	if analysis.ResponseModel != "" {
		lifecycleManager.SetModelWithComparison(analysis.ResponseModel, source)
		lifecycleManager.SetResponseModel(analysis.ResponseModel)
	}
	lifecycleManager.SetResponseReasoning(analysis.ResponseReasoning)
}
