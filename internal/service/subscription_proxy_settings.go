// Package service - Codex 订阅代理专用配置服务
// 创建时间: 2026-09-10
//
// 配置以单行 JSON 保存在 settings 表的 codex_subscription_proxy.config：
//   - 首次初始化只允许插入缺行（InsertIfAbsent），并一次性返回明文访问密钥；
//   - 之后所有保存都携带 expected_revision，经 store.CompareAndSwapValue 原子比较更新；
//   - 数据库只保存访问密钥的 SHA-256 十六进制校验值，校验采用恒定时间比较；
//   - 配置损坏（JSON/schema/字段非法）时明确报错且拒绝覆盖，不静默重置。
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"cc-forwarder/internal/store"
)

const (
	// CodexSubscriptionProxyConfigKey 单行配置在 settings 表中的 key。
	CodexSubscriptionProxyConfigKey = "config"
	// CodexSubscriptionProxySchemaVersion 当前配置结构版本；不匹配视为损坏。
	CodexSubscriptionProxySchemaVersion = 1

	CodexSubscriptionProxyModeSubscription = "subscription"
	CodexSubscriptionProxyModePool         = "pool"

	CodexSubscriptionProxyCompressionAuto     = "auto"
	CodexSubscriptionProxyCompressionDisabled = "disabled"

	CodexSubscriptionProxyDefaultListenHost = "127.0.0.1"
	CodexSubscriptionProxyDefaultListenPort = 28088

	// CodexSubscriptionProxyCapabilityPrefix / Suffix 组成 Codex 接入地址：
	// http://<host>:<port>/subscription/<secret>/backend-api/codex
	CodexSubscriptionProxyCapabilityPrefix = "/subscription/"
	CodexSubscriptionProxyCapabilitySuffix = "/backend-api/codex"

	codexSubscriptionProxyAccessSecretBytes = 32
	// CodexSubscriptionProxyMaxSecretLength 限制待校验密钥长度，避免对任意长文本做哈希。
	CodexSubscriptionProxyMaxSecretLength = 128
)

var (
	// ErrCodexSubscriptionProxyRevisionConflict 表示 expected_revision 与当前不一致或 CAS 未命中。
	ErrCodexSubscriptionProxyRevisionConflict = errors.New("Codex 订阅代理配置已被其他写入更新，请刷新后重试")
	// ErrCodexSubscriptionProxyConfigCorrupted 表示已存配置无法解析或字段非法；保存会被拒绝，不覆盖。
	ErrCodexSubscriptionProxyConfigCorrupted = errors.New("Codex 订阅代理配置损坏，已拒绝覆盖")
	// ErrCodexSubscriptionProxyNotInitialized 表示尚无配置行，需先保存一次完整配置。
	ErrCodexSubscriptionProxyNotInitialized = errors.New("Codex 订阅代理尚未初始化")
	// ErrCodexSubscriptionProxyInvalidConfig 表示输入不满足校验规则。
	ErrCodexSubscriptionProxyInvalidConfig = errors.New("Codex 订阅代理配置无效")
)

// CodexSubscriptionProxyConfig 持久化的单行配置。
type CodexSubscriptionProxyConfig struct {
	SchemaVersion   int     `json:"schema_version"`
	Revision        int64   `json:"revision"`
	Enabled         bool    `json:"enabled"`
	Mode            string  `json:"mode"`
	ListenHost      string  `json:"listen_host"`
	ListenPort      int     `json:"listen_port"`
	WSCompression   string  `json:"ws_compression"`
	LocalAccessHash *string `json:"local_access_hash"`
}

// ListenAddr 返回监听地址 host:port（IPv6 自动加方括号）。
func (c CodexSubscriptionProxyConfig) ListenAddr() string {
	return net.JoinHostPort(c.ListenHost, strconv.Itoa(c.ListenPort))
}

// AccessConfigured 表示已存在可校验的访问密钥哈希。
func (c CodexSubscriptionProxyConfig) AccessConfigured() bool {
	return len(c.AccessHashBytes()) == sha256.Size
}

// AccessHashBytes 解码存储的十六进制哈希；缺失或格式错误返回 nil。
func (c CodexSubscriptionProxyConfig) AccessHashBytes() []byte {
	if c.LocalAccessHash == nil {
		return nil
	}
	raw, err := hex.DecodeString(strings.TrimSpace(*c.LocalAccessHash))
	if err != nil || len(raw) != sha256.Size {
		return nil
	}
	return raw
}

// CodexSubscriptionProxyConfigState Load 的结果：Initialized=false 表示数据库中没有配置行。
type CodexSubscriptionProxyConfigState struct {
	Config      CodexSubscriptionProxyConfig
	Initialized bool
}

// CodexSubscriptionProxyConfigInput 保存输入；不包含 revision 与密钥。
type CodexSubscriptionProxyConfigInput struct {
	Enabled       bool
	Mode          string
	ListenHost    string
	ListenPort    int
	WSCompression string
}

// CodexSubscriptionProxySaveResult 保存结果；AccessSecret 仅在首次初始化与轮换时返回一次。
type CodexSubscriptionProxySaveResult struct {
	Config       CodexSubscriptionProxyConfig
	AccessSecret string
}

// CodexSubscriptionProxySettingsService 专用配置服务：本进程内串行化写入，数据库层由 CAS 保证。
type CodexSubscriptionProxySettingsService struct {
	store store.SettingsStore
	mu    sync.Mutex
}

// NewCodexSubscriptionProxySettingsService 创建专用配置服务。
func NewCodexSubscriptionProxySettingsService(st store.SettingsStore) *CodexSubscriptionProxySettingsService {
	return &CodexSubscriptionProxySettingsService{store: st}
}

// DefaultCodexSubscriptionProxyConfig 返回未初始化时展示的默认值（不落库）。
func DefaultCodexSubscriptionProxyConfig() CodexSubscriptionProxyConfig {
	return CodexSubscriptionProxyConfig{
		SchemaVersion: CodexSubscriptionProxySchemaVersion,
		Revision:      0,
		Enabled:       false,
		Mode:          CodexSubscriptionProxyModeSubscription,
		ListenHost:    CodexSubscriptionProxyDefaultListenHost,
		ListenPort:    CodexSubscriptionProxyDefaultListenPort,
		WSCompression: CodexSubscriptionProxyCompressionAuto,
	}
}

// Load 读取当前配置；缺行返回默认值且 Initialized=false；损坏或读取失败返回错误。
func (s *CodexSubscriptionProxySettingsService) Load(ctx context.Context) (CodexSubscriptionProxyConfigState, error) {
	record, cfg, err := s.loadRaw(ctx)
	if err != nil {
		return CodexSubscriptionProxyConfigState{}, err
	}
	if record == nil {
		return CodexSubscriptionProxyConfigState{Config: DefaultCodexSubscriptionProxyConfig()}, nil
	}
	return CodexSubscriptionProxyConfigState{Config: *cfg, Initialized: true}, nil
}

// Save 保存完整配置。expectedRevision 为 0 且尚无配置行时执行首次初始化并生成访问密钥；
// 其他情况要求 expectedRevision 等于当前 revision，否则返回冲突。
func (s *CodexSubscriptionProxySettingsService) Save(ctx context.Context, input CodexSubscriptionProxyConfigInput, expectedRevision int64) (CodexSubscriptionProxySaveResult, error) {
	normalized, err := NormalizeCodexSubscriptionProxyInput(input)
	if err != nil {
		return CodexSubscriptionProxySaveResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, current, err := s.loadRaw(ctx)
	if err != nil {
		return CodexSubscriptionProxySaveResult{}, err
	}
	if record == nil {
		return s.initialize(ctx, normalized, expectedRevision)
	}
	next := *current
	next.Enabled = normalized.Enabled
	next.Mode = normalized.Mode
	next.ListenHost = normalized.ListenHost
	next.ListenPort = normalized.ListenPort
	next.WSCompression = normalized.WSCompression
	if err := s.swap(ctx, record, current, &next, expectedRevision); err != nil {
		return CodexSubscriptionProxySaveResult{}, err
	}
	return CodexSubscriptionProxySaveResult{Config: next}, nil
}

// SetEnabled 只切换启用意图，其余字段保持不变；未初始化时返回明确错误。
func (s *CodexSubscriptionProxySettingsService) SetEnabled(ctx context.Context, enabled bool, expectedRevision int64) (CodexSubscriptionProxySaveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, current, err := s.loadRaw(ctx)
	if err != nil {
		return CodexSubscriptionProxySaveResult{}, err
	}
	if record == nil {
		return CodexSubscriptionProxySaveResult{}, ErrCodexSubscriptionProxyNotInitialized
	}
	next := *current
	next.Enabled = enabled
	if err := s.swap(ctx, record, current, &next, expectedRevision); err != nil {
		return CodexSubscriptionProxySaveResult{}, err
	}
	return CodexSubscriptionProxySaveResult{Config: next}, nil
}

// RotateAccess 生成新的访问密钥并替换哈希；旧地址立即失效。明文只随本次结果返回一次。
func (s *CodexSubscriptionProxySettingsService) RotateAccess(ctx context.Context, expectedRevision int64) (CodexSubscriptionProxySaveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, current, err := s.loadRaw(ctx)
	if err != nil {
		return CodexSubscriptionProxySaveResult{}, err
	}
	if record == nil {
		return CodexSubscriptionProxySaveResult{}, ErrCodexSubscriptionProxyNotInitialized
	}
	secret, hashHex, err := GenerateCodexSubscriptionProxyAccessSecret()
	if err != nil {
		return CodexSubscriptionProxySaveResult{}, err
	}
	next := *current
	next.LocalAccessHash = &hashHex
	if err := s.swap(ctx, record, current, &next, expectedRevision); err != nil {
		return CodexSubscriptionProxySaveResult{}, err
	}
	return CodexSubscriptionProxySaveResult{Config: next, AccessSecret: secret}, nil
}

// initialize 首次插入配置行；已存在（并发初始化）返回冲突。调用方必须持有 s.mu。
func (s *CodexSubscriptionProxySettingsService) initialize(ctx context.Context, input CodexSubscriptionProxyConfigInput, expectedRevision int64) (CodexSubscriptionProxySaveResult, error) {
	if expectedRevision != 0 {
		return CodexSubscriptionProxySaveResult{}, fmt.Errorf("%w: 配置尚未初始化，expected_revision 应为 0", ErrCodexSubscriptionProxyRevisionConflict)
	}
	secret, hashHex, err := GenerateCodexSubscriptionProxyAccessSecret()
	if err != nil {
		return CodexSubscriptionProxySaveResult{}, err
	}
	cfg := CodexSubscriptionProxyConfig{
		SchemaVersion:   CodexSubscriptionProxySchemaVersion,
		Revision:        1,
		Enabled:         input.Enabled,
		Mode:            input.Mode,
		ListenHost:      input.ListenHost,
		ListenPort:      input.ListenPort,
		WSCompression:   input.WSCompression,
		LocalAccessHash: &hashHex,
	}
	value, err := json.Marshal(cfg)
	if err != nil {
		return CodexSubscriptionProxySaveResult{}, fmt.Errorf("序列化 Codex 订阅代理配置失败: %w", err)
	}
	inserted, err := s.store.InsertIfAbsent(ctx, &store.SettingRecord{
		Category:    CategoryCodexSubscriptionProxy,
		Key:         CodexSubscriptionProxyConfigKey,
		Value:       string(value),
		ValueType:   ValueTypeJSON,
		Label:       "Codex 订阅代理配置",
		Description: "单行 JSON；仅可通过 Codex 订阅代理设置页修改",
	})
	if err != nil {
		return CodexSubscriptionProxySaveResult{}, err
	}
	if !inserted {
		return CodexSubscriptionProxySaveResult{}, fmt.Errorf("%w: 配置已被并发初始化", ErrCodexSubscriptionProxyRevisionConflict)
	}
	return CodexSubscriptionProxySaveResult{Config: cfg, AccessSecret: secret}, nil
}

// swap 校验 revision 后把 next（revision+1）以 CAS 写回；未命中返回冲突。调用方必须持有 s.mu。
func (s *CodexSubscriptionProxySettingsService) swap(ctx context.Context, record *store.SettingRecord, current, next *CodexSubscriptionProxyConfig, expectedRevision int64) error {
	if current.Revision != expectedRevision {
		return fmt.Errorf("%w: 当前 revision=%d, expected=%d", ErrCodexSubscriptionProxyRevisionConflict, current.Revision, expectedRevision)
	}
	next.SchemaVersion = CodexSubscriptionProxySchemaVersion
	next.Revision = current.Revision + 1
	value, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("序列化 Codex 订阅代理配置失败: %w", err)
	}
	swapped, err := s.store.CompareAndSwapValue(ctx, CategoryCodexSubscriptionProxy, CodexSubscriptionProxyConfigKey, record.Value, string(value))
	if err != nil {
		return err
	}
	if !swapped {
		return fmt.Errorf("%w: 数据库中的配置已变化", ErrCodexSubscriptionProxyRevisionConflict)
	}
	return nil
}

// loadRaw 读取并严格解析配置行；缺行返回 (nil, nil, nil)。
func (s *CodexSubscriptionProxySettingsService) loadRaw(ctx context.Context) (*store.SettingRecord, *CodexSubscriptionProxyConfig, error) {
	record, err := s.store.Get(ctx, CategoryCodexSubscriptionProxy, CodexSubscriptionProxyConfigKey)
	if err != nil {
		return nil, nil, fmt.Errorf("读取 Codex 订阅代理配置失败: %w", err)
	}
	if record == nil {
		return nil, nil, nil
	}
	cfg, err := parseCodexSubscriptionProxyConfig(record.Value)
	if err != nil {
		return nil, nil, err
	}
	return record, cfg, nil
}

// parseCodexSubscriptionProxyConfig 解析并校验已存 JSON；任何异常都归为“损坏”。
func parseCodexSubscriptionProxyConfig(raw string) (*CodexSubscriptionProxyConfig, error) {
	var cfg CodexSubscriptionProxyConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCodexSubscriptionProxyConfigCorrupted, err)
	}
	if cfg.SchemaVersion != CodexSubscriptionProxySchemaVersion {
		return nil, fmt.Errorf("%w: 不支持的 schema_version=%d", ErrCodexSubscriptionProxyConfigCorrupted, cfg.SchemaVersion)
	}
	if cfg.Revision < 1 {
		return nil, fmt.Errorf("%w: revision=%d 非法", ErrCodexSubscriptionProxyConfigCorrupted, cfg.Revision)
	}
	normalized, err := NormalizeCodexSubscriptionProxyInput(CodexSubscriptionProxyConfigInput{
		Enabled:       cfg.Enabled,
		Mode:          cfg.Mode,
		ListenHost:    cfg.ListenHost,
		ListenPort:    cfg.ListenPort,
		WSCompression: cfg.WSCompression,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCodexSubscriptionProxyConfigCorrupted, err)
	}
	// 已存值必须已是规范形式，否则 CAS 的原文比较会与展示值脱节
	if normalized.Mode != cfg.Mode || normalized.ListenHost != cfg.ListenHost ||
		normalized.ListenPort != cfg.ListenPort || normalized.WSCompression != cfg.WSCompression {
		return nil, fmt.Errorf("%w: 字段未规范化", ErrCodexSubscriptionProxyConfigCorrupted)
	}
	if cfg.LocalAccessHash != nil && len(cfg.AccessHashBytes()) != sha256.Size {
		return nil, fmt.Errorf("%w: local_access_hash 格式错误", ErrCodexSubscriptionProxyConfigCorrupted)
	}
	return &cfg, nil
}

// NormalizeCodexSubscriptionProxyInput 规范化并校验输入：
// 模式仅 subscription/pool；监听地址必须是回环 IP 字面量；端口 1-65535；压缩仅 auto/disabled。
func NormalizeCodexSubscriptionProxyInput(input CodexSubscriptionProxyConfigInput) (CodexSubscriptionProxyConfigInput, error) {
	out := input

	out.Mode = strings.ToLower(strings.TrimSpace(input.Mode))
	if out.Mode == "" {
		out.Mode = CodexSubscriptionProxyModeSubscription
	}
	if out.Mode != CodexSubscriptionProxyModeSubscription && out.Mode != CodexSubscriptionProxyModePool {
		return out, fmt.Errorf("%w: mode 只能是 %s 或 %s", ErrCodexSubscriptionProxyInvalidConfig, CodexSubscriptionProxyModeSubscription, CodexSubscriptionProxyModePool)
	}

	host := strings.TrimSpace(input.ListenHost)
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" {
		host = CodexSubscriptionProxyDefaultListenHost
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return out, fmt.Errorf("%w: listen_host 必须是 IP 字面量", ErrCodexSubscriptionProxyInvalidConfig)
	}
	if !ip.IsLoopback() {
		return out, fmt.Errorf("%w: listen_host 只允许回环地址", ErrCodexSubscriptionProxyInvalidConfig)
	}
	out.ListenHost = ip.String()

	if input.ListenPort == 0 {
		out.ListenPort = CodexSubscriptionProxyDefaultListenPort
	}
	if out.ListenPort < 1 || out.ListenPort > 65535 {
		return out, fmt.Errorf("%w: listen_port 必须在 1-65535 之间", ErrCodexSubscriptionProxyInvalidConfig)
	}

	out.WSCompression = strings.ToLower(strings.TrimSpace(input.WSCompression))
	if out.WSCompression == "" {
		out.WSCompression = CodexSubscriptionProxyCompressionAuto
	}
	if out.WSCompression != CodexSubscriptionProxyCompressionAuto && out.WSCompression != CodexSubscriptionProxyCompressionDisabled {
		return out, fmt.Errorf("%w: ws_compression 只能是 %s 或 %s", ErrCodexSubscriptionProxyInvalidConfig, CodexSubscriptionProxyCompressionAuto, CodexSubscriptionProxyCompressionDisabled)
	}
	return out, nil
}

// GenerateCodexSubscriptionProxyAccessSecret 生成 32 字节密码学随机密钥（base64url 无填充）及其 SHA-256 十六进制哈希。
func GenerateCodexSubscriptionProxyAccessSecret() (secret string, hashHex string, err error) {
	raw := make([]byte, codexSubscriptionProxyAccessSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("生成 Codex 订阅代理访问密钥失败: %w", err)
	}
	secret = base64.RawURLEncoding.EncodeToString(raw)
	return secret, hex.EncodeToString(HashCodexSubscriptionProxyAccessSecret(secret)), nil
}

// HashCodexSubscriptionProxyAccessSecret 计算密钥的 SHA-256。
func HashCodexSubscriptionProxyAccessSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// VerifyCodexSubscriptionProxyAccessSecret 用恒定时间比较校验待验证密钥；哈希缺失或格式错误一律失败。
func VerifyCodexSubscriptionProxyAccessSecret(storedHash []byte, presented string) bool {
	if len(storedHash) != sha256.Size || presented == "" || len(presented) > CodexSubscriptionProxyMaxSecretLength {
		return false
	}
	return subtle.ConstantTimeCompare(HashCodexSubscriptionProxyAccessSecret(presented), storedHash) == 1
}

// BuildCodexSubscriptionProxyAccessURL 拼接 Codex 接入地址（含明文密钥，仅用于一次性展示）。
func BuildCodexSubscriptionProxyAccessURL(cfg CodexSubscriptionProxyConfig, secret string) string {
	return "http://" + cfg.ListenAddr() + CodexSubscriptionProxyCapabilityPrefix + secret + CodexSubscriptionProxyCapabilitySuffix
}
