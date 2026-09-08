package service

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"cc-forwarder/internal/store"
)

func createSoftFailureAccount(t *testing.T, svc *AccountPoolService, name string, window, threshold int) *store.UpstreamAccountRecord {
	t.Helper()
	record := &store.UpstreamAccountRecord{ProviderType: "api_key", AccountName: name, CredentialRaw: "fixture-" + name, Priority: 10, Enabled: true}
	if window > 0 {
		record.SoftFailureWindowMinutes, record.SoftFailureThreshold = &window, &threshold
	}
	created, err := svc.CreateAccount(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestAccountPoolService_SoftFailurePolicy_窗口阈值按账号独立(t *testing.T) {
	svc, _ := newTestAccountPoolServiceWithStore(t)
	now := time.Now().Add(time.Hour)
	svc.softFailureTracker.now = func() time.Time { return now }
	short := createSoftFailureAccount(t, svc, "short-window", 2, 2)
	long := createSoftFailureAccount(t, svc, "long-window", 10, 3)
	defaultAccount := createSoftFailureAccount(t, svc, "default-window", 0, 0)
	record := func(id int64, want bool) {
		t.Helper()
		got, err := svc.RecordAccountSoftFailure(context.Background(), id, "busy", accountSoftFailureCategoryRateLimit, 0)
		if err != nil || got != want {
			t.Fatalf("账号 %d: failover=%v want=%v err=%v", id, got, want, err)
		}
	}
	for _, account := range []*store.UpstreamAccountRecord{short, long, defaultAccount} {
		record(account.ID, false)
	}
	now = now.Add(6 * time.Minute)
	record(short.ID, false)
	record(defaultAccount.ID, false)
	record(long.ID, false)
	now = now.Add(time.Minute)
	record(long.ID, true)
	record(short.ID, true)
	record(defaultAccount.ID, false)
}

func TestAccountPoolService_SoftFailurePolicy_更新隔离计数并保留冷却(t *testing.T) {
	svc, _ := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	a := createSoftFailureAccount(t, svc, "policy-a", 0, 0)
	b := createSoftFailureAccount(t, svc, "policy-b", 0, 0)
	record := func(id int64, want bool) {
		t.Helper()
		got, err := svc.RecordAccountSoftFailure(ctx, id, "busy", accountSoftFailureCategoryServerError, 0)
		if err != nil || got != want {
			t.Fatalf("账号 %d: failover=%v want=%v err=%v", id, got, want, err)
		}
	}
	for i := 0; i < 2; i++ {
		record(a.ID, false)
		record(b.ID, false)
	}
	window, threshold := 10, 4
	a.SoftFailureWindowMinutes, a.SoftFailureThreshold = &window, &threshold
	if err := svc.UpdateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		record(a.ID, false)
	}
	record(b.ID, true)
	record(a.ID, true)
	before, err := svc.GetAccount(ctx, a.ID)
	if err != nil || before.CooldownUntil == nil {
		t.Fatalf("缺少冷却: %v", err)
	}
	a.SoftFailureWindowMinutes, a.SoftFailureThreshold = nil, nil
	if err := svc.UpdateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	after, err := svc.GetAccount(ctx, a.ID)
	if err != nil || after.CooldownUntil == nil || !after.CooldownUntil.Equal(*before.CooldownUntil) || after.State != before.State {
		t.Fatalf("修改策略清除了已有冷却: %v", err)
	}
	if after.SoftFailureWindowMinutes != nil || after.SoftFailureThreshold != nil {
		t.Fatal("恢复默认未同步运行时")
	}
}

func TestAccountPoolService_SoftFailurePolicy_普通编辑和保存失败不清计数(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		svc, _ := newTestAccountPoolServiceWithStore(t)
		ctx := context.Background()
		a := createSoftFailureAccount(t, svc, "keep-history", 0, 0)
		for i := 0; i < 2; i++ {
			if hit, err := svc.RecordAccountSoftFailure(ctx, a.ID, "busy", accountSoftFailureCategoryServerError, 0); err != nil || hit {
				t.Fatalf("提前冷却: %v", err)
			}
		}
		a.AccountName = "renamed"
		if invalid {
			zero, three := 0, 3
			a.SoftFailureWindowMinutes, a.SoftFailureThreshold = &zero, &three
		}
		err := svc.UpdateAccount(ctx, a)
		if (err != nil) != invalid {
			t.Fatalf("保存校验不符: %v", err)
		}
		if hit, err := svc.RecordAccountSoftFailure(ctx, a.ID, "busy", accountSoftFailureCategoryServerError, 0); err != nil || !hit {
			t.Fatalf("原有计数被清空: %v", err)
		}
	}
}

func TestAccountPoolService_SoftFailurePolicy_并发发布与结算(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	a := createSoftFailureAccount(t, svc, "concurrent-policy", 2, 1000)
	var wg sync.WaitGroup
	wg.Add(2)
	errors := make(chan error, 200)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if _, err := svc.RecordAccountSoftFailure(ctx, a.ID, "busy", accountSoftFailureCategoryServerError, 0); err != nil {
				errors <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			input := *a
			window := 2 + i%2
			input.SoftFailureWindowMinutes = &window
			if err := svc.UpdateAccount(ctx, &input); err != nil {
				errors <- err
			}
		}
	}()
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	runtime, err := svc.GetAccount(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := st.GetAccount(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *runtime.SoftFailureWindowMinutes != 3 || *persisted.SoftFailureWindowMinutes != 3 || runtime.State == "cooldown" {
		t.Fatal("并发结算覆盖了最终策略或错误触发冷却")
	}
	*runtime.SoftFailureWindowMinutes = 99
	readAgain, err := svc.GetAccount(ctx, a.ID)
	if err != nil || *readAgain.SoftFailureWindowMinutes != 3 {
		t.Fatal("返回的配置指针与运行时共享")
	}
}

func TestAccountPoolService_RecordAccountSoftFailure_TriggersCooldownAfterThreshold(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	base := time.Now().Add(2 * time.Minute).Round(time.Second)
	svc.softFailureTracker.now = func() time.Time { return base }

	acc, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "acc-soft-server",
		CredentialRaw: "sk-soft-server",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		shouldFailover, err := svc.RecordAccountSoftFailure(ctx, acc.ID, "server busy", accountSoftFailureCategoryServerError, 0)
		if err != nil {
			t.Fatalf("RecordAccountSoftFailure attempt %d failed: %v", attempt+1, err)
		}
		if shouldFailover {
			t.Fatalf("expected no failover before threshold, got true on attempt %d", attempt+1)
		}
		current, err := svc.GetAccount(ctx, acc.ID)
		if err != nil {
			t.Fatalf("GetAccount failed: %v", err)
		}
		if current.State != "active" || current.CooldownUntil != nil || current.FailCount != 0 {
			t.Fatalf("expected no cooldown before threshold, got %+v", current)
		}
	}

	shouldFailover, err := svc.RecordAccountSoftFailure(ctx, acc.ID, "server busy", accountSoftFailureCategoryServerError, 0)
	if err != nil {
		t.Fatalf("RecordAccountSoftFailure third attempt failed: %v", err)
	}
	if !shouldFailover {
		t.Fatal("expected failover trigger after threshold")
	}
	current, err := svc.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if current.State != "cooldown" {
		t.Fatalf("expected cooldown after threshold, got state=%s", current.State)
	}
	if current.FailCount != 1 {
		t.Fatalf("expected fail_count=1 after cooldown trigger, got %d", current.FailCount)
	}
	if current.CooldownUntil == nil {
		t.Fatal("expected cooldown_until to be set")
	}
	if got, want := current.CooldownUntil.Sub(base), defaultAccountServerErrorCooldown; got != want {
		t.Fatalf("unexpected cooldown duration: got %v want %v", got, want)
	}
	waitForPersistedAccountState(t, st, acc.ID, func(record *store.UpstreamAccountRecord) bool {
		return record != nil && record.State == "cooldown" && record.FailCount == 1 && record.CooldownUntil != nil && record.CooldownUntil.Sub(base) == defaultAccountServerErrorCooldown
	})
}

func TestAccountPoolService_RecordAccountSoftFailure_UsesRetryAfterForRateLimit(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	base := time.Now().Add(2 * time.Minute).Round(time.Second)
	svc.softFailureTracker.now = func() time.Time { return base }

	acc, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "acc-soft-rate",
		CredentialRaw: "sk-soft-rate",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		shouldFailover, err := svc.RecordAccountSoftFailure(ctx, acc.ID, "rate limited", accountSoftFailureCategoryRateLimit, 45*time.Second)
		if err != nil {
			t.Fatalf("RecordAccountSoftFailure attempt %d failed: %v", attempt+1, err)
		}
		if shouldFailover {
			t.Fatalf("expected no failover before threshold, got true on attempt %d", attempt+1)
		}
	}
	shouldFailover, err := svc.RecordAccountSoftFailure(ctx, acc.ID, "rate limited", accountSoftFailureCategoryRateLimit, 45*time.Second)
	if err != nil {
		t.Fatalf("RecordAccountSoftFailure third attempt failed: %v", err)
	}
	if !shouldFailover {
		t.Fatal("expected failover trigger after threshold")
	}

	current, err := svc.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if current.CooldownUntil == nil {
		t.Fatal("expected cooldown_until to be set")
	}
	if got := current.CooldownUntil.Sub(base); got != 45*time.Second {
		t.Fatalf("unexpected rate limit cooldown: got %v want %v", got, 45*time.Second)
	}
	waitForPersistedAccountState(t, st, acc.ID, func(record *store.UpstreamAccountRecord) bool {
		return record != nil && record.CooldownUntil != nil && record.CooldownUntil.Sub(base) == 45*time.Second
	})
}

func TestAccountPoolService_MarkAccountSuccess_ClearsSoftFailureWindow(t *testing.T) {
	svc, st := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	base := time.Now().Add(2 * time.Minute).Round(time.Second)
	svc.softFailureTracker.now = func() time.Time { return base }

	acc, err := st.CreateAccount(ctx, &store.UpstreamAccountRecord{
		ProviderType:  "api_key",
		AccountName:   "acc-soft-clear",
		CredentialRaw: "sk-soft-clear",
		Priority:      10,
		Enabled:       true,
		State:         "active",
	})
	if err != nil {
		t.Fatalf("create account failed: %v", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		shouldFailover, err := svc.RecordAccountSoftFailure(ctx, acc.ID, "server busy", accountSoftFailureCategoryServerError, 0)
		if err != nil {
			t.Fatalf("RecordAccountSoftFailure attempt %d failed: %v", attempt+1, err)
		}
		if shouldFailover {
			t.Fatalf("expected no failover before threshold, got true on attempt %d", attempt+1)
		}
	}

	if err := svc.MarkAccountSuccess(ctx, acc.ID); err != nil {
		t.Fatalf("MarkAccountSuccess failed: %v", err)
	}
	shouldFailover, err := svc.RecordAccountSoftFailure(ctx, acc.ID, "server busy", accountSoftFailureCategoryServerError, 0)
	if err != nil {
		t.Fatalf("RecordAccountSoftFailure after success failed: %v", err)
	}
	if shouldFailover {
		t.Fatal("expected success to reset failover threshold")
	}

	current, err := svc.GetAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if current.State != "active" {
		t.Fatalf("expected account to remain active after reset, got state=%s", current.State)
	}
	if current.FailCount != 0 {
		t.Fatalf("expected fail_count to remain 0 after reset, got %d", current.FailCount)
	}
}

func waitForPersistedAccountState(t *testing.T, st *store.SQLiteAccountPoolStore, accountID int64, predicate func(*store.UpstreamAccountRecord) bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for {
		record, err := st.GetAccount(context.Background(), accountID)
		if err == nil && predicate(record) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("persisted account state did not converge in time: record=%+v err=%v", record, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

type softFailureDeleteErrorStore struct {
	store.AccountPoolStore
	deleteErr error
}

func (s *softFailureDeleteErrorStore) DeleteAccount(context.Context, int64) error {
	return s.deleteErr
}

func TestAccountPoolService_SoftFailurePolicy_删除按持久化结果清理(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "删除成功"
		if fail {
			name = "删除失败"
		}
		t.Run(name, func(t *testing.T) {
			svc, st := newTestAccountPoolServiceWithStore(t)
			ctx := context.Background()
			account := createSoftFailureAccount(t, svc, "delete-policy", 0, 0)
			other := createSoftFailureAccount(t, svc, "keep-policy", 0, 0)
			for _, id := range []int64{account.ID, other.ID} {
				if hit, err := svc.RecordAccountSoftFailure(ctx, id, "busy", accountSoftFailureCategoryServerError, 0); err != nil || hit {
					t.Fatalf("准备失败事件: hit=%v err=%v", hit, err)
				}
			}
			deleteErr := errors.New("删除持久化失败")
			if fail {
				svc.store = &softFailureDeleteErrorStore{AccountPoolStore: st, deleteErr: deleteErr}
			}
			err := svc.DeleteAccount(ctx, account.ID)
			if fail && !errors.Is(err, deleteErr) || !fail && err != nil {
				t.Fatalf("删除返回错误不符合预期: %v", err)
			}
			svc.softFailureTracker.mu.Lock()
			count := len(svc.softFailureTracker.events[account.ID])
			otherCount := len(svc.softFailureTracker.events[other.ID])
			svc.softFailureTracker.mu.Unlock()
			want := 0
			if fail {
				want = 1
			}
			if count != want || otherCount != 1 {
				t.Errorf("事件清理错误: deleted=%d want=%d other=%d", count, want, otherCount)
			}
			record, err := st.GetAccount(ctx, account.ID)
			if fail {
				if err != nil || record == nil {
					t.Fatal("失败的删除改变了持久化账号")
				}
			} else if !errors.Is(err, sql.ErrNoRows) || record != nil {
				t.Fatalf("持久化账号未删除: err=%v", err)
			}
			if _, exists := svc.runtimeCache.get(account.ID); exists != fail {
				t.Error("运行态与删除结果不一致")
			}
		})
	}
}

func TestAccountPoolService_SoftFailurePolicy_删除与在途结算串行(t *testing.T) {
	svc, _ := newTestAccountPoolServiceWithStore(t)
	ctx := context.Background()
	account := createSoftFailureAccount(t, svc, "delete-in-flight", 0, 0)
	captured, resume := make(chan struct{}), make(chan struct{})
	var captureOnce, resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	defer release()
	now := time.Now()
	svc.softFailureTracker.now = func() time.Time {
		captureOnce.Do(func() {
			close(captured)
			<-resume
		})
		return now
	}
	settled := make(chan error, 1)
	go func() {
		_, err := svc.RecordAccountSoftFailure(ctx, account.ID, "busy", accountSoftFailureCategoryServerError, 0)
		settled <- err
	}()
	select {
	case <-captured:
	case <-time.After(5 * time.Second):
		t.Fatal("失败结算未读到账号")
	}
	deleted := make(chan error, 1)
	go func() { deleted <- svc.DeleteAccount(ctx, account.ID) }()
	var deleteErr error
	deletedBeforeSettlement := false
	select {
	case deleteErr = <-deleted:
		deletedBeforeSettlement = true
	case <-time.After(200 * time.Millisecond):
		// 正确实现会等候在途结算退出，再完成删除。
	}
	release()
	select {
	case err := <-settled:
		if err != nil {
			t.Errorf("先开始的失败结算未正常完成: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("失败结算未结束")
	}
	if !deletedBeforeSettlement {
		select {
		case deleteErr = <-deleted:
		case <-time.After(5 * time.Second):
			t.Fatal("删除未结束")
		}
	}
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	if deletedBeforeSettlement {
		t.Error("删除越过了尚未完成的失败结算")
	}
	svc.softFailureTracker.mu.Lock()
	count := len(svc.softFailureTracker.events[account.ID])
	svc.softFailureTracker.mu.Unlock()
	if count != 0 {
		t.Fatalf("删除后晚到结算留下 %d 条事件", count)
	}
	if _, err := svc.RecordAccountSoftFailure(ctx, account.ID, "late failure", accountSoftFailureCategoryServerError, 0); err == nil {
		t.Fatal("删除后的失败结算仍被接受")
	}
}
