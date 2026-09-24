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

func TestClaudeCodeEntrySettings_ModePersistenceAndLegacyDefault(t *testing.T) {
	_, _, st := newCodexSubscriptionProxySettingsTestEnv(t)
	svc := NewClaudeCodeEntrySettingsService(st)
	ctx := context.Background()

	// 加入 mode 字段之前保存的配置：读出来按 auto 处理
	if err := st.Set(ctx, CategoryClaudeCodeEntry, ClaudeCodeEntryConfigKey, `{"schema_version":1,"enabled":true,"listen_port":18088}`); err != nil {
		t.Fatalf("seed legacy row failed: %v", err)
	}
	legacy, err := svc.Load(ctx)
	if err != nil || legacy.Mode != "auto" {
		t.Fatalf("旧配置应按 auto 读取: cfg=%+v err=%v", legacy, err)
	}

	saved, err := svc.Save(ctx, ClaudeCodeEntryInput{Enabled: true, ListenPort: 18088, Mode: " API "})
	if err != nil || saved.Mode != "api" {
		t.Fatalf("模式应归一化保存: cfg=%+v err=%v", saved, err)
	}
	if loaded, err := svc.Load(ctx); err != nil || loaded.Mode != "api" {
		t.Fatalf("保存后读取模式不一致: cfg=%+v err=%v", loaded, err)
	}
	if _, err := svc.Save(ctx, ClaudeCodeEntryInput{ListenPort: 18088, Mode: "pool"}); !errors.Is(err, ErrClaudeCodeEntryInvalidConfig) {
		t.Fatalf("未知模式应被拒绝，got %v", err)
	}
}

func TestClaudeCodeEntrySettings_RequestClassRouting(t *testing.T) {
	_, _, st := newCodexSubscriptionProxySettingsTestEnv(t)
	svc := NewClaudeCodeEntrySettingsService(st)
	ctx := context.Background()

	defaults, err := svc.Load(ctx)
	if err != nil || defaults.RequestClassRouting || !reflect.DeepEqual(defaults.APIRequestClasses, []string{"subagent", "auxiliary"}) {
		t.Fatalf("默认关闭，预选子代理与后台辅助: cfg=%+v err=%v", defaults, err)
	}

	saved, err := svc.Save(ctx, ClaudeCodeEntryInput{
		ListenPort:          18088,
		RequestClassRouting: true,
		APIRequestClasses:   []string{" Workflow ", "subagent", "subagent", ""},
	})
	if err != nil || !saved.RequestClassRouting || !reflect.DeepEqual(saved.APIRequestClasses, []string{"subagent", "workflow"}) {
		t.Fatalf("类别应去重、归一化并按固定顺序保存: cfg=%+v err=%v", saved, err)
	}
	if loaded, err := svc.Load(ctx); err != nil || !reflect.DeepEqual(loaded, saved) {
		t.Fatalf("保存后读取不一致: loaded=%+v saved=%+v err=%v", loaded, saved, err)
	}
	if _, err := svc.Save(ctx, ClaudeCodeEntryInput{ListenPort: 18088, APIRequestClasses: []string{"main"}}); !errors.Is(err, ErrClaudeCodeEntryInvalidConfig) {
		t.Fatalf("main 不可配置为强制走 API，got %v", err)
	}
}
