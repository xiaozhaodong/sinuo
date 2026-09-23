package codexidentity

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// 稳定映射：安装 namespace → 目标账号 scope → 源逻辑 ID。
//
// 同一逻辑 ID 家族（session / thread / x-client-request-id / prompt_cache_key / parent thread /
// turn id）使用同一映射函数，保持 CLI 的 session == thread 等等值关系；
// window 保留 `<thread>:<n>` 结构只映射线程部分；installation 只由 namespace + scope 派生。
// 映射用于稳定关联与隔离来源 ID，不提供不可关联的安全保证。

const (
	// ChatGPTFirstPartyHost 官方 Codex 后端 host；只有 OAuth 凭据发往该 host 才按首方规则处理。
	ChatGPTFirstPartyHost = "chatgpt.com"
)

// Namespace 持久化的随机 UUID namespace；跟随数据库备份恢复，不删除或轮换。
type Namespace struct {
	id uuid.UUID
}

// NewRandomNamespace 生成新的随机 namespace（仅在 settings 缺行时由 service 层调用一次）。
func NewRandomNamespace() (Namespace, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return Namespace{}, fmt.Errorf("生成 Codex 身份 namespace 失败: %w", err)
	}
	return Namespace{id: id}, nil
}

// ParseNamespace 解析已持久化的 namespace；非法值明确报错，不临时生成替代值。
func ParseNamespace(raw string) (Namespace, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Namespace{}, fmt.Errorf("Codex 身份 namespace 不是合法 UUID: %w", err)
	}
	if id == uuid.Nil {
		return Namespace{}, fmt.Errorf("Codex 身份 namespace 不能为全零 UUID")
	}
	return Namespace{id: id}, nil
}

// String 返回 namespace 的 UUID 文本。
func (n Namespace) String() string {
	return n.id.String()
}

// IsZero 表示 namespace 未加载。
func (n Namespace) IsZero() bool {
	return n.id == uuid.Nil
}

// AccountScope 目标账号作用域：账号记录 ID、provider、目标 origin 与 OAuth 解析出的真实账号 ID。
// 禁止把 access/refresh token 作为派生输入。token 自动刷新不改变 scope。
type AccountScope struct {
	AccountID        int64
	Provider         string
	TargetOrigin     string // scheme://host[:port]
	ChatGPTAccountID string
	// FirstPartyCredential 表示凭据是 ChatGPT OAuth 账号（由调用方按 provider 判定）。
	FirstPartyCredential bool
}

// NewAccountScope 从目标 URL 构造 scope；targetURL 非法时报错。
func NewAccountScope(accountID int64, provider, targetURL, chatGPTAccountID string, firstPartyCredential bool) (AccountScope, error) {
	parsed, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return AccountScope{}, fmt.Errorf("目标 URL 无效: %q", targetURL)
	}
	return AccountScope{
		AccountID:            accountID,
		Provider:             strings.ToLower(strings.TrimSpace(provider)),
		TargetOrigin:         strings.ToLower(parsed.Scheme + "://" + parsed.Host),
		ChatGPTAccountID:     strings.TrimSpace(chatGPTAccountID),
		FirstPartyCredential: firstPartyCredential,
	}, nil
}

// Key 返回 scope 的稳定键；不同账号、不同目标 origin 不共享映射。
func (s AccountScope) Key() string {
	raw, _ := json.Marshal([]any{s.AccountID, s.Provider, s.TargetOrigin, s.ChatGPTAccountID})
	return string(raw)
}

// TargetHost 返回目标 host（不含端口）。
func (s AccountScope) TargetHost() string {
	parsed, err := url.Parse(s.TargetOrigin)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// IsFirstParty 表示 OAuth 凭据发往官方 chatgpt.com：按首方 OAuth 池规则处理。
// 任何凭据发往第三方中转，或 API Key 发往公开 API，都按标准契约处理。
func (s AccountScope) IsFirstParty() bool {
	return s.FirstPartyCredential && (s.TargetOrigin == "https://chatgpt.com" || s.TargetOrigin == "https://chatgpt.com:443")
}

// Mapper 某个账号 scope 下的稳定 ID 映射器。
type Mapper struct {
	scopeNamespace uuid.UUID
}

// Mapper 返回 scope 专属的映射器。
func (n Namespace) Mapper(scope AccountScope) Mapper {
	return Mapper{scopeNamespace: uuid.NewSHA1(n.id, []byte("scope:"+scope.Key()))}
}

// ID 映射一个逻辑 ID（session / thread / request id / cache key / parent / turn）。
// 同一来源值在同一 scope 下恒定；空值映射为空。
func (m Mapper) ID(value string) string {
	if value == "" {
		return ""
	}
	return uuid.NewSHA1(m.scopeNamespace, []byte("id:"+value)).String()
}

// WindowID 映射 `<thread>:<n>` 形态的窗口 ID：线程部分走 ID 映射，保留后缀；
// 其他形态整体在独立的 window 空间映射。
func (m Mapper) WindowID(value string) string {
	if value == "" {
		return ""
	}
	if thread, suffix, ok := SplitWindowID(value); ok {
		return m.ID(thread) + ":" + suffix
	}
	return uuid.NewSHA1(m.scopeNamespace, []byte("window:"+value)).String()
}

// InstallationID 由 namespace + scope 派生的稳定安装标识，与来源安装 ID 无关。
func (m Mapper) InstallationID() string {
	return uuid.NewSHA1(m.scopeNamespace, []byte("installation")).String()
}

// PromptCacheKey 按来源关系映射 prompt_cache_key：
//   - 与来源 session 相同 → 与映射后的 session 相同（同一函数保证）；
//   - `<source>:<parent thread>` 派生前缀（Internal 会话）→ 保留前缀，只映射线程；
//   - 其他显式 override → 整体映射，保持其在该账号下的稳定性。
func (m Mapper) PromptCacheKey(value, parentThreadID string) string {
	if value == "" {
		return ""
	}
	if parentThreadID != "" {
		if prefix, rest, ok := strings.Cut(value, ":"); ok && rest == parentThreadID && prefix != "" {
			return prefix + ":" + m.ID(parentThreadID)
		}
	}
	return m.ID(value)
}

// SplitWindowID 拆分 `<thread>:<generation>`；generation 必须是十进制整数。
func SplitWindowID(value string) (thread, generation string, ok bool) {
	index := strings.LastIndexByte(value, ':')
	if index <= 0 || index == len(value)-1 {
		return "", "", false
	}
	suffix := value[index+1:]
	if _, err := strconv.ParseUint(suffix, 10, 64); err != nil {
		return "", "", false
	}
	return value[:index], suffix, true
}
