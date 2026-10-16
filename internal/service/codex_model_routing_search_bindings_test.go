package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"sinuo/internal/accountauth"
	"sinuo/internal/modelrouting"
	"sinuo/internal/store"
)

func newCodexSearchService(t *testing.T) (*CodexModelRoutingService, []*store.UpstreamAccountRecord) {
	t.Helper()
	s := newCodexModelRoutingServiceForTest(t)
	accounts := []*store.UpstreamAccountRecord{
		{ID: 1, AccountName: "oauth-one", ProviderType: accountauth.ProviderChatGPTRefreshToken, Enabled: true, CredentialRaw: "test-oauth-one"},
		{ID: 2, AccountName: "api-two", ProviderType: accountauth.ProviderAPIKey, Enabled: true, CredentialRaw: "test-api-two"},
		{ID: 3, AccountName: "oauth-three", ProviderType: "oauth", Enabled: true, CredentialRaw: "test-oauth-three"},
	}
	s.listAccounts = func(context.Context, bool) ([]*store.UpstreamAccountRecord, error) { return accounts, nil }
	return s, accounts
}

func codexSearchInput() CodexModelRouteBindingInput {
	return CodexModelRouteBindingInput{Kind: modelrouting.BindingKindSearch, AccountIDs: []int64{1, 3}, RouteMode: modelrouting.RouteModeAuto, Enabled: true}
}

func TestCodexSearchBinding_AccountReadBeforeTransaction(t *testing.T) {
	s, accounts := newCodexSearchService(t)
	s.listAccounts = func(ctx context.Context, _ bool) ([]*store.UpstreamAccountRecord, error) {
		if _, err := s.store.GetSettings(ctx); err != nil {
			return nil, err
		}
		return accounts, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.CreateBinding(ctx, codexSearchInput()); err != nil {
		t.Fatalf("单连接库内账号读取不得被绑定事务阻塞: %v", err)
	}
}

func TestCodexSearchBinding_NormalizationAndValidation(t *testing.T) {
	for _, tc := range []struct{ kind, model, want string }{
		{"model", modelrouting.SearchKey, "模型名不能以保留前缀"},
		{"", "SERVER_TOOL:WEB", "模型名不能以保留前缀"},
		{"other", "gpt-5", "不支持的映射类型"},
	} {
		in := codexSearchInput()
		in.Kind, in.Model = tc.kind, tc.model
		if _, err := normalizeCodexBindingInput(in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	in := codexSearchInput()
	in.Model = "ignored"
	got, err := normalizeCodexBindingInput(in)
	if err != nil || got.Kind != "search" || got.Model != modelrouting.SearchKey {
		t.Fatalf("搜索类型必须忽略请求模型名: %+v %v", got, err)
	}
	s, _ := newCodexSearchService(t)
	in.AccountIDs = []int64{1, 2}
	if _, err := s.CreateBinding(context.Background(), in); err == nil || !strings.Contains(err.Error(), "联网搜索只能使用 ChatGPT OAuth 账号：账号 2 不是 OAuth 账号") {
		t.Fatalf("写入必须拒绝 api_key: %v", err)
	}
	bindings, err := s.ListBindings(context.Background())
	if err != nil || len(bindings) != 0 {
		t.Fatalf("拒绝写入后不应残留映射: %+v %v", bindings, err)
	}
}

func TestCodexSearchBinding_UpdateKindAndList(t *testing.T) {
	s, _ := newCodexSearchService(t)
	ctx := context.Background()
	model := CodexModelRouteBindingInput{Model: "a-model", AccountIDs: []int64{1}, RouteMode: "auto", Enabled: true}
	modelID, err := s.CreateBinding(ctx, model)
	if err != nil {
		t.Fatal(err)
	}
	in := codexSearchInput()
	searchID, err := s.CreateBinding(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBinding(ctx, in); err == nil || !strings.Contains(err.Error(), "联网搜索映射已经存在") {
		t.Fatalf("搜索映射只能有一条: %v", err)
	}
	model.Kind = "search"
	if err := s.UpdateBinding(ctx, modelID, model); err == nil || !strings.Contains(err.Error(), "映射类型创建后不能修改") {
		t.Fatalf("模型映射不能改为搜索: %v", err)
	}
	in.Kind, in.Model = "model", "another-model"
	if err := s.UpdateBinding(ctx, searchID, in); err == nil || !strings.Contains(err.Error(), "映射类型创建后不能修改") {
		t.Fatalf("搜索映射不能改为模型: %v", err)
	}
	in.Kind, in.Model, in.Note = "", modelrouting.SearchKey, "旧调用保留搜索类型"
	in.RouteMode, in.RouteTargetID = "manual_preferred", 3
	if err := s.UpdateBinding(ctx, searchID, in); err != nil {
		t.Fatal(err)
	}
	model.Kind = ""
	if err := s.UpdateBinding(ctx, modelID, model); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListBindings(ctx)
	if err != nil || len(list) != 2 || list[0].Kind != "search" || list[0].AssignmentID != searchID || list[0].Note != in.Note || list[0].RouteTargetID != 3 || list[1].Kind != "model" {
		t.Fatalf("类型、排序、旧调用更新应往返保留: %+v %v", list, err)
	}
	names, err := s.ModelsUsingAccount(ctx, 3)
	if err != nil || len(names) != 1 || names[0] != "联网搜索" {
		t.Fatalf("删除守卫不得暴露保留键: %+v %v", names, err)
	}
}
