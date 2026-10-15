package config

import (
	"os"
	"testing"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

func TestLoadConfig_RetentionDefaultsToPermanent(t *testing.T) {
	path := writeConfigFixture(t, "endpoints_storage:\n  type: sqlite\nusage_tracking:\n  enabled: true\n")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UsageTracking.RetentionDays != 0 || cfg.UsageTracking.CleanupInterval != 24*time.Hour {
		t.Fatalf("未配置保留天数应永久保留、每 24 小时清理: %+v", cfg.UsageTracking)
	}
}

func TestConfigValidateEndpointModelRewriteRules(t *testing.T) {
	tests := []struct {
		name    string
		rules   string
		wantErr bool
	}{
		{
			name:  "固定双路径精确规则",
			rules: `[{"paths":["/v1/messages","/v1/messages/count_tokens"],"match":"exact","from":"source","to":"target"}]`,
		},
		{
			name:    "缺少 count_tokens 路径",
			rules:   `[{"paths":["/v1/messages"],"match":"exact","from":"source","to":"target"}]`,
			wantErr: true,
		},
		{
			name:    "不允许 prefix",
			rules:   `[{"paths":["/v1/messages","/v1/messages/count_tokens"],"match":"prefix","from":"source","to":"target"}]`,
			wantErr: true,
		},
		{
			name:    "无效 JSON",
			rules:   `{"from":`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				EndpointsStorage: EndpointsStorageConfig{Type: "sqlite"},
				Endpoints: []EndpointConfig{{
					Name:              "test-endpoint",
					URL:               "https://api.example.com",
					Priority:          1,
					ModelRewriteRules: tt.rules,
				}},
			}

			err := cfg.validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected model rewrite validation error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("valid model rewrite rules rejected: %v", err)
			}
		})
	}
}

func TestLoadConfigIgnoresLegacyYAMLEndpoints(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "legacy-endpoints-*.yaml")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if _, err := file.WriteString("endpoints_storage:\n  type: sqlite\nendpoints:\n  - name: legacy\n    url: https://legacy.example.com\n    token: secret\n"); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close config: %v", err)
	}

	cfg, err := LoadConfig(file.Name())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Endpoints) != 0 {
		t.Fatalf("runtime endpoints must only come from SQLite, got %d YAML endpoints", len(cfg.Endpoints))
	}
}

func TestLoadConfigRejectsNonSQLiteEndpointStorage(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "endpoint-storage-*.yaml")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if _, err := file.WriteString("endpoints_storage:\n  type: yaml\n"); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close config: %v", err)
	}
	if _, err := LoadConfig(file.Name()); err == nil {
		t.Fatal("expected non-SQLite endpoint storage to be rejected")
	}
}

func TestLoadConfigTimezoneAuthority(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		wantTimezone string
		wantErr      bool
	}{
		{name: "missing follows system", yaml: "endpoints_storage:\n  type: sqlite\n", wantTimezone: timezonepolicy.SystemDefault()},
		{name: "local follows system", yaml: "timezone: local\nendpoints_storage:\n  type: sqlite\n", wantTimezone: timezonepolicy.SystemDefault()},
		{name: "valid IANA timezone", yaml: "timezone: America/New_York\nendpoints_storage:\n  type: sqlite\n", wantTimezone: "America/New_York"},
		{name: "invalid timezone", yaml: "timezone: Mars/Olympus_Mons\nendpoints_storage:\n  type: sqlite\n", wantErr: true},
		{name: "matching deprecated database timezone", yaml: "timezone: UTC\nendpoints_storage:\n  type: sqlite\nusage_tracking:\n  database:\n    type: sqlite\n    timezone: UTC\n", wantTimezone: "UTC"},
		{name: "both local not conflicting", yaml: "timezone: local\nendpoints_storage:\n  type: sqlite\nusage_tracking:\n  database:\n    type: sqlite\n    timezone: local\n", wantTimezone: timezonepolicy.SystemDefault()},
		{name: "local and Local not conflicting", yaml: "timezone: local\nendpoints_storage:\n  type: sqlite\nusage_tracking:\n  database:\n    type: sqlite\n    timezone: Local\n", wantTimezone: timezonepolicy.SystemDefault()},
		{name: "conflicting database timezone", yaml: "timezone: UTC\nendpoints_storage:\n  type: sqlite\nusage_tracking:\n  database:\n    type: sqlite\n    timezone: Asia/Shanghai\n", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFixture(t, tc.yaml)
			cfg, err := LoadConfig(path)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected timezone validation error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Timezone != tc.wantTimezone {
				t.Fatalf("timezone = %q, want %q", cfg.Timezone, tc.wantTimezone)
			}
		})
	}
}

func writeConfigFixture(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig_AccountPoolEnabledDefaultWhenMissing(t *testing.T) {
	configContent := `
endpoints_storage:
  type: "sqlite"
`

	tmpFile, err := os.CreateTemp("", "test-account-pool-default-*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	tmpFile.Close()

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.AccountPool.Enabled {
		t.Fatalf("expected account_pool.enabled=false when missing explicit config")
	}
}

func TestLoadConfig_EOFRetryHintMigration(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{
			name: "missing both defaults false",
			yaml: "endpoints_storage:\n  type: sqlite\n",
			want: false,
		},
		{
			name: "legacy true is preserved",
			yaml: "endpoints_storage:\n  type: sqlite\nrequest_suspend:\n  eof_retry_hint: true\n",
			want: true,
		},
		{
			name: "new explicit false overrides legacy true",
			yaml: "endpoints_storage:\n  type: sqlite\nstreaming:\n  eof_retry_hint: false\nrequest_suspend:\n  eof_retry_hint: true\n",
			want: false,
		},
		{
			name: "new true overrides legacy false",
			yaml: "endpoints_storage:\n  type: sqlite\nstreaming:\n  eof_retry_hint: true\nrequest_suspend:\n  eof_retry_hint: false\n",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "eof-retry-hint-*.yaml")
			if err != nil {
				t.Fatalf("create config: %v", err)
			}
			if _, err := file.WriteString(tt.yaml); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if err := file.Close(); err != nil {
				t.Fatalf("close config: %v", err)
			}

			cfg, err := LoadConfig(file.Name())
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.Streaming.EOFRetryHint != tt.want {
				t.Fatalf("streaming.eof_retry_hint = %v, want %v", cfg.Streaming.EOFRetryHint, tt.want)
			}
		})
	}
}

func TestLoadConfig_AccountPoolEnabledExplicitFalse(t *testing.T) {
	configContent := `
endpoints_storage:
  type: "sqlite"
account_pool:
  enabled: false
`

	tmpFile, err := os.CreateTemp("", "test-account-pool-false-*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	tmpFile.Close()

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.AccountPool.Enabled {
		t.Fatalf("expected account_pool.enabled=false when explicitly configured false")
	}
}

func TestLoadConfig_AccountPoolEnabledExplicitTrue(t *testing.T) {
	configContent := `
endpoints_storage:
  type: "sqlite"
account_pool:
  enabled: true
`

	tmpFile, err := os.CreateTemp("", "test-account-pool-true-*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	tmpFile.Close()

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if !cfg.AccountPool.Enabled {
		t.Fatalf("expected account_pool.enabled=true when explicitly configured true")
	}
}

func TestLoadConfig_AccountPoolFailurePolicyDefaults(t *testing.T) {
	configContent := `
endpoints_storage:
  type: "sqlite"
`

	tmpFile, err := os.CreateTemp("", "test-account-pool-failure-defaults-*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	tmpFile.Close()

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if got := cfg.AccountPool.FailurePolicy.SoftFailureWindow; got != 5*time.Minute {
		t.Fatalf("expected soft_failure_window=5m, got %v", got)
	}
	if got := cfg.AccountPool.FailurePolicy.SoftFailureThreshold; got != 3 {
		t.Fatalf("expected soft_failure_threshold=3, got %d", got)
	}
	if cfg.AccountPool.FailurePolicy.RespectRetryAfter == nil || !*cfg.AccountPool.FailurePolicy.RespectRetryAfter {
		t.Fatalf("expected respect_retry_after=true by default, got %+v", cfg.AccountPool.FailurePolicy.RespectRetryAfter)
	}
	if got := cfg.AccountPool.FailurePolicy.LocalNoAvailableProvidersMarker; got != "no_available_providers::ccf_local" {
		t.Fatalf("unexpected local marker: %q", got)
	}
	if got := cfg.AccountPool.FailurePolicy.Cooldowns.ConnectionFailure; got != 90*time.Second {
		t.Fatalf("expected connection_failure=90s, got %v", got)
	}
	if got := cfg.AccountPool.FailurePolicy.Cooldowns.ProcessingFailure; got != 60*time.Second {
		t.Fatalf("expected processing_failure=60s, got %v", got)
	}
	if got := cfg.AccountPool.FailurePolicy.Cooldowns.RateLimitDefault; got != 180*time.Second {
		t.Fatalf("expected rate_limit_default=180s, got %v", got)
	}
	if got := cfg.AccountPool.FailurePolicy.Cooldowns.ServerError; got != 120*time.Second {
		t.Fatalf("expected server_error=120s, got %v", got)
	}
}

func TestLoadConfig_AccountPoolFailurePolicyExplicitValues(t *testing.T) {
	configContent := `
endpoints_storage:
  type: "sqlite"
account_pool:
  enabled: true
  failure_policy:
    soft_failure_window: "2m"
    soft_failure_threshold: 5
    respect_retry_after: false
    local_no_available_providers_marker: "no_available_providers::custom_marker"
    cooldowns:
      connection_failure: "30s"
      processing_failure: "45s"
      rate_limit_default: "75s"
      server_error: "95s"
`

	tmpFile, err := os.CreateTemp("", "test-account-pool-failure-explicit-*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	tmpFile.Close()

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if got := cfg.AccountPool.FailurePolicy.SoftFailureWindow; got != 2*time.Minute {
		t.Fatalf("expected soft_failure_window=2m, got %v", got)
	}
	if got := cfg.AccountPool.FailurePolicy.SoftFailureThreshold; got != 5 {
		t.Fatalf("expected soft_failure_threshold=5, got %d", got)
	}
	if cfg.AccountPool.FailurePolicy.RespectRetryAfter == nil || *cfg.AccountPool.FailurePolicy.RespectRetryAfter {
		t.Fatalf("expected respect_retry_after=false, got %+v", cfg.AccountPool.FailurePolicy.RespectRetryAfter)
	}
	if got := cfg.AccountPool.FailurePolicy.LocalNoAvailableProvidersMarker; got != "no_available_providers::custom_marker" {
		t.Fatalf("unexpected local marker: %q", got)
	}
	if got := cfg.AccountPool.FailurePolicy.Cooldowns.ConnectionFailure; got != 30*time.Second {
		t.Fatalf("expected connection_failure=30s, got %v", got)
	}
	if got := cfg.AccountPool.FailurePolicy.Cooldowns.ProcessingFailure; got != 45*time.Second {
		t.Fatalf("expected processing_failure=45s, got %v", got)
	}
	if got := cfg.AccountPool.FailurePolicy.Cooldowns.RateLimitDefault; got != 75*time.Second {
		t.Fatalf("expected rate_limit_default=75s, got %v", got)
	}
	if got := cfg.AccountPool.FailurePolicy.Cooldowns.ServerError; got != 95*time.Second {
		t.Fatalf("expected server_error=95s, got %v", got)
	}
}
