// Package service - Claude Code 专用入口配置服务
// 创建时间: 2026-09-24
//
// Claude Code 以 claude.ai 订阅登录、ANTHROPIC_BASE_URL 指向本入口时，请求携带自己的 OAuth token；
// 订阅直通模式直连 Anthropic 官方，端点模式与主端口一样走端点调度。配置以单行 JSON 存在 settings 表的
// claude_code_entry.config，由本服务独占（通用设置接口不可写）。
// 监听地址固定为 127.0.0.1；端口写进 Claude Code 配置后不能漂移，被占用时启动失败而不是换端口。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"

	"sinuo/internal/endpoint"
	"sinuo/internal/store"
)

const (
	// CategoryClaudeCodeEntry 由 ClaudeCodeEntrySettingsService 独占
	CategoryClaudeCodeEntry = "claude_code_entry"
	// ClaudeCodeEntryConfigKey 单行配置的 key
	ClaudeCodeEntryConfigKey = "config"
	// ClaudeCodeEntrySchemaVersion 当前配置结构版本；不匹配视为损坏
	ClaudeCodeEntrySchemaVersion = 1

	// ClaudeCodeEntryListenHost 固定回环地址
	ClaudeCodeEntryListenHost = "127.0.0.1"
	// ClaudeCodeEntryDefaultPort 默认端口（Codex 订阅代理默认 28088，两者错开）
	ClaudeCodeEntryDefaultPort = 18088
)

var (
	// ErrClaudeCodeEntryConfigCorrupted 已存配置无法解析或字段非法；保存会被拒绝，不覆盖
	ErrClaudeCodeEntryConfigCorrupted = errors.New("Claude Code 入口配置损坏，已拒绝覆盖")
	// ErrClaudeCodeEntryInvalidConfig 输入不满足校验规则
	ErrClaudeCodeEntryInvalidConfig = errors.New("Claude Code 入口配置无效")
)

// ClaudeCodeEntryConfig 持久化的单行配置
type ClaudeCodeEntryConfig struct {
	SchemaVersion int  `json:"schema_version"`
	Enabled       bool `json:"enabled"`
	ListenPort    int  `json:"listen_port"`
	// Mode 接入模式：subscription / endpoint；缺省与旧版 auto 按订阅直通，旧版 api 按端点
	Mode string `json:"mode"`
}

// ClaudeCodeEntryInput 保存输入
type ClaudeCodeEntryInput struct {
	Enabled    bool
	ListenPort int
	Mode       string
}

// ListenAddr 监听地址 host:port
func (c ClaudeCodeEntryConfig) ListenAddr() string {
	return net.JoinHostPort(ClaudeCodeEntryListenHost, strconv.Itoa(c.ListenPort))
}

// BaseURL Claude Code 的 ANTHROPIC_BASE_URL
func (c ClaudeCodeEntryConfig) BaseURL() string {
	return "http://" + c.ListenAddr()
}

// DefaultClaudeCodeEntryConfig 未初始化时的默认值（不落库）
func DefaultClaudeCodeEntryConfig() ClaudeCodeEntryConfig {
	return ClaudeCodeEntryConfig{
		SchemaVersion: ClaudeCodeEntrySchemaVersion,
		Enabled:       false,
		ListenPort:    ClaudeCodeEntryDefaultPort,
		Mode:          endpoint.ClaudeCodeEntryModeSubscription,
	}
}

// ClaudeCodeEntrySettingsService 专用配置服务；本进程内串行化写入
type ClaudeCodeEntrySettingsService struct {
	store store.SettingsStore
	mu    sync.Mutex
}

// NewClaudeCodeEntrySettingsService 创建专用配置服务
func NewClaudeCodeEntrySettingsService(st store.SettingsStore) *ClaudeCodeEntrySettingsService {
	return &ClaudeCodeEntrySettingsService{store: st}
}

// Load 读取当前配置；缺行返回默认值；损坏或读取失败返回错误
func (s *ClaudeCodeEntrySettingsService) Load(ctx context.Context) (ClaudeCodeEntryConfig, error) {
	record, err := s.store.Get(ctx, CategoryClaudeCodeEntry, ClaudeCodeEntryConfigKey)
	if err != nil {
		return ClaudeCodeEntryConfig{}, fmt.Errorf("读取 Claude Code 入口配置失败: %w", err)
	}
	if record == nil {
		return DefaultClaudeCodeEntryConfig(), nil
	}
	return parseClaudeCodeEntryConfig(record.Value)
}

// Save 校验后整体覆盖保存；已存配置损坏时拒绝覆盖，避免掩盖问题
func (s *ClaudeCodeEntrySettingsService) Save(ctx context.Context, input ClaudeCodeEntryInput) (ClaudeCodeEntryConfig, error) {
	next, err := normalizeClaudeCodeEntryConfig(ClaudeCodeEntryConfig{
		SchemaVersion: ClaudeCodeEntrySchemaVersion,
		Enabled:       input.Enabled,
		ListenPort:    input.ListenPort,
		Mode:          input.Mode,
	})
	if err != nil {
		return ClaudeCodeEntryConfig{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.Load(ctx); err != nil {
		return ClaudeCodeEntryConfig{}, err
	}
	value, err := json.Marshal(next)
	if err != nil {
		return ClaudeCodeEntryConfig{}, fmt.Errorf("序列化 Claude Code 入口配置失败: %w", err)
	}
	if err := s.store.Set(ctx, CategoryClaudeCodeEntry, ClaudeCodeEntryConfigKey, string(value)); err != nil {
		return ClaudeCodeEntryConfig{}, fmt.Errorf("保存 Claude Code 入口配置失败: %w", err)
	}
	return next, nil
}

func parseClaudeCodeEntryConfig(raw string) (ClaudeCodeEntryConfig, error) {
	var cfg ClaudeCodeEntryConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return ClaudeCodeEntryConfig{}, fmt.Errorf("%w: %v", ErrClaudeCodeEntryConfigCorrupted, err)
	}
	if cfg.SchemaVersion != ClaudeCodeEntrySchemaVersion {
		return ClaudeCodeEntryConfig{}, fmt.Errorf("%w: 不支持的 schema_version=%d", ErrClaudeCodeEntryConfigCorrupted, cfg.SchemaVersion)
	}
	normalized, err := normalizeClaudeCodeEntryConfig(cfg)
	if err != nil {
		return ClaudeCodeEntryConfig{}, fmt.Errorf("%w: %v", ErrClaudeCodeEntryConfigCorrupted, err)
	}
	return normalized, nil
}

// normalizeClaudeCodeEntryConfig 归一化并校验字段；Mode 空值与旧版 auto 按订阅直通
func normalizeClaudeCodeEntryConfig(cfg ClaudeCodeEntryConfig) (ClaudeCodeEntryConfig, error) {
	if cfg.ListenPort < 1 || cfg.ListenPort > 65535 {
		return cfg, fmt.Errorf("%w: 端口必须在 1-65535 之间", ErrClaudeCodeEntryInvalidConfig)
	}
	cfg.Mode = endpoint.NormalizeClaudeCodeEntryMode(cfg.Mode)
	if !endpoint.IsValidClaudeCodeEntryMode(cfg.Mode) {
		return cfg, fmt.Errorf("%w: 模式只能是 subscription / endpoint", ErrClaudeCodeEntryInvalidConfig)
	}
	return cfg, nil
}
