package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"cc-forwarder/config"
	"cc-forwarder/internal/migration"
	timezonepolicy "cc-forwarder/internal/timezone"
	"cc-forwarder/internal/tracking"
	"cc-forwarder/internal/utils"
)

const desktopDefaultConfigFileName = "config.yaml"

func defaultDesktopConfigPath() string {
	return filepath.Join(utils.GetConfigDir(), desktopDefaultConfigFileName)
}

func ensureDesktopConfigFile(configPath string, defaultContent []byte) (created bool, err error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return false, fmt.Errorf("config path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return false, err
	}
	if _, err := os.Stat(configPath); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.WriteFile(configPath, defaultContent, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func resolveStartupConfigPath(explicitPath string, explicit bool, configDir string, defaultContent []byte) (string, bool, error) {
	if explicit && strings.TrimSpace(explicitPath) != "" {
		return explicitPath, false, nil
	}
	resolvedPath := filepath.Join(configDir, desktopDefaultConfigFileName)
	created, err := ensureDesktopConfigFile(resolvedPath, defaultContent)
	if err != nil {
		return "", false, err
	}
	return resolvedPath, created, nil
}

func (a *App) resolveStartupConfigPath(logger *slog.Logger) (string, error) {
	resolvedPath, created, err := resolveStartupConfigPath(a.configPath, a.configPathExplicit, utils.GetConfigDir(), defaultConfigContent)
	if err != nil {
		return "", err
	}
	if logger != nil {
		if a.configPathExplicit {
			logger.Info("📝 使用命令行指定配置", "config_file", resolvedPath)
		} else if created {
			logger.Info("📝 已初始化默认配置文件", "config_file", resolvedPath)
		} else {
			logger.Info("📝 使用用户目录配置文件", "config_file", resolvedPath)
		}
	}
	return resolvedPath, nil
}

func (a *App) applyDesktopRuntimePathOverrides(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.Logging.FilePath = filepath.Join(utils.GetLogDir(), "app.log")
	if strings.TrimSpace(cfg.UsageTracking.DatabasePath) == "" {
		cfg.UsageTracking.DatabasePath = filepath.Join(utils.GetDataDir(), "usage.db")
	}
}

func (a *App) prepareCoreDatabaseAndMigration(ctx context.Context) error {
	tempLogger := slog.Default()
	if err := utils.EnsureAppDirs(); err != nil {
		return fmt.Errorf("prepare application directories: %w", err)
	}
	resolvedConfigPath, err := a.resolveStartupConfigPath(tempLogger)
	if err != nil {
		return fmt.Errorf("prepare config file: %w", err)
	}
	a.configPath = resolvedConfigPath

	legacy, err := migration.LoadLegacyConfig(resolvedConfigPath)
	if err != nil {
		return err
	}
	globalTimezone := legacy.EffectiveGlobalTimezone()
	if legacy.DatabaseTimezone != "" && timezonepolicy.ResolveConfigured(legacy.DatabaseTimezone) != globalTimezone {
		return fmt.Errorf("usage_tracking.database.timezone %q conflicts with top-level timezone %q; remove it and use the top-level timezone", legacy.DatabaseTimezone, globalTimezone)
	}
	policy, err := timezonepolicy.New(globalTimezone)
	if err != nil {
		return fmt.Errorf("validate configured timezone: %w", err)
	}
	a.timezonePolicy = policy
	databasePath := legacy.ResolveDatabasePath(filepath.Join(utils.GetDataDir(), "usage.db"))
	databaseExisted := false
	if databasePath != ":memory:" {
		if _, statErr := os.Stat(databasePath); statErr == nil {
			databaseExisted = true
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect application database: %w", statErr)
		}
	}
	core, err := tracking.OpenCoreDatabase(tracking.DatabaseConfig{Type: "sqlite", DatabasePath: databasePath})
	if err != nil {
		return err
	}
	a.coreDatabase = core
	coordinator := &migration.Coordinator{
		DB: core.DB(), DatabasePath: databasePath, ConfigPath: resolvedConfigPath,
		DataDir: utils.GetDataDir(), DatabaseExisted: databaseExisted, Logger: tempLogger,
	}
	a.setMigrationCoordinator(coordinator)
	return a.prepareMigratedRuntime(ctx, coordinator)
}

func (a *App) loadRuntimeConfig(databasePath string) error {
	cfg, err := config.LoadConfig(a.configPath)
	if err != nil {
		return fmt.Errorf("load migrated runtime config: %w", err)
	}
	a.applyDesktopRuntimePathOverrides(cfg)
	cfg.UsageTracking.DatabasePath = databasePath
	if cfg.UsageTracking.Database != nil {
		cfg.UsageTracking.Database.Path = databasePath
	}
	a.config = cfg
	if a.timezonePolicy == nil {
		policy, err := timezonepolicy.New(cfg.Timezone)
		if err != nil {
			return err
		}
		a.timezonePolicy = policy
	} else if a.timezonePolicy.Name() != cfg.Timezone {
		return fmt.Errorf("runtime timezone %q differs from validated startup timezone %q", cfg.Timezone, a.timezonePolicy.Name())
	}
	return nil
}

const (
	timezoneSettingCategory = "system"
	timezoneSettingKey      = "last_seen_timezone"
)

// noteTimezoneChange 对比上次运行使用的时区并记录当前值。
// 时区变化只影响展示与业务日期分组口径，这里仅提示，不做任何数据处理。
func (a *App) noteTimezoneChange(ctx context.Context) {
	if a.settingsStore == nil || a.timezonePolicy == nil {
		return
	}
	current := a.timezonePolicy.Name()
	record, err := a.settingsStore.Get(ctx, timezoneSettingCategory, timezoneSettingKey)
	if err != nil {
		a.logger.Warn("读取上次运行时区失败", "error", err)
		return
	}
	if record != nil && record.Value != "" && record.Value != current {
		a.logger.Warn("⏰ 检测到时区变化，历史日统计将按新时区展示",
			"previous_timezone", record.Value,
			"current_timezone", current)
	}
	if record == nil || record.Value != current {
		if err := a.settingsStore.Set(ctx, timezoneSettingCategory, timezoneSettingKey, current); err != nil {
			a.logger.Warn("记录当前时区失败", "error", err)
		}
	}
}
