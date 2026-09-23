package accountauth

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// Codex 兼容描述档案（rust-v0.155.1，P0 2026-09-22 实测格式）。
//
// 原生 UA 形态：`<originator>/<version> (<OS> <ver>; <arch>) <terminal>`，
// `/responses` 路径额外追加 ` (<originator>; <version>)` 尾段，辅助接口不追加。
// 版本固定到已验证的发布，不自动跟随 GitHub latest；更新时同步复核 P0 结果文档。
const (
	CodexCompatVersion    = "0.155.1"
	CodexCompatOriginator = "codex_cli_rs"
	codexUnknownTerminal  = "unknown"
)

var (
	codexPlatformOnce sync.Once
	codexPlatformDesc string
)

// CodexBaseUserAgent 返回无来源客户端上下文时（RT 刷新、授权码兑换、连通性测试、quota）
// 使用的基础 UA。终端信息缺失时按原生缺省填 `unknown`，不伪造具体终端。
func CodexBaseUserAgent() string {
	return fmt.Sprintf("%s/%s (%s) %s", CodexCompatOriginator, CodexCompatVersion, codexPlatformDescription(), codexUnknownTerminal)
}

// codexPlatformDescription 返回 `<OS> <ver>; <arch>`，与 os_info 的输出对齐。
func codexPlatformDescription() string {
	codexPlatformOnce.Do(func() {
		codexPlatformDesc = fmt.Sprintf("%s; %s", codexOSDescription(), codexArch())
	})
	return codexPlatformDesc
}

func codexArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}

func codexOSDescription() string {
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
	return normalizeThreeComponentVersion(string(match[1]))
}

func normalizeThreeComponentVersion(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts[:3], ".")
}
