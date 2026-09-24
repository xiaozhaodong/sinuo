package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestClaudeCodeEntrySettings_DefaultSaveAndLoad(t *testing.T) {
	_, generic, st := newCodexSubscriptionProxySettingsTestEnv(t)
	svc := NewClaudeCodeEntrySettingsService(st)
	ctx := context.Background()

	cfg, err := svc.Load(ctx)
	if err != nil {
		t.Fatalf("load default failed: %v", err)
	}
	if cfg.Enabled || cfg.ListenPort != ClaudeCodeEntryDefaultPort {
		t.Fatalf("未初始化时应返回默认值（关闭，%d），got %+v", ClaudeCodeEntryDefaultPort, cfg)
	}
	if cfg.BaseURL() != "http://127.0.0.1:18088" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL())
	}

	saved, err := svc.Save(ctx, ClaudeCodeEntryInput{Enabled: true, ListenPort: 19000})
	if err != nil {
		t.Fatalf("save failed: %v", err)
	}
	loaded, err := svc.Load(ctx)
	if err != nil || !reflect.DeepEqual(loaded, saved) || !loaded.Enabled || loaded.ListenPort != 19000 {
		t.Fatalf("保存后读取不一致: saved=%+v loaded=%+v err=%v", saved, loaded, err)
	}

	if err := generic.Set(ctx, CategoryClaudeCodeEntry, ClaudeCodeEntryConfigKey, "{}"); !errors.Is(err, ErrProtectedSettingsCategory) {
		t.Fatalf("通用设置接口必须拒绝写入入口配置，got %v", err)
	}
}

func TestClaudeCodeEntrySettings_RejectsInvalidPortAndCorruptedRow(t *testing.T) {
	_, _, st := newCodexSubscriptionProxySettingsTestEnv(t)
	svc := NewClaudeCodeEntrySettingsService(st)
	ctx := context.Background()

	for _, port := range []int{0, -1, 65536} {
		if _, err := svc.Save(ctx, ClaudeCodeEntryInput{Enabled: true, ListenPort: port}); !errors.Is(err, ErrClaudeCodeEntryInvalidConfig) {
			t.Fatalf("端口 %d 应被拒绝，got %v", port, err)
		}
	}

	if err := st.Set(ctx, CategoryClaudeCodeEntry, ClaudeCodeEntryConfigKey, `{"schema_version":1,"enabled":true,"listen_port":0}`); err != nil {
		t.Fatalf("seed corrupted row failed: %v", err)
	}
	if _, err := svc.Load(ctx); !errors.Is(err, ErrClaudeCodeEntryConfigCorrupted) {
		t.Fatalf("损坏配置应报错，got %v", err)
	}
	if _, err := svc.Save(ctx, ClaudeCodeEntryInput{ListenPort: 18088}); !errors.Is(err, ErrClaudeCodeEntryConfigCorrupted) {
		t.Fatalf("已存配置损坏时必须拒绝覆盖，got %v", err)
	}
}

func TestClaudeCodeEntrySettings_ModePersistenceAndLegacyModes(t *testing.T) {
	_, _, st := newCodexSubscriptionProxySettingsTestEnv(t)
	svc := NewClaudeCodeEntrySettingsService(st)
	ctx := context.Background()

	defaults, err := svc.Load(ctx)
	if err != nil || defaults.Mode != "subscription" {
		t.Fatalf("未初始化时默认订阅直通: cfg=%+v err=%v", defaults, err)
	}

	// 旧版配置：没有 mode 字段、按模型混用的 auto、仅 API 的 api，以及已取消的请求类别字段
	legacyRows := map[string]string{
		`{"schema_version":1,"enabled":true,"listen_port":18088}`:                                                 "subscription",
		`{"schema_version":1,"enabled":true,"listen_port":18088,"mode":"auto","request_class_routing":true}`:      "subscription",
		`{"schema_version":1,"enabled":true,"listen_port":18088,"mode":"api","api_request_classes":["subagent"]}`: "endpoint",
	}
	for raw, want := range legacyRows {
		if err := st.Set(ctx, CategoryClaudeCodeEntry, ClaudeCodeEntryConfigKey, raw); err != nil {
			t.Fatalf("seed legacy row failed: %v", err)
		}
		legacy, err := svc.Load(ctx)
		if err != nil || legacy.Mode != want {
			t.Fatalf("旧配置 %s 应按 %s 读取: cfg=%+v err=%v", raw, want, legacy, err)
		}
	}

	saved, err := svc.Save(ctx, ClaudeCodeEntryInput{Enabled: true, ListenPort: 18088, Mode: " Endpoint "})
	if err != nil || saved.Mode != "endpoint" {
		t.Fatalf("模式应归一化保存: cfg=%+v err=%v", saved, err)
	}
	if loaded, err := svc.Load(ctx); err != nil || loaded.Mode != "endpoint" {
		t.Fatalf("保存后读取模式不一致: cfg=%+v err=%v", loaded, err)
	}
	if _, err := svc.Save(ctx, ClaudeCodeEntryInput{ListenPort: 18088, Mode: "pool"}); !errors.Is(err, ErrClaudeCodeEntryInvalidConfig) {
		t.Fatalf("未知模式应被拒绝，got %v", err)
	}
}
