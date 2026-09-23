package codexidentity

import (
	"strings"
)

// 关闭映射时保留旧的身份语义：正文和已有状态头不做身份层改写。
// 请求/响应的通用凭据与 hop-by-hop 隔离仍由账号池管线负责。
func (r *Request) prepareUnmapped(scope AccountScope, body []byte, owners *OwnerIndex) (*Prepared, error) {
	return &Prepared{Body: body, FirstParty: scope.IsFirstParty()}, nil
}

func knownDescriptor(src SourceHeaders) bool {
	if _, err := ValidateCompatVersion(src.Version); err != nil {
		return false
	}
	descriptors := map[string]string{
		"codex_cli_rs/":  "codex_cli_rs",
		"codex_exec/":    "codex_exec",
		"codex_vscode/":  "codex_vscode",
		"Codex Desktop/": "Codex Desktop",
	}
	for prefix, originator := range descriptors {
		version, ok := strings.CutPrefix(src.UserAgent, prefix)
		if !ok {
			continue
		}
		if at := strings.IndexByte(version, ' '); at >= 0 {
			version = version[:at]
		}
		if version == src.Version && src.Originator == originator {
			return true
		}
	}
	return false
}
