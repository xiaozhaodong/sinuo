package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
	"cc-forwarder/internal/service"
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

func TestSoftFailurePolicy_页面全局设置保存后更新继承策略(t *testing.T) {
	app, db := newAccountPoolAPITestApp(t)
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
	ctx := context.Background()
	if err := app.settingsService.InitDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	app.settingsService.SetOnChangeCallback(app.applySettingsToConfig)
	for _, name := range []string{"inherited", "custom"} {
		for i := 0; i < 2; i++ {
			app.endpointManager.RecordSoftFailure(name, endpoint.SoftFailureScopeMessages, endpoint.SoftFailureCategoryRateLimit)
		}
	}
	if err := app.BatchUpdateSettings(BatchUpdateSettingsInput{Settings: []UpdateSettingInput{
		{Category: service.CategoryRequest, Key: "global_timeout", Value: "400s"},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := app.endpointManager.GetSoftFailureCounts("inherited", endpoint.SoftFailureScopeMessages)[endpoint.SoftFailureCategoryRateLimit]; got != 2 {
		t.Fatal("保存无关设置清空了继承计数")
	}
	if err := app.BatchUpdateSettings(BatchUpdateSettingsInput{Settings: []UpdateSettingInput{
		{Category: service.CategoryRequest, Key: "failure_time_window", Value: "10m"},
		{Category: service.CategoryRequest, Key: "failure_threshold", Value: "4"},
	}}); err != nil {
		t.Fatal(err)
	}
	if app.settingsService.GetDuration(ctx, service.CategoryRequest, "failure_time_window", 0) != 10*time.Minute ||
		app.settingsService.GetInt(ctx, service.CategoryRequest, "failure_threshold", 0) != 4 {
		t.Fatal("页面设置未持久化")
	}
	if app.endpointManager.SoftFailureThreshold("inherited") != 4 {
		t.Fatal("页面设置未发布到运行态")
	}
	if got := app.endpointManager.GetSoftFailureCounts("custom", endpoint.SoftFailureScopeMessages)[endpoint.SoftFailureCategoryRateLimit]; got != 2 {
		t.Fatal("保存全局设置改变了自定义端点计数")
	}
	if count, hit := app.endpointManager.RecordSoftFailure("inherited", endpoint.SoftFailureScopeMessages, endpoint.SoftFailureCategoryRateLimit); count != 1 || hit {
		t.Errorf("页面保存后仍沿用旧累计: count=%d hit=%v", count, hit)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.BatchUpdateSettings(BatchUpdateSettingsInput{Settings: []UpdateSettingInput{
		{Category: service.CategoryRequest, Key: "failure_threshold", Value: "5"},
	}}); err == nil {
		t.Fatal("未报告保存失败")
	}
	if app.endpointManager.SoftFailureThreshold("inherited") != 4 {
		t.Fatal("保存失败发布了新策略")
	}
	if got := app.endpointManager.GetSoftFailureCounts("inherited", endpoint.SoftFailureScopeMessages)[endpoint.SoftFailureCategoryRateLimit]; got != 1 {
		t.Errorf("保存失败改变了已有计数: %d", got)
	}
}
