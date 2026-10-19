package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"sinuo/config"
	"sinuo/internal/middleware"
	"sinuo/internal/service"
	"sinuo/internal/utils"
)

func TestLANAccessSettings_ValidateEffectiveBatch(t *testing.T) {
	app := newImageGenerationSettingsTestApp(t)
	app.config = &config.Config{}
	ctx := context.Background()

	err := app.validateLANAccessSettingUpdates(ctx, []UpdateSettingInput{{
		Category: service.CategoryServer, Key: "lan_access", Value: "true",
	}})
	if err == nil || !strings.Contains(err.Error(), "请先在「访问鉴权」中设置 Token") {
		t.Fatalf("expected missing-token validation error, got %v", err)
	}

	err = app.validateLANAccessSettingUpdates(ctx, []UpdateSettingInput{
		{Category: service.CategoryServer, Key: "lan_access", Value: "true"},
		{Category: service.CategoryAuth, Key: "token", Value: "  secret  "},
	})
	if err != nil {
		t.Fatalf("enabling LAN access with token should pass: %v", err)
	}

	if err := app.settingsStore.Set(ctx, service.CategoryServer, "lan_access", "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.settingsStore.Set(ctx, service.CategoryAuth, "token", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := app.validateLANAccessSettingUpdates(ctx, []UpdateSettingInput{{
		Category: service.CategoryAuth, Key: "token", Value: "   ",
	}}); err == nil {
		t.Fatal("clearing token while LAN access is enabled should fail")
	}
}

func TestLANAccessSettings_UpdateAndResetGuards(t *testing.T) {
	app := newImageGenerationSettingsTestApp(t)
	app.config = &config.Config{Auth: config.AuthConfig{Token: "secret"}}
	app.logger = slog.Default()
	app.lanAccessGate = middleware.NewLANAccessGate(app.logger)
	app.portManager = utils.NewPortManager(8087)
	app.lanAccessGate.Update(true, "secret")
	if err := app.settingsStore.Set(context.Background(), service.CategoryServer, "lan_access", "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetCategorySettings(service.CategoryAuth); err == nil {
		t.Fatal("resetting auth while LAN access is enabled should fail")
	}

	if err := app.UpdateSetting(UpdateSettingInput{Category: service.CategoryServer, Key: "lan_access", Value: "false"}); err != nil {
		t.Fatalf("saving LAN access=false failed: %v", err)
	}
	enabled, _ := app.lanAccessGate.State()
	if enabled {
		t.Fatal("saving LAN access=false should update the gate immediately")
	}
}

func TestGetPortInfo_ReportsLANRuntimeState(t *testing.T) {
	app := newImageGenerationSettingsTestApp(t)
	app.config = &config.Config{Server: config.ServerConfig{Port: 8087}}
	app.portManager = utils.NewPortManager(8087)
	app.portManager.SetActualPort(8090)
	app.portManager.SetLANAccessActive(true)
	app.lanAccessGate = middleware.NewLANAccessGate(slog.Default())
	app.lanAccessGate.Update(true, "secret")
	info := app.GetPortInfo()
	if !info.LANAccess || !info.LANAccessActive || !info.LANTokenConfigured || info.ActualPort != 8090 {
		t.Fatalf("unexpected port info: %+v", info)
	}
	if len(info.LANAddresses) == 0 {
		t.Log("no private LAN interface is available in this test environment")
	}
}
