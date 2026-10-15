package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/endpoint"
	"sinuo/internal/service"
)

func apiPolicyInt(value int) *int { return &value }

func TestSoftFailurePolicy_账号接口创建更新与恢复默认(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	input := CreateUpstreamAccountInput{
		ProviderType: "api_key", AccountName: "policy-account", CredentialRaw: "fixture-policy", BaseURL: "https://example.test", Priority: 10, Enabled: true,
		SoftFailureWindowMinutes: apiPolicyInt(10), SoftFailureThreshold: apiPolicyInt(5),
	}
	if err := app.CreateUpstreamAccount(input); err != nil {
		t.Fatal(err)
	}
	list, err := app.GetUpstreamAccounts()
	if err != nil || len(list) != 1 {
		t.Fatalf("读取账号失败: %v", err)
	}
	if list[0].SoftFailureWindowMinutes == nil || *list[0].SoftFailureWindowMinutes != 10 || *list[0].SoftFailureThreshold != 5 {
		t.Fatal("账号接口丢失自定义策略")
	}
	input.SoftFailureWindowMinutes, input.SoftFailureThreshold = nil, nil
	if err := app.UpdateUpstreamAccount(list[0].ID, input); err != nil {
		t.Fatal(err)
	}
	list, err = app.GetUpstreamAccounts()
	if err != nil || list[0].SoftFailureWindowMinutes != nil || list[0].SoftFailureThreshold != nil {
		t.Fatalf("恢复默认失败: %v", err)
	}
	input.SoftFailureWindowMinutes = apiPolicyInt(1)
	if err := app.UpdateUpstreamAccount(list[0].ID, input); err == nil {
		t.Fatal("账号接口未拒绝不完整策略")
	}
}

func TestSoftFailurePolicy_端点接口创建更新与恢复默认(t *testing.T) {
	app, cleanup := newEndpointStorageAPITestApp(t)
	defer cleanup()
	input := CreateEndpointInput{
		Name: "policy-endpoint", URL: "https://example.test", Priority: 1, FailoverEnabled: true, TimeoutSeconds: 300,
		SoftFailureWindowMinutes: apiPolicyInt(2), SoftFailureThreshold: apiPolicyInt(1),
	}
	if err := app.CreateEndpointRecord(input); err != nil {
		t.Fatal(err)
	}
	list, err := app.GetEndpointRecords()
	if err != nil || len(list) != 1 {
		t.Fatalf("读取端点失败: %v", err)
	}
	if list[0].SoftFailureWindowMinutes == nil || *list[0].SoftFailureWindowMinutes != 2 || *list[0].SoftFailureThreshold != 1 {
		t.Fatal("端点接口丢失自定义策略")
	}
	input.SoftFailureWindowMinutes, input.SoftFailureThreshold = nil, nil
	if err := app.UpdateEndpointRecord(input.Name, input); err != nil {
		t.Fatal(err)
	}
	read, err := app.GetEndpointRecord(input.Name)
	if err != nil || read.SoftFailureWindowMinutes != nil || read.SoftFailureThreshold != nil {
		t.Fatalf("恢复默认失败: %v", err)
	}
	input.SoftFailureWindowMinutes, input.SoftFailureThreshold = apiPolicyInt(5), apiPolicyInt(0)
	if err := app.UpdateEndpointRecord(input.Name, input); err == nil {
		t.Fatal("端点接口未拒绝零阈值")
	}
}

func TestSoftFailurePolicy_JSON拒绝小数与非整数(t *testing.T) {
	for _, value := range []string{"1.5", "true", "[]", "\"3\""} {
		body := []byte(`{"soft_failure_window_minutes":` + value + `,"soft_failure_threshold":3}`)
		var account CreateUpstreamAccountInput
		var endpoint CreateEndpointInput
		if json.Unmarshal(body, &account) == nil || json.Unmarshal(body, &endpoint) == nil {
			t.Fatalf("错误接受 %s", value)
		}
	}
}

func TestSoftFailurePolicy_保存设置不改写软失败策略(t *testing.T) {
	app, _ := newAccountPoolAPITestApp(t)
	app.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	app.config = &config.Config{
		FailureTracker: config.FailureTrackerConfig{Enabled: true, TimeWindow: 5 * time.Minute, Threshold: 3},
		Endpoints: []config.EndpointConfig{
			{Name: "inherited", URL: "https://example.test"},
			{Name: "custom", URL: "https://example.test", SoftFailureWindowMinutes: apiPolicyInt(10), SoftFailureThreshold: apiPolicyInt(5)},
		},
	}
	app.endpointManager = endpoint.NewManager(app.config)
	t.Cleanup(app.endpointManager.Stop)
	if err := app.settingsService.InitDefaults(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.settingsService.SetOnChangeCallback(app.applySettingsToConfig)
	for _, name := range []string{"inherited", "custom"} {
		for range 2 {
			app.endpointManager.RecordSoftFailure(name, endpoint.SoftFailureScopeMessages, endpoint.SoftFailureCategoryRateLimit)
		}
	}
	for _, setting := range []UpdateSettingInput{
		{Category: service.CategoryRequest, Key: "global_timeout", Value: "400s"},
		{Category: service.CategoryRequest, Key: "eof_retry_hint", Value: "true"},
	} {
		if err := app.BatchUpdateSettings(BatchUpdateSettingsInput{Settings: []UpdateSettingInput{setting}}); err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]int{"inherited": 3, "custom": 5} {
			if got := app.endpointManager.SoftFailureThreshold(name); got != want {
				t.Fatalf("保存普通设置改变了 %s 阈值: want=%d got=%d", name, want, got)
			}
			if got := app.endpointManager.GetSoftFailureCounts(name, endpoint.SoftFailureScopeMessages)[endpoint.SoftFailureCategoryRateLimit]; got != 2 {
				t.Fatalf("保存普通设置改变了 %s 计数: %d", name, got)
			}
		}
	}
}
