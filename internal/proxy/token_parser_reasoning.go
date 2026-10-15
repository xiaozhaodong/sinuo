package proxy

import (
	"strings"

	"sinuo/internal/reasoning"
)

// filterResponseModel 把解析器内部的占位模型名（空 / default / unknown / error:*）过滤为空串。
// response_model 列只允许真正来自上游响应的模型名。
func filterResponseModel(modelName string) string {
	modelName = strings.TrimSpace(modelName)
	switch {
	case modelName == "", modelName == "default", modelName == "unknown":
		return ""
	case strings.HasPrefix(modelName, "error:"):
		return ""
	}
	return modelName
}

// GetResponseModel 返回仅由响应解析得到的模型名；无响应模型时为空串。
func (tp *TokenParser) GetResponseModel() string {
	return filterResponseModel(tp.modelName)
}

// GetResponseReasoning 返回响应侧思考信息的累计归一化串（读取时序列化）。
func (tp *TokenParser) GetResponseReasoning() string {
	return tp.responseState.String()
}

// MergeResponseReasoningFromJSON 供非流式 JSON 解析使用：
// Claude 响应扫描 content[] 并标记完成；Codex 响应按字段合并 reasoning / usage。
func (tp *TokenParser) MergeResponseReasoningFromJSON(payload map[string]interface{}) {
	if payload == nil {
		return
	}
	if _, isClaude := payload["content"].([]interface{}); isClaude {
		tp.responseState.MarkClaudeContent(payload)
		return
	}
	fields := reasoning.ExtractCodexFields(payload)
	if payload["object"] != "response" {
		fields.Speed = ""
	}
	tp.responseState.MergeCodexFields(fields)
}

// ResponseDiagnostics 返回流式处理结束后的响应模型与思考串，供管线写入终态诊断。
func (sp *StreamProcessor) ResponseDiagnostics() (model, reasoning string) {
	if sp == nil || sp.tokenParser == nil {
		return "", ""
	}
	return sp.tokenParser.GetResponseModel(), sp.tokenParser.GetResponseReasoning()
}
