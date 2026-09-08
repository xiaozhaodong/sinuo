package service

import (
	"context"
	"reflect"
	"testing"

	"cc-forwarder/internal/modelrouting"
)

func TestCodexModelRouting_MemberInputsUseOneAccountSet(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ids     []int64
		enabled map[int64]bool
		wantIDs []int64
	}{
		{"部分状态映射保留显式顺序", []int64{2, 1}, map[int64]bool{1: false}, []int64{2, 1}},
		{"仅状态映射确定成员", nil, map[int64]bool{2: true, 1: false}, []int64{1, 2}},
		{"旧格式默认参与", []int64{2, 1}, nil, []int64{2, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newCodexModelRoutingServiceForTest(t)
			ctx := context.Background()
			input := CodexModelRouteBindingInput{
				Model: "member-input", AccountIDs: tc.ids, MemberEnabled: tc.enabled,
				RouteMode: modelrouting.RouteModeManualFixed, RouteTargetID: 2, Enabled: true,
			}
			id, err := svc.CreateBinding(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			// 更新入口也必须使用同一份归一化结果。
			if err := svc.UpdateBinding(ctx, id, input); err != nil {
				t.Fatal(err)
			}
			bindings, err := svc.ListBindings(ctx)
			if err != nil || len(bindings) != 1 {
				t.Fatalf("读取绑定失败: %v", err)
			}
			if !reflect.DeepEqual(bindings[0].AccountIDs, tc.wantIDs) {
				t.Errorf("存储成员顺序不符: got=%v want=%v", bindings[0].AccountIDs, tc.wantIDs)
			}
			for _, accountID := range tc.wantIDs {
				wantEnabled := true
				if value, ok := tc.enabled[accountID]; ok {
					wantEnabled = value
				}
				if bindings[0].MemberEnabled[accountID] != wantEnabled {
					t.Errorf("账号 %d 参与状态不符", accountID)
				}
			}
			if err := svc.UpdateEnabled(ctx, true); err != nil {
				t.Fatal(err)
			}
			prepared, err := svc.Prepare(ctx, input.Model, "/v1/responses")
			if err != nil || len(prepared.Accounts) != 1 || prepared.Accounts[0].ID != 2 {
				t.Fatalf("合法固定目标不可用: accounts=%d err=%v", len(prepared.Accounts), err)
			}
		})
	}
}

func TestCodexModelRouting_InvalidMemberInputsDoNotPersist(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ids     []int64
		enabled map[int64]bool
	}{
		{"映射成员不存在", nil, map[int64]bool{999: true}},
		{"映射包含成员外账号", []int64{1}, map[int64]bool{1: true, 2: false}},
		{"显式成员不存在", []int64{999}, map[int64]bool{999: true}},
		{"状态映射不能掩盖重复成员", []int64{1, 1}, map[int64]bool{1: true}},
		{"状态映射不能掩盖非法成员", []int64{0, 1}, map[int64]bool{1: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newCodexModelRoutingServiceForTest(t)
			ctx := context.Background()
			input := CodexModelRouteBindingInput{Model: "invalid-members", AccountIDs: tc.ids, MemberEnabled: tc.enabled, RouteMode: modelrouting.RouteModeAuto, Enabled: true}
			if _, err := svc.CreateBinding(ctx, input); err == nil {
				t.Error("新建绑定接受了非法成员")
			}
			bindings, err := svc.ListBindings(ctx)
			if err != nil || len(bindings) != 0 {
				t.Fatalf("失败的新建残留了绑定: count=%d err=%v", len(bindings), err)
			}
			valid := CodexModelRouteBindingInput{Model: "kept-members", AccountIDs: []int64{1, 2}, RouteMode: modelrouting.RouteModeAuto, Enabled: true}
			id, err := svc.CreateBinding(ctx, valid)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.UpdateBinding(ctx, id, input); err == nil {
				t.Error("更新绑定接受了非法成员")
			}
			bindings, err = svc.ListBindings(ctx)
			if err != nil || len(bindings) != 1 || bindings[0].Model != valid.Model || !reflect.DeepEqual(bindings[0].AccountIDs, valid.AccountIDs) {
				t.Fatalf("失败的更新改变了已有绑定: %v", err)
			}
		})
	}
}
