package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"sinuo/internal/store"
)

type cooldownTestStore struct {
	store.AccountPoolStore
	beforeTransient func(context.Context) error
	beforeClear     func(context.Context) error
	beforeAuth      func(context.Context) error
}

func (s *cooldownTestStore) SaveAccountRuntimeState(ctx context.Context, id int64, state store.AccountRuntimeState) error {
	var before func(context.Context) error
	if state.State == "cooldown" {
		before = s.beforeTransient
	} else if state.State == "disabled_auth" {
		before = s.beforeAuth
	}
	if before != nil {
		if err := before(ctx); err != nil {
			return err
		}
	}
	return s.AccountPoolStore.SaveAccountRuntimeState(ctx, id, state)
}

func (s *cooldownTestStore) ClearAccountCooldown(ctx context.Context, id int64, at time.Time) error {
	if s.beforeClear != nil {
		if err := s.beforeClear(ctx); err != nil {
			return err
		}
	}
	return s.AccountPoolStore.(interface {
		ClearAccountCooldown(context.Context, int64, time.Time) error
	}).ClearAccountCooldown(ctx, id, at)
}

// 手动驱动生产 writer 的刷新入口，确保测试不会依赖 200ms tick 的时机。
func newCooldownTestService(t *testing.T, st *cooldownTestStore) (*AccountPoolService, int64, *accountPoolRuntimeWriter) {
	t.Helper()
	svc, id := newTestAccountPoolServiceForConnectivity(t, "https://example.invalid", "api_key", "fixture-cooldown")
	if err := svc.runtimeWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if st != nil {
		st.AccountPoolStore = svc.store
		svc.store = st
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &accountPoolRuntimeWriter{
		store: svc.store, ctx: ctx, cancel: cancel,
		pending:          make(map[int64]accountRuntimeMutation),
		isCurrentVersion: svc.runtimeCache.matchesStateVersion,
	}
	svc.runtimeWriter = w
	return svc, id, w
}

func assertCooldownAccountState(t *testing.T, svc *AccountPoolService, id int64, state string, enabled bool) {
	t.Helper()
	var first *store.UpstreamAccountRecord
	for name, read := range map[string]func(context.Context, int64) (*store.UpstreamAccountRecord, error){
		"运行态": svc.GetAccount, "数据库": svc.store.GetAccount,
	} {
		record, err := read(context.Background(), id)
		if err != nil || record == nil {
			t.Fatalf("%s 读取失败: %v", name, err)
		}
		if record.State != state || record.Enabled != enabled {
			t.Errorf("%s: state=%s enabled=%v，期望 %s/%v", name, record.State, record.Enabled, state, enabled)
		}
		if state == "active" && (record.CooldownUntil != nil || record.FailCount != 0 || record.LastError != "") {
			t.Errorf("%s 残留冷却状态: until=%v count=%d error=%q", name, record.CooldownUntil, record.FailCount, record.LastError)
		}
		if first != nil && record.FailCount != first.FailCount {
			t.Errorf("运行态与数据库失败次数不一致: %d / %d", first.FailCount, record.FailCount)
		}
		first = record
	}
}

func TestClearAccountCooldown_PreservesQueuedAuthFailure(t *testing.T) {
	svc, id, writer := newCooldownTestService(t, nil)
	ctx := context.Background()
	if err := svc.MarkAccountTransientFailure(ctx, id, "temporary", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := writer.FlushAccount(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkAccountAuthFailed(ctx, id, "invalid credential"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ClearAccountCooldown(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := writer.flushPending(false); err != nil {
		t.Fatal(err)
	}
	assertCooldownAccountState(t, svc, id, "disabled_auth", false)
	record, _ := svc.GetAccount(ctx, id)
	if record.LastError != "invalid credential" {
		t.Errorf("鉴权失败原因被清除: %q", record.LastError)
	}
}

func TestClearAccountCooldown_WaitsForDetachedFailureWrite(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	var resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	defer release()
	st := &cooldownTestStore{beforeTransient: func(ctx context.Context) error {
		close(entered)
		select {
		case <-resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	svc, id, writer := newCooldownTestService(t, st)
	ctx := context.Background()
	if err := svc.MarkAccountTransientFailure(ctx, id, "older failure", time.Minute); err != nil {
		t.Fatal(err)
	}
	flushed := make(chan error, 1)
	go func() { flushed <- writer.flushPending(false) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("旧失败写入未开始")
	}
	cleared := make(chan error, 1)
	go func() { cleared <- svc.ClearAccountCooldown(ctx, id) }()
	var clearErr error
	returnedEarly := false
	select {
	case clearErr = <-cleared:
		returnedEarly = true
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	if !returnedEarly {
		clearErr = <-cleared
	}
	if clearErr != nil {
		t.Fatal(clearErr)
	}
	if returnedEarly {
		t.Error("解除冷却在旧写入完成前返回了成功")
	}
	assertCooldownAccountState(t, svc, id, "active", true)
}

func TestClearAccountCooldown_PersistFailureKeepsCooldownAndCounts(t *testing.T) {
	persistErr := errors.New("清除冷却写入失败")
	svc, id, writer := newCooldownTestService(t, &cooldownTestStore{beforeClear: func(context.Context) error { return persistErr }})
	ctx := context.Background()
	if err := svc.MarkAccountTransientFailure(ctx, id, "temporary", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := writer.FlushAccount(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordAccountSoftFailure(ctx, id, "counted failure", accountSoftFailureCategoryServerError, 0); err != nil {
		t.Fatal(err)
	}
	if err := svc.ClearAccountCooldown(ctx, id); !errors.Is(err, persistErr) {
		t.Fatalf("未返回持久化错误: %v", err)
	}
	assertCooldownAccountState(t, svc, id, "cooldown", true)
	svc.softFailureTracker.mu.Lock()
	count := len(svc.softFailureTracker.events[id])
	svc.softFailureTracker.mu.Unlock()
	if count != 1 {
		t.Errorf("保存失败清除了累计事件: %d", count)
	}
}

func TestClearAccountCooldown_AuthWriteFailureRemainsRetryable(t *testing.T) {
	persistErr := errors.New("鉴权状态写入失败")
	fail := true
	svc, id, writer := newCooldownTestService(t, &cooldownTestStore{beforeAuth: func(context.Context) error {
		if fail {
			return persistErr
		}
		return nil
	}})
	ctx := context.Background()
	if err := svc.MarkAccountAuthFailed(ctx, id, "invalid credential"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ClearAccountCooldown(ctx, id); !errors.Is(err, persistErr) {
		t.Errorf("鉴权写入失败不应报告清除成功: %v", err)
	}
	fail = false
	if err := writer.flushPending(false); err != nil {
		t.Fatal(err)
	}
	assertCooldownAccountState(t, svc, id, "disabled_auth", false)
}

func TestClearAccountCooldown_PreservesFailureDuringPersistence(t *testing.T) {
	for _, tc := range []struct {
		operation string
		state     string
		enabled   bool
		failCount int
	}{
		{"transient", "cooldown", true, 1},
		{"auth", "disabled_auth", false, 0},
		{"disable", "active", false, 0},
		{"edit_disable", "active", false, 0},
		{"success", "active", true, 0},
		{"request_success", "active", true, 0},
		{"quota", "cooldown", true, 1},
		{"soft_failure", "cooldown", true, 1},
		{"profile_auth", "disabled_auth", false, 0},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			entered, resume := make(chan struct{}), make(chan struct{})
			var resumeOnce sync.Once
			release := func() { resumeOnce.Do(func() { close(resume) }) }
			defer release()
			svc, id, writer := newCooldownTestService(t, &cooldownTestStore{beforeClear: func(ctx context.Context) error {
				close(entered)
				select {
				case <-resume:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}})
			ctx := context.Background()
			if err := svc.MarkAccountTransientFailure(ctx, id, "old failure", time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := writer.FlushAccount(ctx, id); err != nil {
				t.Fatal(err)
			}
			cleared := make(chan error, 1)
			go func() { cleared <- svc.ClearAccountCooldown(ctx, id) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("清除写入未开始")
			}
			changed := make(chan error, 1)
			go func() {
				var err error
				switch tc.operation {
				case "auth":
					err = svc.MarkAccountAuthFailed(ctx, id, "new failure")
				case "disable":
					err = svc.ToggleAccount(ctx, id, false)
				case "edit_disable", "profile_auth":
					var account *store.UpstreamAccountRecord
					account, err = svc.GetAccount(ctx, id)
					if err == nil {
						if tc.operation == "edit_disable" {
							account.Enabled = false
							err = svc.UpdateAccount(ctx, account)
						} else {
							err = svc.persistAccountAuthFailureProfile(ctx, account, "new failure", time.Now())
						}
					}
				case "success":
					err = svc.MarkAccountSuccess(ctx, id)
				case "request_success":
					_, err = svc.MarkAccountSuccessIfNoNewerFailure(ctx, id, time.Now())
				case "quota":
					err = svc.MarkAccountUsageLimitExceeded(ctx, id, "new failure", "plus", time.Now().Add(time.Hour))
				case "soft_failure":
					for i := 0; i < 3 && err == nil; i++ {
						_, err = svc.RecordAccountSoftFailure(ctx, id, "new failure", accountSoftFailureCategoryServerError, 0)
					}
				default:
					err = svc.MarkAccountTransientFailure(ctx, id, "new failure", 2*time.Minute)
				}
				changed <- err
			}()
			var changeErr error
			returnedEarly := false
			select {
			case changeErr = <-changed:
				returnedEarly = true
			case <-time.After(100 * time.Millisecond):
			}
			release()
			if err := <-cleared; err != nil {
				t.Fatal(err)
			}
			if !returnedEarly {
				changeErr = <-changed
			}
			if changeErr != nil {
				t.Fatal(changeErr)
			}
			if returnedEarly {
				t.Error("状态变更越过了尚未完成的冷却清除")
			}
			if err := writer.flushPending(false); err != nil {
				t.Fatal(err)
			}
			assertCooldownAccountState(t, svc, id, tc.state, tc.enabled)
			record, _ := svc.GetAccount(ctx, id)
			if record.FailCount != tc.failCount {
				t.Errorf("失败次数=%d，期望 %d", record.FailCount, tc.failCount)
			}
		})
	}
}

func TestAccountAuthFailure_LateRequestOutcomesKeepDisabledState(t *testing.T) {
	for _, outcome := range []string{"transient", "soft_failure", "quota", "success"} {
		t.Run(outcome, func(t *testing.T) {
			svc, id, writer := newCooldownTestService(t, nil)
			ctx := context.Background()
			if err := svc.MarkAccountAuthFailed(ctx, id, "invalid credential"); err != nil {
				t.Fatal(err)
			}
			var err error
			switch outcome {
			case "transient":
				err = svc.MarkAccountTransientFailure(ctx, id, "late failure", time.Minute)
			case "quota":
				err = svc.MarkAccountUsageLimitExceeded(ctx, id, "late limit", "plus", time.Now().Add(time.Hour))
			case "success":
				var updated bool
				updated, err = svc.MarkAccountSuccessIfNoNewerFailure(ctx, id, time.Now())
				if updated {
					t.Error("普通成功结算改写了鉴权禁用")
				}
			case "soft_failure":
				for i := 0; i < 3; i++ {
					var shouldFailover bool
					shouldFailover, err = svc.RecordAccountSoftFailure(ctx, id, "late failure", accountSoftFailureCategoryServerError, 0)
					if err != nil || !shouldFailover {
						t.Fatalf("鉴权禁用后阻止了当前请求换号: failover=%v err=%v", shouldFailover, err)
					}
				}
				svc.softFailureTracker.mu.Lock()
				count := len(svc.softFailureTracker.events[id])
				svc.softFailureTracker.mu.Unlock()
				if count != 0 {
					t.Errorf("鉴权禁用后仍累计软失败: %d", count)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.ClearAccountCooldown(ctx, id); err != nil {
				t.Fatal(err)
			}
			if err := writer.FlushAccount(ctx, id); err != nil {
				t.Fatal(err)
			}
			assertCooldownAccountState(t, svc, id, "disabled_auth", false)
			record, _ := svc.GetAccount(ctx, id)
			if record.LastError != "invalid credential" {
				t.Errorf("鉴权失败原因被覆盖: %q", record.LastError)
			}
		})
	}
}

func TestAccountRuntimeWriter_StaleRetryKeepsLatestState(t *testing.T) {
	for _, auth := range []bool{false, true} {
		name := "旧临时失败"
		if auth {
			name = "显式恢复后的旧鉴权失败"
		}
		t.Run(name, func(t *testing.T) {
			svc, id, writer := newCooldownTestService(t, nil)
			ctx := context.Background()
			var err error
			if auth {
				err = svc.MarkAccountAuthFailed(ctx, id, "old auth")
			} else {
				err = svc.MarkAccountTransientFailure(ctx, id, "old failure", time.Minute)
			}
			if err != nil {
				t.Fatal(err)
			}
			detached := writer.detachPending()
			if len(detached) != 1 {
				t.Fatalf("待写回事件数量=%d", len(detached))
			}
			if auth {
				if err := svc.ToggleAccount(ctx, id, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.MarkAccountTransientFailure(ctx, id, "latest failure", 2*time.Minute); err != nil {
				t.Fatal(err)
			}
			writer.enqueue(detached[0]) // 旧写入失败后重新进入队列。
			if err := writer.FlushAccount(ctx, id); err != nil {
				t.Fatal(err)
			}
			assertCooldownAccountState(t, svc, id, "cooldown", true)
			persisted, _ := svc.store.GetAccount(ctx, id)
			if persisted.LastError != "latest failure" {
				t.Errorf("旧事件覆盖了新失败原因: %q", persisted.LastError)
			}
		})
	}
}

func TestClearAccountCooldown_CancelledCallPreservesPendingFailure(t *testing.T) {
	svc, id, writer := newCooldownTestService(t, nil)
	if err := svc.MarkAccountTransientFailure(context.Background(), id, "temporary", time.Minute); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.ClearAccountCooldown(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("未返回取消错误: %v", err)
	}
	if err := writer.flushPending(false); err != nil {
		t.Fatal(err)
	}
	assertCooldownAccountState(t, svc, id, "cooldown", true)
}

func TestUpdateAccount_EnabledChangePreservesPendingFailureCounts(t *testing.T) {
	for _, enable := range []bool{false, true} {
		name := "编辑停用"
		if enable {
			name = "编辑重新启用"
		}
		t.Run(name, func(t *testing.T) {
			svc, id, writer := newCooldownTestService(t, nil)
			ctx := context.Background()
			if enable {
				if err := svc.ToggleAccount(ctx, id, false); err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.MarkAccountTransientFailure(ctx, id, "before edit", time.Minute); err != nil {
				t.Fatal(err)
			}
			account, err := svc.GetAccount(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			account.Enabled = enable
			if err := svc.UpdateAccount(ctx, account); err != nil {
				t.Fatal(err)
			}
			if enable {
				if err := svc.MarkAccountTransientFailure(ctx, id, "after enable", time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.FlushAccount(ctx, id); err != nil {
				t.Fatal(err)
			}
			assertCooldownAccountState(t, svc, id, "cooldown", enable)
			persisted, _ := svc.store.GetAccount(ctx, id)
			if persisted.FailCount != 1 {
				t.Errorf("编辑启停后失败次数=%d，期望 1", persisted.FailCount)
			}
		})
	}
}
