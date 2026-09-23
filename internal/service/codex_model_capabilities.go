package service

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxCodexModelEntries = 1000
	MaxCodexCatalogBytes = 16 * 1024 * 1024
	maxCodexModelBytes   = 2 * 1024 * 1024

	maxCodexModelIDRunes     = 256
	maxCodexDisplayNameBytes = 4096
	maxCodexDescriptionBytes = 16384
	maxCodexSafeInteger      = 9007199254740991
	minCodexPriority         = -2147483648
	maxCodexPriority         = 2147483647
)

// 字段规则按 Codex rust-v0.156.1 的 ModelInfo 反序列化行为校准：比它严会拒绝上游合法目录，
// 比它松则会让 Codex 整份丢弃远端目录（单个模型解析失败即整体失败）。
// 前端 codexModelCatalog.js 维护同一套规则，共用用例见 testdata/codex_capabilities_cases.json。
var (
	codexIdentityFields = []string{"slug", "display_name", "description"}

	codexRequiredCapabilityFields = []string{
		"supported_reasoning_levels", "shell_type", "visibility", "supported_in_api",
		"priority", "support_verbosity", "truncation_policy", "experimental_supported_tools",
	}

	// nullable=true 对应 Codex 中的 Option 字段；带默认值的非 Option 字段可以缺省，但不能为 null。
	codexChoiceFields = []struct {
		key      string
		nullable bool
		choices  []string
	}{
		{"shell_type", false, []string{"unified_exec", "disabled", "default", "local", "shell_command"}},
		{"visibility", false, []string{"list", "hide", "none"}},
		{"default_reasoning_summary", false, []string{"auto", "concise", "detailed", "none"}},
		{"web_search_tool_type", false, []string{"text", "text_and_image"}},
		{"default_verbosity", true, []string{"low", "medium", "high"}},
		{"apply_patch_tool_type", true, []string{"freeform"}},
	}

	codexBoolCapabilityFields = []string{
		"supported_in_api", "support_verbosity", "include_skills_usage_instructions",
		"include_plugin_usage_instructions", "include_apps_usage_instructions",
		"supports_reasoning_summary_parameter", "supports_image_detail_original",
		"supports_search_tool", "supports_experimental_context", "use_responses_lite",
		"supports_reasoning_effort_updates", "node_repl_auto_review_required", "node_repl_disabled",
	}

	codexInputModalities = []string{"text", "image", "audio"}
	codexTruncationModes = []string{"bytes", "tokens"}
)

// codexFields 是能力对象的原始字段；缺失与 null 在 Codex 中语义不同，需要分开判断。
type codexFields map[string]json.RawMessage

// optional 返回需要继续校验的字段值：缺失、或允许为 null 时的 null 都视为未设置。
func (f codexFields) optional(key string, nullable bool) (json.RawMessage, bool, error) {
	value, exists := f[key]
	if !exists {
		return nil, false, nil
	}
	if string(value) == "null" {
		if nullable {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%s 不能为 null", key)
	}
	return value, true, nil
}

// ValidateCodexModelCatalog 在保存前做严格校验；读取与输出只逐条跳过无效能力，不整体失败。
func ValidateCodexModelCatalog(catalog CodexModelCatalog) error {
	if len(catalog.Models) > MaxCodexModelEntries {
		return fmt.Errorf("Codex 模型目录最多允许 %d 个条目", MaxCodexModelEntries)
	}
	mode := strings.ToLower(strings.TrimSpace(catalog.Mode))
	if mode != "" && mode != CodexModelsModeLocal && mode != CodexModelsModePassthrough {
		return fmt.Errorf("无效的 Codex 模型目录模式 %q", catalog.Mode)
	}
	seen := make(map[string]bool, len(catalog.Models))
	for _, model := range catalog.Models {
		if err := validateCodexModelEntry(model); err != nil {
			return err
		}
		id := strings.TrimSpace(model.ID)
		if seen[id] {
			return fmt.Errorf("Codex 模型 ID %q 重复", id)
		}
		seen[id] = true
	}
	payload, err := json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("序列化 Codex 模型目录失败: %w", err)
	}
	if len(payload) > MaxCodexCatalogBytes {
		return fmt.Errorf("Codex 模型目录超过 16 MiB")
	}
	return nil
}

func validateCodexModelEntry(model CodexModelEntry) error {
	id := strings.TrimSpace(model.ID)
	if !isValidCodexModelID(id) {
		return fmt.Errorf("Codex 模型 ID 必须为 1–%d 个字符且不含空白或控制字符", maxCodexModelIDRunes)
	}
	if len(model.DisplayName) > maxCodexDisplayNameBytes || len(model.Description) > maxCodexDescriptionBytes {
		return fmt.Errorf("模型 %q 的名称或描述过长", id)
	}
	if strings.TrimSpace(model.CapabilitiesJSON) == "" {
		return nil
	}
	if _, err := model.BuildCodexModelMetadata(); err != nil {
		return fmt.Errorf("模型 %q：%w", id, err)
	}
	return nil
}

func isValidCodexModelID(id string) bool {
	if id == "" || utf8.RuneCountInString(id) > maxCodexModelIDRunes {
		return false
	}
	return strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

// CapabilitiesError 返回能力无效的原因；未配置能力或能力有效时为空，供界面标记已保存的问题条目。
func (m CodexModelEntry) CapabilitiesError() string {
	if strings.TrimSpace(m.CapabilitiesJSON) == "" {
		return ""
	}
	if _, err := m.BuildCodexModelMetadata(); err != nil {
		return err.Error()
	}
	return ""
}

// BuildCodexModelMetadata 保留未知字段与原始 JSON 数值，只从权威字段派生模型身份。
func (m CodexModelEntry) BuildCodexModelMetadata() (json.RawMessage, error) {
	if len(m.CapabilitiesJSON) > maxCodexModelBytes {
		return nil, fmt.Errorf("单个模型的能力目录超过 2 MiB")
	}
	var fields codexFields
	if err := json.Unmarshal([]byte(m.CapabilitiesJSON), &fields); err != nil {
		return nil, fmt.Errorf("能力 JSON 无效: %w", err)
	}
	if fields == nil {
		return nil, fmt.Errorf("能力 JSON 必须是对象")
	}
	for _, key := range codexIdentityFields {
		if _, ok := fields[key]; ok {
			return nil, fmt.Errorf("能力 JSON 不应重复包含 %s，请使用模型基本信息字段", key)
		}
	}
	if err := validateCodexCapabilities(fields); err != nil {
		return nil, err
	}
	name := m.DisplayName
	if name == "" {
		name = strings.TrimSpace(m.ID)
	}
	for key, value := range map[string]string{"slug": strings.TrimSpace(m.ID), "display_name": name, "description": m.Description} {
		fields[key], _ = json.Marshal(value)
	}
	return json.Marshal(fields)
}

func validateCodexCapabilities(fields codexFields) error {
	checks := []func(codexFields) error{
		validateCodexRequiredFields,
		validateCodexChoiceFields,
		validateCodexBoolFields,
		validateCodexPriority,
		validateCodexContextWindow,
		validateCodexReasoningLevels,
		validateCodexStringArrays,
		validateCodexTruncationPolicy,
		validateCodexInstructions,
	}
	for _, check := range checks {
		if err := check(fields); err != nil {
			return err
		}
	}
	return nil
}

func validateCodexRequiredFields(fields codexFields) error {
	for _, key := range codexRequiredCapabilityFields {
		if value, exists := fields[key]; !exists || string(value) == "null" {
			return fmt.Errorf("能力目录缺少 %s", key)
		}
	}
	return nil
}

func validateCodexChoiceFields(fields codexFields) error {
	for _, field := range codexChoiceFields {
		raw, set, err := fields.optional(field.key, field.nullable)
		if err != nil {
			return err
		}
		if !set {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil || !slices.Contains(field.choices, value) {
			return fmt.Errorf("%s 的值无效", field.key)
		}
	}
	return nil
}

func validateCodexBoolFields(fields codexFields) error {
	for _, key := range codexBoolCapabilityFields {
		raw, set, err := fields.optional(key, false)
		if err != nil {
			return fmt.Errorf("%s 必须为布尔值", key)
		}
		var value bool
		if set && json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("%s 必须为布尔值", key)
		}
	}
	return nil
}

func validateCodexPriority(fields codexFields) error {
	var priority int64
	if json.Unmarshal(fields["priority"], &priority) != nil || priority < minCodexPriority || priority > maxCodexPriority {
		return fmt.Errorf("priority 必须为 32 位整数")
	}
	return nil
}

// validateCodexContextWindow 在 Codex 类型规则之外保留语义护栏：窗口为正、最大值不小于窗口、比例 1–100。
func validateCodexContextWindow(fields codexFields) error {
	window, err := codexPositiveInteger(fields, "context_window", true)
	if err != nil {
		return err
	}
	maximum, err := codexPositiveInteger(fields, "max_context_window", true)
	if err != nil {
		return err
	}
	if window > 0 && maximum > 0 && maximum < window {
		return fmt.Errorf("最大上下文不能小于上下文窗口")
	}
	percent, err := codexPositiveInteger(fields, "effective_context_window_percent", false)
	if err != nil {
		return err
	}
	if percent > 100 {
		return fmt.Errorf("有效上下文比例必须为 1–100")
	}
	return nil
}

// validateCodexReasoningLevels 与 Codex 一致接受任意非空等级名和空列表（非推理模型），
// 额外要求等级不重复、默认等级属于支持集合。
func validateCodexReasoningLevels(fields codexFields) error {
	var levels []struct {
		Effort      *string `json:"effort"`
		Description *string `json:"description"`
	}
	if json.Unmarshal(fields["supported_reasoning_levels"], &levels) != nil {
		return fmt.Errorf("supported_reasoning_levels 必须是思考等级数组")
	}
	seen := make(map[string]bool, len(levels))
	for _, level := range levels {
		if level.Effort == nil || *level.Effort == "" || level.Description == nil || seen[*level.Effort] {
			return fmt.Errorf("思考等级必须非空、不重复且包含 description")
		}
		seen[*level.Effort] = true
	}
	raw, set, err := fields.optional("default_reasoning_level", true)
	if err != nil || !set {
		return err
	}
	var defaultLevel string
	if json.Unmarshal(raw, &defaultLevel) != nil || !seen[defaultLevel] {
		return fmt.Errorf("默认思考等级必须属于支持的等级集合")
	}
	return nil
}

func validateCodexStringArrays(fields codexFields) error {
	for _, key := range []string{"experimental_supported_tools", "additional_speed_tiers", "input_modalities"} {
		raw, set, err := fields.optional(key, false)
		if err != nil {
			return fmt.Errorf("%s 必须是字符串数组", key)
		}
		if !set {
			continue
		}
		var values []string
		if json.Unmarshal(raw, &values) != nil {
			return fmt.Errorf("%s 必须是字符串数组", key)
		}
		if key == "input_modalities" {
			if err := validateCodexInputModalities(values); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCodexInputModalities(values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("至少选择一种输入能力")
	}
	for _, value := range values {
		if !slices.Contains(codexInputModalities, value) {
			return fmt.Errorf("输入能力仅支持 %s", strings.Join(codexInputModalities, "/"))
		}
	}
	return nil
}

func validateCodexTruncationPolicy(fields codexFields) error {
	var truncation struct {
		Mode  string `json:"mode"`
		Limit *int64 `json:"limit"`
	}
	if json.Unmarshal(fields["truncation_policy"], &truncation) != nil || !slices.Contains(codexTruncationModes, truncation.Mode) || truncation.Limit == nil || *truncation.Limit < 0 {
		return fmt.Errorf("truncation_policy 必须包含 bytes/tokens 模式和非负整数 limit")
	}
	return nil
}

// validateCodexInstructions 要求非空指令：Codex 只要求字段存在，但空指令会让模型失去系统提示。
func validateCodexInstructions(fields codexFields) error {
	var instructions string
	if raw, set, _ := fields.optional("base_instructions", true); set && json.Unmarshal(raw, &instructions) != nil {
		return fmt.Errorf("base_instructions 必须为字符串或 null")
	}
	var messages struct {
		Template *string `json:"instructions_template"`
	}
	if raw, set, _ := fields.optional("model_messages", true); set && json.Unmarshal(raw, &messages) != nil {
		return fmt.Errorf("model_messages 必须为对象或 null，其中 instructions_template 必须为字符串或 null")
	}
	template := ""
	if messages.Template != nil {
		template = *messages.Template
	}
	if strings.TrimSpace(instructions) == "" && strings.TrimSpace(template) == "" {
		return fmt.Errorf("需要完整的 base_instructions 或 model_messages.instructions_template；请先导入完整目录或复制已有模型")
	}
	return nil
}

func codexPositiveInteger(fields codexFields, key string, nullable bool) (int64, error) {
	raw, set, err := fields.optional(key, nullable)
	if err != nil || !set {
		return 0, err
	}
	var n int64
	if json.Unmarshal(raw, &n) != nil || n <= 0 || n > maxCodexSafeInteger {
		return 0, fmt.Errorf("%s 必须为正的安全整数", key)
	}
	return n, nil
}
