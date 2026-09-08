package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cc-forwarder/config"
	timezonepolicy "cc-forwarder/internal/timezone"

	"gopkg.in/yaml.v3"
)

type SourceMode string

const (
	SourceModeYAML   SourceMode = "yaml"
	SourceModeSQLite SourceMode = "sqlite"
)

type legacyDuration struct{ time.Duration }

func (d *legacyDuration) UnmarshalYAML(node *yaml.Node) error {
	if node == nil || strings.TrimSpace(node.Value) == "" {
		return nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(node.Value))
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

type LegacyCredential struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type LegacyEndpoint struct {
	Name                          string             `yaml:"name"`
	URL                           string             `yaml:"url"`
	Channel                       string             `yaml:"channel"`
	Group                         string             `yaml:"group"`
	GroupPriority                 int                `yaml:"group-priority"`
	Priority                      int                `yaml:"priority"`
	Token                         string             `yaml:"token"`
	APIKey                        string             `yaml:"api-key"`
	Tokens                        []LegacyCredential `yaml:"tokens"`
	APIKeys                       []LegacyCredential `yaml:"api-keys"`
	Headers                       map[string]string  `yaml:"headers"`
	Timeout                       legacyDuration     `yaml:"timeout"`
	Cooldown                      *legacyDuration    `yaml:"cooldown"`
	SupportsCountTokens           bool               `yaml:"supports_count_tokens"`
	ModelRewriteRules             string             `yaml:"model_rewrite_rules"`
	SoftFailureWindowMinutes      *int               `yaml:"soft_failure_window_minutes"`
	SoftFailureThreshold          *int               `yaml:"soft_failure_threshold"`
	Enabled                       *bool              `yaml:"enabled"`
	AvailabilityEnabled           *bool              `yaml:"availability_enabled"`
	FailoverEnabled               *bool              `yaml:"failover_enabled"`
	CostMultiplier                float64            `yaml:"cost_multiplier"`
	InputCostMultiplier           float64            `yaml:"input_cost_multiplier"`
	OutputCostMultiplier          float64            `yaml:"output_cost_multiplier"`
	CacheCreationCostMultiplier   float64            `yaml:"cache_creation_cost_multiplier"`
	CacheCreationCostMultiplier1h float64            `yaml:"cache_creation_cost_multiplier_1h"`
	CacheReadCostMultiplier       float64            `yaml:"cache_read_cost_multiplier"`
}

// UnmarshalYAML 阻止 YAML 将小数静默截断为整型策略值。
func (endpoint *LegacyEndpoint) UnmarshalYAML(node *yaml.Node) error {
	var fields map[string]yaml.Node
	if err := node.Decode(&fields); err != nil {
		return err
	}
	for _, name := range []string{"soft_failure_window_minutes", "soft_failure_threshold"} {
		field, exists := fields[name]
		if !exists {
			continue
		}
		for field.Kind == yaml.AliasNode && field.Alias != nil {
			field = *field.Alias
		}
		if field.Tag != "!!int" && field.Tag != "!!null" {
			return fmt.Errorf("%s 必须为整数或 null", name)
		}
	}
	type plainEndpoint LegacyEndpoint
	var decoded plainEndpoint
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	if err := config.ValidateSoftFailureOverrides(decoded.SoftFailureWindowMinutes, decoded.SoftFailureThreshold); err != nil {
		return err
	}
	*endpoint = LegacyEndpoint(decoded)
	return nil
}

type legacyConfigDocument struct {
	Timezone         string `yaml:"timezone"`
	EndpointsStorage struct {
		Type string `yaml:"type"`
	} `yaml:"endpoints_storage"`
	UsageTracking struct {
		DatabasePath string `yaml:"database_path"`
		Database     struct {
			Path     string `yaml:"path"`
			Timezone string `yaml:"timezone"`
		} `yaml:"database"`
	} `yaml:"usage_tracking"`
	GlobalTimeout legacyDuration   `yaml:"global_timeout"`
	Endpoints     []LegacyEndpoint `yaml:"endpoints"`
}

type LegacyConfig struct {
	Path             string
	Raw              []byte
	Root             yaml.Node
	SourceMode       SourceMode
	DatabasePath     string
	DatabaseTimezone string
	GlobalTimezone   string
	GlobalTimeout    time.Duration
	Endpoints        []LegacyEndpoint
}

func LoadLegacyConfig(path string) (*LegacyConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read legacy config: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse legacy config node: %w", err)
	}
	var document legacyConfigDocument
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse legacy config values: %w", err)
	}
	mode := SourceModeYAML
	if strings.EqualFold(strings.TrimSpace(document.EndpointsStorage.Type), string(SourceModeSQLite)) {
		mode = SourceModeSQLite
	}
	databasePath := strings.TrimSpace(document.UsageTracking.Database.Path)
	if databasePath == "" {
		databasePath = strings.TrimSpace(document.UsageTracking.DatabasePath)
	}
	return &LegacyConfig{
		Path:             path,
		Raw:              raw,
		Root:             root,
		SourceMode:       mode,
		DatabasePath:     databasePath,
		DatabaseTimezone: strings.TrimSpace(document.UsageTracking.Database.Timezone),
		GlobalTimezone:   strings.TrimSpace(document.Timezone),
		GlobalTimeout:    document.GlobalTimeout.Duration,
		Endpoints:        document.Endpoints,
	}, nil
}

// EffectiveGlobalTimezone 返回运行时展示时区：留空或 "local" 时跟随系统时区。
func (c *LegacyConfig) EffectiveGlobalTimezone() string {
	if c != nil {
		return timezonepolicy.ResolveConfigured(c.GlobalTimezone)
	}
	return timezonepolicy.SystemDefault()
}

// LegacyTrackingTimezone 仅用于解释迁移前无 offset 的历史数据库时间。
// 口径必须与旧版本 Tracker 的实际写入行为一致：
//   - 显式 IANA 名称：原样使用；
//   - 精确的 "Local"（Go time.LoadLocation 的特殊值，旧版合法）：旧版会使用系统
//     时区，这里探测出具体 IANA 名称固定使用；探测失败时报错，要求用户在配置中
//     显式指定，绝不静默按 Asia/Shanghai 解释；
//   - "local" / "LOCAL" 等非精确形式：旧版 time.LoadLocation 无法加载，Tracker
//     实际回退 Asia/Shanghai 写入，因此解释口径同样固定 Asia/Shanghai；
//   - 留空：旧版默认 Asia/Shanghai，保持钉死，不跟随当前系统时区。
func (c *LegacyConfig) LegacyTrackingTimezone() (string, error) {
	candidate := ""
	if c != nil {
		candidate = strings.TrimSpace(c.DatabaseTimezone)
		if candidate == "" {
			candidate = strings.TrimSpace(c.GlobalTimezone)
		}
	}
	if candidate == "" {
		return timezonepolicy.FallbackName, nil
	}
	if candidate == "Local" {
		name, err := timezonepolicy.DetectSystem()
		if err != nil {
			return "", fmt.Errorf(
				"legacy timezone %q relies on the system timezone but detection failed (%v); set an explicit IANA timezone in the config before migrating", candidate, err)
		}
		return name, nil
	}
	if strings.EqualFold(candidate, "local") {
		return timezonepolicy.FallbackName, nil
	}
	return candidate, nil
}

func (c *LegacyConfig) ResolveDatabasePath(defaultPath string) string {
	if c == nil || strings.TrimSpace(c.DatabasePath) == "" {
		return defaultPath
	}
	if filepath.IsAbs(c.DatabasePath) {
		return filepath.Clean(c.DatabasePath)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(c.Path), c.DatabasePath))
}

func boolDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func positiveOrDefault(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}
