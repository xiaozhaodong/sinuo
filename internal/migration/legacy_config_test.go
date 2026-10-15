package migration

import (
	"path/filepath"
	"testing"
	"time"

	timezonepolicy "cc-forwarder/internal/timezone"
)

func TestLoadLegacyConfigFixture(t *testing.T) {
	config, err := LoadLegacyConfig(filepath.Join("testdata", "legacy_config.yml"))
	if err != nil {
		t.Fatalf("LoadLegacyConfig() error = %v", err)
	}
	if config.SourceMode != SourceModeYAML {
		t.Fatalf("SourceMode = %q, want yaml", config.SourceMode)
	}
	if len(config.Endpoints) != 5 {
		t.Fatalf("endpoint count = %d, want 5", len(config.Endpoints))
	}
	if got := config.Endpoints[0].Timeout.Duration; got != 45*time.Second {
		t.Fatalf("first endpoint timeout = %v, want 45s", got)
	}
	if got := config.ResolveDatabasePath("fallback.db"); got != "/tmp/sinuo-migration-fixture/usage.db" {
		t.Fatalf("ResolveDatabasePath() = %q", got)
	}
}

func TestLegacyTimezoneResolution(t *testing.T) {
	explicit := &LegacyConfig{GlobalTimezone: "America/New_York"}
	if got := explicit.EffectiveGlobalTimezone(); got != "America/New_York" {
		t.Fatalf("explicit effective timezone = %q", got)
	}
	if got, err := explicit.LegacyTrackingTimezone(); err != nil || got != "America/New_York" {
		t.Fatalf("explicit legacy timezone = %q, err = %v", got, err)
	}

	// 留空：旧版默认 Asia/Shanghai，历史数据解释必须钉死，不跟随系统时区。
	empty := &LegacyConfig{}
	if got, err := empty.LegacyTrackingTimezone(); err != nil || got != "Asia/Shanghai" {
		t.Fatalf("empty legacy timezone = %q, err = %v", got, err)
	}

	// 旧版合法的 Go 特殊值 "Local"：旧版按系统时区写入，解释时必须探测出具体 IANA 名称。
	system, detectErr := timezonepolicy.DetectSystem()
	local := &LegacyConfig{GlobalTimezone: "Local"}
	got, err := local.LegacyTrackingTimezone()
	if detectErr == nil {
		if err != nil || got != system {
			t.Fatalf("Local legacy timezone = %q, err = %v, want %q", got, err, system)
		}
	} else if err == nil {
		t.Fatal("Local legacy timezone should error when system detection fails")
	}

	// 非精确的 "local"：旧版 LoadLocation 加载失败并回退 Asia/Shanghai，解释口径保持一致。
	for _, value := range []string{"local", "LOCAL"} {
		inexact := &LegacyConfig{GlobalTimezone: value}
		if got, err := inexact.LegacyTrackingTimezone(); err != nil || got != "Asia/Shanghai" {
			t.Fatalf("%q legacy timezone = %q, err = %v, want Asia/Shanghai", value, got, err)
		}
	}

	// database.timezone 优先于全局配置。
	database := &LegacyConfig{GlobalTimezone: "UTC", DatabaseTimezone: "Asia/Tokyo"}
	if got, err := database.LegacyTrackingTimezone(); err != nil || got != "Asia/Tokyo" {
		t.Fatalf("database legacy timezone = %q, err = %v", got, err)
	}
}
