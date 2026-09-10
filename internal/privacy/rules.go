// Package privacy 提供出站请求隐私保护的纯规则引擎。
// 本包不依赖 proxy/store/service，只做规则编译、文本扫描与脱敏替换。
package privacy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 全局模式（privacy_settings.mode 唯一真值来源）
const (
	ModeDisabled = "disabled"
	ModeDetect   = "detect"
	ModeRedact   = "redact"
)

// 扫描出错策略
const (
	OnErrorFailOpen   = "fail_open"
	OnErrorFailClosed = "fail_closed"
)

// 超出扫描上限策略
const (
	OverLimitScanPrefix = "scan_prefix"
	OverLimitFailClosed = "fail_closed"
)

// 规则匹配方式
const (
	MatchTypeLiteral = "literal"
	MatchTypeRegex   = "regex"
	MatchTypeBuiltin = "builtin"
)

// 规则动作（block 仅预留，不在第一版 UI 暴露）
const (
	ActionDetect = "detect"
	ActionRedact = "redact"
)

// 上游类型。subscription 表示 Codex 订阅代理的凭据直通目标：没有本地账号 ID。
// 空 upstream_types scope 仍按既有语义匹配全部目标；显式 subscription 可将规则收窄到直通目标。
const (
	UpstreamTypeEndpoint     = "endpoint"
	UpstreamTypeAccount      = "account"
	UpstreamTypeSubscription = "subscription"
)

// 规则来源
const (
	SourceCustom  = "custom"
	SourcePreset  = "preset"
	SourceBuiltin = "builtin"
	SourceExact   = "exact"
)

// DefaultScanMaxBytes 单请求累计扫描文本字节上限默认值（4MB）
const DefaultScanMaxBytes = 4 * 1024 * 1024

// DefaultPlaceholder 默认脱敏占位符
const DefaultPlaceholder = "[已脱敏]"

// Scope 规则作用域。空数组表示该维度不限；多维度之间 AND，同一维度内 OR。
type Scope struct {
	Paths         []string `json:"paths"`
	UpstreamTypes []string `json:"upstream_types"`
	EndpointNames []string `json:"endpoint_names"`
	AccountIDs    []int64  `json:"account_ids"`
	ProviderTypes []string `json:"provider_types"`
}

// Rule 单条隐私规则
type Rule struct {
	ID          int64  `json:"id"`
	Enabled     bool   `json:"enabled"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	MatchType   string `json:"match_type"`
	Pattern     string `json:"pattern"`
	Placeholder string `json:"placeholder"`
	Action      string `json:"action"`
	Scope       Scope  `json:"scope"`
	Source      string `json:"source"`
}

// Settings 引擎运行配置（来自 privacy_settings 表）
type Settings struct {
	Mode            string `json:"mode"`
	ScanMaxBytes    int64  `json:"scan_max_bytes"`
	OverLimitAction string `json:"over_limit_action"`
	OnError         string `json:"on_error"`
}

// DefaultSettings 返回默认引擎配置（默认关闭）
func DefaultSettings() Settings {
	return Settings{
		Mode:            ModeDisabled,
		ScanMaxBytes:    DefaultScanMaxBytes,
		OverLimitAction: OverLimitScanPrefix,
		OnError:         OnErrorFailOpen,
	}
}

// ValidateSettings 校验引擎配置字段
func ValidateSettings(s Settings) error {
	switch s.Mode {
	case ModeDisabled, ModeDetect, ModeRedact:
	default:
		return fmt.Errorf("invalid mode: %q", s.Mode)
	}
	if s.ScanMaxBytes <= 0 {
		return fmt.Errorf("scan_max_bytes must be positive, got %d", s.ScanMaxBytes)
	}
	switch s.OverLimitAction {
	case OverLimitScanPrefix, OverLimitFailClosed:
	default:
		return fmt.Errorf("invalid over_limit_action: %q", s.OverLimitAction)
	}
	switch s.OnError {
	case OnErrorFailOpen, OnErrorFailClosed:
	default:
		return fmt.Errorf("invalid on_error: %q", s.OnError)
	}
	return nil
}

// ValidateRule 校验单条规则字段（不含正则编译，编译交给 CompileRule）
func ValidateRule(r Rule) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("rule name is required")
	}
	if r.Pattern == "" {
		return fmt.Errorf("rule pattern is required")
	}
	switch r.MatchType {
	case MatchTypeLiteral, MatchTypeRegex, MatchTypeBuiltin:
	default:
		return fmt.Errorf("invalid match_type: %q", r.MatchType)
	}
	switch r.Action {
	case ActionDetect, ActionRedact:
	default:
		return fmt.Errorf("invalid action: %q", r.Action)
	}
	if r.Action == ActionRedact && r.Placeholder == "" {
		return fmt.Errorf("placeholder is required for redact action")
	}
	return nil
}

// ParseScope 解析 scope JSON。空字符串或 "{}" 返回零值 Scope（不限）。
func ParseScope(scopeJSON string) (Scope, error) {
	scope := Scope{}
	trimmed := strings.TrimSpace(scopeJSON)
	if trimmed == "" {
		return scope, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &scope); err != nil {
		return Scope{}, fmt.Errorf("invalid scope json: %w", err)
	}
	return scope, nil
}

// EncodeScope 将 Scope 序列化为 JSON 字符串
func EncodeScope(scope Scope) (string, error) {
	raw, err := json.Marshal(scope)
	if err != nil {
		return "", fmt.Errorf("encode scope failed: %w", err)
	}
	return string(raw), nil
}

// Matches 判断作用域是否命中请求。空维度不限；path 使用精确匹配。
func (s Scope) Matches(req Request) bool {
	if !matchStringDim(s.Paths, req.Path) {
		return false
	}
	if !matchStringDim(s.UpstreamTypes, req.UpstreamType) {
		return false
	}
	if !matchStringDim(s.EndpointNames, req.EndpointName) {
		return false
	}
	if !matchInt64Dim(s.AccountIDs, req.AccountID) {
		return false
	}
	if !matchStringDim(s.ProviderTypes, req.ProviderType) {
		return false
	}
	return true
}

func matchStringDim(allowed []string, value string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, item := range allowed {
		if strings.TrimSpace(item) == value {
			return true
		}
	}
	return false
}

func matchInt64Dim(allowed []int64, value int64) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, item := range allowed {
		if item == value {
			return true
		}
	}
	return false
}
