package main

import (
	"log/slog"
	"strings"
	"testing"

	"sinuo/config"
	"sinuo/internal/tracking"
)

func TestSetupAccountPoolStore_WarnsCompactCoverageWhenUsageTrackingDisabled(t *testing.T) {
	app := NewApp()
	logs := &syncWriter{}
	app.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app.config = &config.Config{
		AccountPool: config.AccountPoolConfig{Enabled: true},
	}

	app.setupAccountPoolStore()

	if !strings.Contains(logs.String(), "Codex /v1/responses 与 /v1/responses/compact 将返回账号池未就绪错误") {
		t.Fatalf("expected compact coverage warning, got logs: %s", logs.String())
	}
}

func TestSetupAccountPoolStore_WarnsCompactCoverageWhenDatabaseUnavailable(t *testing.T) {
	app := NewApp()
	logs := &syncWriter{}
	app.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app.config = &config.Config{
		AccountPool: config.AccountPoolConfig{Enabled: true},
	}
	app.usageTracker = &tracking.UsageTracker{}

	app.setupAccountPoolStore()

	if !strings.Contains(logs.String(), "Codex /v1/responses 与 /v1/responses/compact 将返回账号池未就绪错误") {
		t.Fatalf("expected compact coverage warning, got logs: %s", logs.String())
	}
}
