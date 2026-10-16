package main

import (
	"context"
	"encoding/json"
	"testing"

	"sinuo/internal/service"
	"sinuo/internal/store"
)

type codexSearchAccountsStore struct {
	store.AccountPoolStore
	accounts []*store.UpstreamAccountRecord
}

func (s codexSearchAccountsStore) ListAccounts(context.Context, bool) ([]*store.UpstreamAccountRecord, error) {
	return s.accounts, nil
}

func newCodexSearchRoutingApp(t *testing.T) *App {
	t.Helper()
	app := newTrayModelRoutingApp(t, true)
	accounts := []*store.UpstreamAccountRecord{{ID: 1, AccountName: "test-oauth", ProviderType: "chatgpt_refresh_token", Enabled: true, CredentialRaw: "test-credential"}}
	app.accountPoolService = service.NewAccountPoolService(codexSearchAccountsStore{accounts: accounts}, nil)
	t.Cleanup(func() { _ = app.accountPoolService.Close() })
	app.codexModelRoutingService = service.NewCodexModelRoutingService(app.modelRoutingStore, app.accountPoolService.ListAccounts)
	app.codexModelRoutingService.SetWriteMutex(&app.routingMu)
	if err := app.codexModelRoutingService.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestCodexSearchAPI_KindRoundTripAndModelCount(t *testing.T) {
	app := newCodexSearchRoutingApp(t)
	var input CodexModelRouteBindingInput
	if err := json.Unmarshal([]byte(`{"kind":"search","model":"","account_ids":[1],"route_mode":"auto","enabled":true}`), &input); err != nil {
		t.Fatal(err)
	}
	binding, err := app.CreateCodexModelRouteBinding(input)
	if err != nil || binding.Kind != "search" || binding.Model != "server_tool:search" {
		t.Fatalf("%+v %v", binding, err)
	}
	wire, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(wire, &got); err != nil || got["kind"] != "search" {
		t.Fatalf("%s %v", wire, err)
	}
	settings, err := app.GetModelRoutingSettings()
	if err != nil || settings.CodexBindingCount != 0 {
		t.Fatalf("只有搜索时模型计数应为零: %+v %v", settings, err)
	}
	input.RouteMode, input.RouteTargetID, input.Note = "manual_fixed", 1, "updated"
	if err := app.UpdateCodexModelRouteBinding(binding.AssignmentID, input); err != nil {
		t.Fatal(err)
	}
	list, err := app.ListCodexModelRouteBindings()
	if err != nil || len(list) != 1 || list[0].Kind != "search" || list[0].Note != "updated" || list[0].RouteTargetID != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	if _, err := app.CreateCodexModelRouteBinding(CodexModelRouteBindingInput{Kind: "model", Model: "gpt-5", AccountIDs: []int64{1}, RouteMode: "auto", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	settings, err = app.GetModelRoutingSettings()
	if err != nil || settings.CodexBindingCount != 1 {
		t.Fatalf("只统计模型映射: %+v %v", settings, err)
	}
	trial, err := app.TestCodexModelRouting(CodexModelRoutingTestInput{Model: "server_tool:search"})
	if err != nil || trial.Assigned {
		t.Fatalf("试算不能通过保留键命中搜索: %+v %v", trial, err)
	}
}
