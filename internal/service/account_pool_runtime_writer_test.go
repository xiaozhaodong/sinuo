package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func assertRuntimeSnapshotPersisted(t *testing.T, svc *AccountPoolService, id int64) {
	t.Helper()
	svc.runtimeCache.mu.RLock()
	version := svc.runtimeCache.stateVersions[id]
	svc.runtimeCache.mu.RUnlock()
	want, ok := svc.runtimeCache.snapshotState(id, version)
	if !ok {
		t.Fatal("无法读取运行态快照")
	}
	got, err := svc.store.GetAccount(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	stamp := func(value *time.Time) int64 {
		if value == nil {
			return 0
		}
		return value.UnixMicro()
	}
	if got.State != want.State || got.Enabled != want.Enabled || got.FailCount != want.FailCount ||
		got.LastError != want.LastError || stamp(got.LastSuccessAt) != stamp(want.LastSuccessAt) ||
		stamp(got.CooldownUntil) != stamp(want.CooldownUntil) || got.UpdatedAt.UnixMicro() != want.UpdatedAt.UnixMicro() {
		t.Errorf("运行态与数据库不一致: state=%s/%s enabled=%v/%v count=%d/%d success=%d/%d", want.State, got.State, want.Enabled, got.Enabled, want.FailCount, got.FailCount, stamp(want.LastSuccessAt), stamp(got.LastSuccessAt))
	}
}

func TestAccountRuntimeSnapshot_FailureSuccessFailurePreservesReset(t *testing.T) {
	svc, id, writer := newCooldownTestService(t, nil)
	ctx := context.Background()
	account, err := svc.store.GetAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Minute)
	account.State, account.FailCount, account.CooldownUntil = "cooldown", 2, &expired
	if err := svc.store.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if updated, err := svc.MarkAccountSuccessIfNoNewerFailure(ctx, id, time.Now()); err != nil || !updated {
		t.Fatalf("成功结算未生效: updated=%v err=%v", updated, err)
	}
	if err := svc.MarkAccountTransientFailure(ctx, id, "new failure", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := writer.FlushAccount(ctx, id); err != nil {
		t.Fatal(err)
	}
	assertRuntimeSnapshotPersisted(t, svc, id)
	persisted, _ := svc.store.GetAccount(ctx, id)
	if persisted.FailCount != 1 || persisted.LastSuccessAt == nil {
		t.Errorf("中间成功的计数重置或时间戳丢失: count=%d success=%v", persisted.FailCount, persisted.LastSuccessAt)
	}
	if persisted.CredentialRaw != account.CredentialRaw || persisted.AccountName != account.AccountName || persisted.Priority != account.Priority {
		t.Error("运行态写回改动了账号配置")
	}
}

func TestAccountRuntimeSnapshot_OutcomeSequencesConverge(t *testing.T) {
	// 枚举 81 条四步序列，覆盖同一刷新窗口内反复成功、失败与鉴权失效的组合。
	for sequence := 0; sequence < 81; sequence++ {
		events := make([]byte, 4)
		value := sequence
		for i := range events {
			events[i] = "fsa"[value%3]
			value /= 3
		}
		t.Run(string(events), func(t *testing.T) {
			svc, id, writer := newCooldownTestService(t, nil)
			ctx := context.Background()
			for _, event := range events {
				var err error
				switch event {
				case 'f':
					err = svc.MarkAccountTransientFailure(ctx, id, "failure", time.Nanosecond)
				case 's':
					_, err = svc.MarkAccountSuccessIfNoNewerFailure(ctx, id, time.Now())
				case 'a':
					err = svc.MarkAccountAuthFailed(ctx, id, "auth failure")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.FlushAccount(ctx, id); err != nil {
				t.Fatal(err)
			}
			assertRuntimeSnapshotPersisted(t, svc, id)
		})
	}
}

func TestAccountRuntimeWriter_ClosedAdmissionDoesNotChangeCache(t *testing.T) {
	svc, id, writer := newCooldownTestService(t, nil)
	ctx := context.Background()
	before, err := svc.GetAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for name, operation := range map[string]func() error{
		"auth":    func() error { return svc.MarkAccountAuthFailed(ctx, id, "late auth") },
		"failure": func() error { return svc.MarkAccountTransientFailure(ctx, id, "late failure", time.Minute) },
		"success": func() error { _, err := svc.MarkAccountSuccessIfNoNewerFailure(ctx, id, time.Now()); return err },
		"soft_failure": func() error {
			_, err := svc.RecordAccountSoftFailure(ctx, id, "late failure", accountSoftFailureCategoryServerError, 0)
			return err
		},
		"quota": func() error {
			return svc.MarkAccountUsageLimitExceeded(ctx, id, "late limit", "plus", time.Now().Add(time.Hour))
		},
		"clear":  func() error { return svc.ClearAccountCooldown(ctx, id) },
		"toggle": func() error { return svc.ToggleAccount(ctx, id, false) },
		"edit":   func() error { return svc.UpdateAccount(ctx, cloneUpstreamAccountRecord(before)) },
		"delete": func() error { return svc.DeleteAccount(ctx, id) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, errAccountRuntimeWriterClosed) {
				t.Fatalf("关闭后的写入未明确拒绝: %v", err)
			}
			current, err := svc.GetAccount(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if current.State != before.State || current.Enabled != before.Enabled || current.FailCount != before.FailCount || current.LastError != before.LastError || !current.UpdatedAt.Equal(before.UpdatedAt) {
				t.Error("被拒绝的写入改变了缓存")
			}
		})
	}
}

func TestAccountRuntimeWriter_ClosingRejectsLateAuthBeforeCacheChange(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	defer release()
	svc, id, writer := newCooldownTestService(t, &cooldownTestStore{beforeTransient: func(context.Context) error {
		close(entered)
		<-resume
		return nil
	}})
	ctx := context.Background()
	if err := svc.MarkAccountTransientFailure(ctx, id, "accepted failure", time.Minute); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- writer.Close() }()
	<-entered
	if err := svc.MarkAccountAuthFailed(ctx, id, "late auth"); !errors.Is(err, errAccountRuntimeWriterClosed) {
		t.Errorf("关闭期间错误报告接受了新结算: %v", err)
	}
	release()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	assertRuntimeSnapshotPersisted(t, svc, id)
}

func TestAccountRuntimeWriter_CloseRetainsAcceptedRetry(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	defer release()
	var calls atomic.Int32
	writeErr := errors.New("first write failed")
	svc, id, writer := newCooldownTestService(t, &cooldownTestStore{beforeTransient: func(context.Context) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-resume
			return writeErr
		}
		return nil
	}})
	if err := svc.MarkAccountTransientFailure(context.Background(), id, "accepted failure", time.Minute); err != nil {
		t.Fatal(err)
	}
	flushed := make(chan error, 1)
	go func() { flushed <- writer.flushPending(true) }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- writer.Close() }()
	deadline := time.Now().Add(time.Second)
	for {
		writer.mu.Lock()
		closing := writer.closing
		writer.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("关闭未开始")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	if err := <-flushed; !errors.Is(err, writeErr) {
		t.Fatalf("未得到首轮写入失败: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	assertRuntimeSnapshotPersisted(t, svc, id)
}

func TestAccountRuntimeWriter_ConcurrentCloseReportsSameFailure(t *testing.T) {
	writeErr := errors.New("final write failed")
	svc, id, writer := newCooldownTestService(t, &cooldownTestStore{beforeAuth: func(context.Context) error { return writeErr }})
	if err := svc.MarkAccountAuthFailed(context.Background(), id, "accepted auth"); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- writer.Close() }()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; !errors.Is(err, writeErr) {
			t.Errorf("并发关闭遗漏了最终写入错误: %v", err)
		}
	}
}
