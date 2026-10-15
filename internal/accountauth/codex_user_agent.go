package accountauth

import "sinuo/internal/codexidentity"

// 辅助鉴权始终使用固定基础 UA，不依赖身份开关、namespace 或数据库。
const (
	CodexCompatVersion    = codexidentity.DefaultCompatVersion
	CodexCompatOriginator = codexidentity.Originator
)

func CodexBaseUserAgent() string { return codexidentity.BaseUserAgent(CodexCompatVersion) }
func normalizeThreeComponentVersion(version string) string {
	return codexidentity.NormalizeThreeComponentVersion(version)
}
