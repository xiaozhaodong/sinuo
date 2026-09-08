package store

import (
	"context"
	"testing"
	"time"
)

func policyValue(value int) *int { return &value }

func assertStoredPolicy(t *testing.T, window, threshold *int, wantWindow, wantThreshold int) {
	t.Helper()
	if window == nil || threshold == nil || *window != wantWindow || *threshold != wantThreshold {
		t.Fatalf("独立失败策略读回不符: window=%v threshold=%v", window, threshold)
	}
}

func TestSQLiteSoftFailurePolicy_账号完整读写与恢复默认(t *testing.T) {
	ctx := context.Background()
	st := newTestSQLiteAccountPoolStore(t)
	account := mustCreateTestAccount(t, st, "policy-account", 10)
	if account.SoftFailureWindowMinutes != nil || account.SoftFailureThreshold != nil {
		t.Fatal("新账号应继承全局策略")
	}
	account.SoftFailureWindowMinutes, account.SoftFailureThreshold = policyValue(10), policyValue(5)
	if err := st.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	read, err := st.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertStoredPolicy(t, read.SoftFailureWindowMinutes, read.SoftFailureThreshold, 10, 5)
	for _, list := range []func() ([]*UpstreamAccountRecord, error){
		func() ([]*UpstreamAccountRecord, error) { return st.ListAccounts(ctx, true) },
		func() ([]*UpstreamAccountRecord, error) { return st.ListSchedulableAccounts(ctx, time.Now()) },
	} {
		accounts, err := list()
		if err != nil || len(accounts) != 1 {
			t.Fatalf("列表读取失败: len=%d err=%v", len(accounts), err)
		}
		assertStoredPolicy(t, accounts[0].SoftFailureWindowMinutes, accounts[0].SoftFailureThreshold, 10, 5)
	}
	account.SoftFailureWindowMinutes, account.SoftFailureThreshold = nil, nil
	if err := st.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	read, err = st.GetAccount(ctx, account.ID)
	if err != nil || read.SoftFailureWindowMinutes != nil || read.SoftFailureThreshold != nil {
		t.Fatalf("恢复默认失败: %v", err)
	}
}

func TestSQLiteSoftFailurePolicy_端点单条批量与恢复默认(t *testing.T) {
	ctx := context.Background()
	st := NewSQLiteEndpointStore(createTestDB(t))
	created, err := st.Create(ctx, &EndpointRecord{Name: "custom", URL: "https://example.test", SoftFailureWindowMinutes: policyValue(2), SoftFailureThreshold: policyValue(1)})
	if err != nil {
		t.Fatal(err)
	}
	assertStoredPolicy(t, created.SoftFailureWindowMinutes, created.SoftFailureThreshold, 2, 1)
	if err := st.BatchCreate(ctx, []*EndpointRecord{
		{Name: "batch", URL: "https://example.test", SoftFailureWindowMinutes: policyValue(10), SoftFailureThreshold: policyValue(5)},
		{Name: "default", URL: "https://example.test"},
	}); err != nil {
		t.Fatal(err)
	}
	items, err := st.List(ctx)
	if err != nil || len(items) != 3 {
		t.Fatalf("批量列表读取失败: len=%d err=%v", len(items), err)
	}
	batch, err := st.Get(ctx, "batch")
	if err != nil {
		t.Fatal(err)
	}
	assertStoredPolicy(t, batch.SoftFailureWindowMinutes, batch.SoftFailureThreshold, 10, 5)
	created.SoftFailureWindowMinutes, created.SoftFailureThreshold = nil, nil
	if err := st.Update(ctx, created); err != nil {
		t.Fatal(err)
	}
	read, err := st.GetByID(ctx, created.ID)
	if err != nil || read.SoftFailureWindowMinutes != nil || read.SoftFailureThreshold != nil {
		t.Fatalf("恢复默认失败: %v", err)
	}
}

func TestSQLiteSoftFailurePolicy_拒绝坏配置而非静默使用默认(t *testing.T) {
	ctx := context.Background()
	accountStore := newTestSQLiteAccountPoolStore(t)
	account := mustCreateTestAccount(t, accountStore, "invalid-policy", 10)
	account.SoftFailureWindowMinutes = policyValue(5)
	if err := accountStore.UpdateAccount(ctx, account); err == nil {
		t.Fatal("应拒绝只填写窗口")
	}
	if _, err := accountStore.db.ExecContext(ctx, `UPDATE upstream_accounts SET soft_failure_window_minutes = ?, soft_failure_threshold = ? WHERE id = ?`, 0, 3, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := accountStore.GetAccount(ctx, account.ID); err == nil {
		t.Fatal("读取坏配置应报错")
	}
	endpointStore := NewSQLiteEndpointStore(createTestDB(t))
	if _, err := endpointStore.Create(ctx, &EndpointRecord{Name: "invalid", URL: "https://example.test", SoftFailureWindowMinutes: policyValue(0), SoftFailureThreshold: policyValue(3)}); err == nil {
		t.Fatal("应拒绝零窗口")
	}
	if err := endpointStore.BatchCreate(ctx, []*EndpointRecord{{Name: "invalid", URL: "https://example.test", SoftFailureThreshold: policyValue(3)}}); err == nil {
		t.Fatal("批量入口也应校验")
	}
}
