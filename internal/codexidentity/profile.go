package codexidentity

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 内置兼容描述档案：codex_cli_rs rust-v0.155.1，依据 P0 基线（2026-09-22）实测格式。
//
// 原生 UA 形态：`<originator>/<version> (<OS> <ver>; <arch>) <terminal>`；
// `/responses` 路径额外追加 ` (<originator>; <version>)` 尾段，`/models` 与辅助接口不追加。
// 档案只随发布维护，不自动跟随 GitHub latest；validated_at 是该次验收时间，不由运行时写入。
const (
	// DefaultCompatVersion 第二批默认兼容版本，也是辅助请求基础 UA 的固定 auth 基线版本。
	DefaultCompatVersion = "0.155.1"
	// Originator 池 OAuth 目标身份使用的 originator。
	Originator = "codex_cli_rs"
	// ProfileRuleVersion 字段规则版本，随内置档案一起复核。
	ProfileRuleVersion = "2026-09-22.1"
	// ProfileEvidence 内置档案的证据引用。
	ProfileEvidence = "docs/plans/2026-09-22-codex-fingerprint-p0-results.md"

	unknownTerminal         = "unknown"
	maxCompatVersionLength  = 32
	profileValidityDuration = 90 * 24 * time.Hour
)

// profileValidatedAt / profileValidUntil：最近一次验证结论及其有效窗口。
// 过期只把档案从“已验证”降为“过期、待复验”，仍可继续使用，不制造不可用错误。
var (
	profileValidatedAt = time.Date(2026, time.September, 22, 0, 0, 0, 0, time.UTC)
	profileValidUntil  = profileValidatedAt.Add(profileValidityDuration)
)

var compatVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// Operation 出站操作；决定 UA 形态与字段策略。
type Operation string

const (
	OperationResponses Operation = "responses"
	OperationCompact   Operation = "responses_compact"
	OperationModels    Operation = "models"
	OperationSearch    Operation = "search"
	OperationAuxiliary Operation = "auxiliary"
	OperationOther     Operation = "other"
)

// ClientProfile 客户端描述档案。
type ClientProfile struct {
	Kind        string
	Version     string
	Originator  string
	Platform    string // "<OS> <ver>; <arch>"
	Terminal    string
	ValidatedAt time.Time
	ValidUntil  time.Time
	RuleVersion string
	Evidence    string
}

// ValidateCompatVersion 校验并规范化兼容版本号。
func ValidateCompatVersion(raw string) (string, error) {
	version := strings.TrimSpace(raw)
	if version == "" {
		return "", fmt.Errorf("兼容版本号不能为空")
	}
	if len(version) > maxCompatVersionLength {
		return "", fmt.Errorf("兼容版本号过长（最多 %d 个字符）", maxCompatVersionLength)
	}
	if !compatVersionPattern.MatchString(version) {
		return "", fmt.Errorf("兼容版本号必须形如 0.155.1")
	}
	return version, nil
}

// BuiltinProfile 返回适用于 ChatGPT Codex 后端的内置 CLI 档案；version 非法时报错。
func BuiltinProfile(version string) (ClientProfile, error) {
	normalized, err := ValidateCompatVersion(version)
	if err != nil {
		return ClientProfile{}, err
	}
	profile := ClientProfile{
		Kind:        "cli",
		Version:     normalized,
		Originator:  Originator,
		Platform:    PlatformDescription(),
		Terminal:    unknownTerminal,
		ValidatedAt: profileValidatedAt,
		ValidUntil:  profileValidUntil,
		RuleVersion: ProfileRuleVersion,
		Evidence:    ProfileEvidence,
	}
	if normalized != DefaultCompatVersion {
		// 用户可配置兼容版本，但覆盖值不继承固定基线的验证结论。
		profile.ValidatedAt, profile.ValidUntil = time.Time{}, time.Time{}
		profile.Evidence = ""
	}
	return profile, nil
}

// UserAgent 按操作返回 UA：/responses 系列带 `(originator; version)` 尾段，其余不带。
func (p ClientProfile) UserAgent(op Operation) string {
	base := fmt.Sprintf("%s/%s (%s) %s", p.Originator, p.Version, p.Platform, p.Terminal)
	switch op {
	case OperationResponses, OperationCompact:
		return fmt.Sprintf("%s (%s; %s)", base, p.Originator, p.Version)
	default:
		return base
	}
}

// IsStale 表示最近一次验证结论已过有效窗口，档案降为“过期、待复验”。
func (p ClientProfile) IsStale(now time.Time) bool {
	return p.ValidatedAt.IsZero() || (!p.ValidUntil.IsZero() && !now.Before(p.ValidUntil))
}

// BaseUserAgent 返回无来源客户端上下文的辅助请求（RT 刷新、授权码兑换、连通性测试、quota）
// 使用的基础 UA；终端缺失时按原生缺省填 `unknown`，不伪造具体终端。
func BaseUserAgent(version string) string {
	return fmt.Sprintf("%s/%s (%s) %s", Originator, version, PlatformDescription(), unknownTerminal)
}

var (
	platformOnce sync.Once
	platformDesc string
)

// PlatformDescription 返回 `<OS> <ver>; <arch>`，与 os_info 的输出对齐。
func PlatformDescription() string {
	platformOnce.Do(func() {
		platformDesc = fmt.Sprintf("%s; %s", osDescription(), archDescription())
	})
	return platformDesc
}

func archDescription() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}

func osDescription() string {
	switch runtime.GOOS {
	case "darwin":
		if version := darwinProductVersion(); version != "" {
			return "Mac OS " + version
		}
		return "Mac OS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	default:
		return runtime.GOOS
	}
}

var darwinProductVersionPattern = regexp.MustCompile(`<key>ProductVersion</key>\s*<string>([0-9.]+)</string>`)

// darwinProductVersion 从系统版本 plist 读取 ProductVersion 并补齐为三段（os_info 口径，如 27.0.0）。
func darwinProductVersion() string {
	raw, err := os.ReadFile("/System/Library/CoreServices/SystemVersion.plist")
	if err != nil {
		return ""
	}
	match := darwinProductVersionPattern.FindSubmatch(raw)
	if len(match) < 2 {
		return ""
	}
	return NormalizeThreeComponentVersion(string(match[1]))
}

// NormalizeThreeComponentVersion 把版本号补齐/截断为三段。
func NormalizeThreeComponentVersion(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts[:3], ".")
}
