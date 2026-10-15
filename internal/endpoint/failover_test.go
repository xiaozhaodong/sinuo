// failover_test.go - 手动解除冷却(ResetEndpointFailureState)与故障 epoch 写时 fencing 测试
// 2026-08-05

package endpoint

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/config"
)

// testManagerWithFailureTracker 构造带启用失败追踪的 Manager 与单端点
func testManagerWithFailureTracker(t *testing.T, name string) *Manager {
	t.Helper()
	cfg := &config.Config{
		FailureTracker: config.FailureTrackerConfig{
			Enabled:    true,
			TimeWindow: time.Hour,
			Threshold:  3,
		},
		Endpoints: []config.EndpointConfig{
			{Name: name, URL: "http://127.0.0.1:1", Priority: 1, Timeout: 5 * time.Second},
		},
	}
	manager := NewManager(cfg)
	if manager.GetEndpointByNameAny(name) == nil {
		t.Fatalf("端点 %s 未装配", name)
	}
	return manager
}

func TestResetEndpointFailureStateClearsBothSlots(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")
	ep := m.GetEndpointByNameAny("ep")
	ep.mutex.Lock()
	ep.Status.CooldownUntil = time.Now().Add(10 * time.Minute)
	ep.Status.CooldownReason = "soft_failure_server_error"
	ep.Status.GlobalCooldownUntil = time.Now().Add(20 * time.Minute)
	ep.Status.GlobalCooldownReason = "auth_rejected"
	ep.mutex.Unlock()

	revision, found := m.ResetEndpointFailureState("ep")
	if !found || revision <= 0 {
		t.Fatalf("Reset 应成功且返回正 revision: revision=%d found=%v", revision, found)
	}
	if m.IsEndpointInCooldown("ep") {
		t.Fatal("Reset 后两槽冷却应全部清除")
	}
	if in, _, reason := m.GetEndpointCooldownInfo("ep"); in {
		t.Fatalf("Reset 后不应有生效冷却, reason=%s", reason)
	}
}

// TestResetEndpointFailureStateRevisionWithoutCooldown 无冷却重试语义:
// 上一次落库失败后的重试必须能补写 tombstone,无冷却也返回严格递增的 revision
func TestResetEndpointFailureStateRevisionWithoutCooldown(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")

	r1, found1 := m.ResetEndpointFailureState("ep")
	r2, found2 := m.ResetEndpointFailureState("ep")
	r3, found3 := m.ResetEndpointFailureState("ep")
	if !found1 || !found2 || !found3 {
		t.Fatalf("无冷却前置下 Reset 应全部 found: %v %v %v", found1, found2, found3)
	}
	if r1 <= 0 || r2 <= r1 || r3 <= r2 {
		t.Fatalf("revision 应严格递增: %d %d %d", r1, r2, r3)
	}
}

func TestResetEndpointFailureStateAdvancesEpoch(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")
	ep := m.GetEndpointByNameAny("ep")

	ep.mutex.RLock()
	before := ep.failureEpoch
	ep.mutex.RUnlock()

	if _, found := m.ResetEndpointFailureState("ep"); !found {
		t.Fatal("Reset 应成功")
	}

	ep.mutex.RLock()
	after := ep.failureEpoch
	ep.mutex.RUnlock()
	if after != before+1 {
		t.Fatalf("epoch 应 +1: before=%d after=%d", before, after)
	}

	// 通过 plan 捕获路径观测同一值
	snapshots := m.snapshotEndpointCandidates()
	if len(snapshots) != 1 || snapshots[0].failureEpoch != after {
		t.Fatalf("快照捕获的 failureEpoch 应与端点一致: %+v", snapshots)
	}
}

func TestResetEndpointFailureStateClearsSoftFailuresAndScopedCooldown(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")

	m.RecordSoftFailure("ep", SoftFailureScopeMessages, SoftFailureCategoryConnection)
	m.RecordSoftFailure("ep", SoftFailureScopeMessages, SoftFailureCategoryServerError)
	m.RecordSoftFailure("ep", SoftFailureScopeCountTokens, SoftFailureCategoryRateLimit)
	m.SetScopedCooldown("ep", SoftFailureScopeCountTokens, time.Minute, "soft_failure_rate_limit")

	m.ResetEndpointFailureState("ep")

	for _, scope := range []SoftFailureScope{SoftFailureScopeMessages, SoftFailureScopeCountTokens} {
		if counts := m.GetSoftFailureCounts("ep", scope); len(counts) != 0 {
			t.Fatalf("Reset 后 scope=%s 软失败计数应清空: %+v", scope, counts)
		}
	}
	if active, _, _ := m.ScopedCooldownActive("ep", SoftFailureScopeCountTokens); active {
		t.Fatal("Reset 后 scoped cooldown 应清除")
	}
}

// TestNewEpochSoftFailureNotClearedSequential 顺序语义:
// Reset 返回后立即以新 epoch fenced 记录软失败,计数必须保留
func TestNewEpochSoftFailureNotClearedSequential(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")
	if _, found := m.ResetEndpointFailureState("ep"); !found {
		t.Fatal("Reset 应成功")
	}
	// Reset 返回后 failureEpoch == 1
	count, tripped, applied := m.RecordSoftFailureFenced("ep", SoftFailureScopeMessages, SoftFailureCategoryConnection, 1)
	if !applied || count != 1 || tripped {
		t.Fatalf("新 epoch 记录应被接受: count=%d tripped=%v applied=%v", count, tripped, applied)
	}
	if got := m.GetSoftFailureCounts("ep", SoftFailureScopeMessages)[SoftFailureCategoryConnection]; got != 1 {
		t.Fatalf("新 epoch 记录应保留: got=%d", got)
	}
}

// TestNewEpochSoftFailureNotClearedConcurrent 确定性并发:
// goroutine A 执行 Reset;goroutine B 自旋观察 epoch,观察到新值后 fenced 记录一次软失败。
// 确定性依据:B 观察到新 epoch 必然 happens-after Reset 临界区退出(写锁互斥),
// 而清理在临界区内——记录必然保留。单临界区被破坏的实现下概率性失败,作为回归护栏。
func TestNewEpochSoftFailureNotClearedConcurrent(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")
	m.ResetEndpointFailureState("ep") // 推进到 epoch 1

	var wg sync.WaitGroup
	wg.Add(2)
	var recorded atomic.Bool

	go func() {
		defer wg.Done()
		m.ResetEndpointFailureState("ep") // 推进到 epoch 2
	}()
	go func() {
		defer wg.Done()
		ep := m.GetEndpointByNameAny("ep")
		deadline := time.Now().Add(5 * time.Second)
		for {
			ep.mutex.RLock()
			epoch := ep.failureEpoch
			ep.mutex.RUnlock()
			if epoch >= 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("等待新 epoch 超时")
				return
			}
			time.Sleep(time.Microsecond)
		}
		_, _, applied := m.RecordSoftFailureFenced("ep", SoftFailureScopeMessages, SoftFailureCategoryConnection, 2)
		recorded.Store(applied)
	}()
	wg.Wait()

	if !recorded.Load() {
		t.Fatal("观察到新 epoch 后的 fenced 记录应被接受")
	}
	if got := m.GetSoftFailureCounts("ep", SoftFailureScopeMessages)[SoftFailureCategoryConnection]; got != 1 {
		t.Fatalf("新 epoch 记录应保留: got=%d", got)
	}
}

func TestRecordSoftFailureFencedWriteFencing(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")

	// 旧 epoch 拒写
	count, tripped, applied := m.RecordSoftFailureFenced("ep", SoftFailureScopeMessages, SoftFailureCategoryConnection, 999)
	if applied || count != 0 || tripped {
		t.Fatalf("旧 epoch 应拒写: count=%d tripped=%v applied=%v", count, tripped, applied)
	}
	if got := m.GetSoftFailureCounts("ep", SoftFailureScopeMessages)[SoftFailureCategoryConnection]; got != 0 {
		t.Fatalf("旧 epoch 不应计数: got=%d", got)
	}

	// 当前 epoch(0)正常计数,阈值/trip 语义与 RecordSoftFailure 一致
	count, _, applied = m.RecordSoftFailureFenced("ep", SoftFailureScopeMessages, SoftFailureCategoryConnection, 0)
	if !applied || count != 1 {
		t.Fatalf("当前 epoch 第一次记录: count=%d applied=%v", count, applied)
	}
	count, _, applied = m.RecordSoftFailureFenced("ep", SoftFailureScopeMessages, SoftFailureCategoryConnection, 0)
	if !applied || count != 2 {
		t.Fatalf("当前 epoch 第二次记录: count=%d applied=%v", count, applied)
	}
	_, tripped, applied = m.RecordSoftFailureFenced("ep", SoftFailureScopeMessages, SoftFailureCategoryConnection, 0)
	if !applied || !tripped {
		t.Fatalf("达到阈值应 trip: tripped=%v applied=%v", tripped, applied)
	}
}

func TestSetScopedCooldownFencedWriteFencing(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")

	if ok := m.SetScopedCooldownFenced("ep", SoftFailureScopeCountTokens, time.Minute, "soft_failure_server_error", 999); ok {
		t.Fatal("旧 epoch 应拒绝写入 scoped cooldown")
	}
	if active, _, _ := m.ScopedCooldownActive("ep", SoftFailureScopeCountTokens); active {
		t.Fatal("旧 epoch 写入后不应有 scoped cooldown")
	}

	if ok := m.SetScopedCooldownFenced("ep", SoftFailureScopeCountTokens, time.Minute, "soft_failure_server_error", 0); !ok {
		t.Fatal("当前 epoch 应正常写入 scoped cooldown")
	}
	if active, until, reason := m.ScopedCooldownActive("ep", SoftFailureScopeCountTokens); !active || until.IsZero() || reason != "soft_failure_server_error" {
		t.Fatalf("scoped cooldown 应生效: active=%v reason=%s", active, reason)
	}
}

func TestSetEndpointCooldownFencedWriteFencing(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")

	if ok := m.SetEndpointCooldownFenced("ep", time.Minute, "soft_failure_server_error", 999); ok {
		t.Fatal("旧 epoch 应拒绝写入冷却槽")
	}
	if m.IsEndpointInCooldown("ep") {
		t.Fatal("旧 epoch 写入后不应有冷却")
	}

	// 当前 epoch 正常写槽,并触发 persist hook
	called := make(chan struct{}, 1)
	m.SetCooldownPersistHook(func(name string, until time.Time, reason string, revision int64) {
		called <- struct{}{}
	})
	if ok := m.SetEndpointCooldownFenced("ep", time.Minute, "soft_failure_server_error", 0); !ok {
		t.Fatal("当前 epoch 应正常写入冷却槽")
	}
	if !m.IsEndpointInCooldown("ep") {
		t.Fatal("fenced 写槽后应处于冷却")
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("fenced 写槽应触发 persist hook")
	}
}

// TestMultipleStaleAttemptsAllRejected 评审场景回归:
// 捕获旧 epoch → Reset → 以旧 epoch 连续 fenced 记录 N 次(跨类别、跨 scope)
// → 各 scope/category 计数全 0、无 scoped cooldown、无冷却槽写入
func TestMultipleStaleAttemptsAllRejected(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")

	m.ResetEndpointFailureState("ep") // 旧 epoch 0 作废,当前 1

	m.RecordSoftFailureFenced("ep", SoftFailureScopeMessages, SoftFailureCategoryConnection, 0)
	m.RecordSoftFailureFenced("ep", SoftFailureScopeMessages, SoftFailureCategoryServerError, 0)
	m.RecordSoftFailureFenced("ep", SoftFailureScopeMessages, SoftFailureCategoryRateLimit, 0)
	m.RecordSoftFailureFenced("ep", SoftFailureScopeCountTokens, SoftFailureCategoryRateLimit, 0)
	m.SetScopedCooldownFenced("ep", SoftFailureScopeCountTokens, time.Minute, "soft_failure_rate_limit", 0)
	m.SetEndpointCooldownFenced("ep", time.Minute, "soft_failure_server_error", 0)

	for _, scope := range []SoftFailureScope{SoftFailureScopeMessages, SoftFailureScopeCountTokens} {
		if counts := m.GetSoftFailureCounts("ep", scope); len(counts) != 0 {
			t.Fatalf("旧 epoch 记录不应残留, scope=%s: %+v", scope, counts)
		}
	}
	if active, _, _ := m.ScopedCooldownActive("ep", SoftFailureScopeCountTokens); active {
		t.Fatal("旧 epoch 不应写入 scoped cooldown")
	}
	if m.IsEndpointInCooldown("ep") {
		t.Fatal("旧 epoch 不应写入冷却槽")
	}
}

func TestResetEndpointFailureStateDoesNotTriggerPersistHook(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")
	var calls atomic.Int64
	m.SetCooldownPersistHook(func(name string, until time.Time, reason string, revision int64) {
		calls.Add(1)
	})

	m.ResetEndpointFailureState("ep")
	time.Sleep(50 * time.Millisecond) // 等待可能的异步派发
	if calls.Load() != 0 {
		t.Fatalf("Reset 不应触发 persist hook, calls=%d", calls.Load())
	}
}

func TestResetAndFencedUnknownEndpoint(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")

	revision, found := m.ResetEndpointFailureState("unknown")
	if found || revision != 0 {
		t.Fatalf("未知端点 Reset 应返回零值: revision=%d found=%v", revision, found)
	}
	if _, _, applied := m.RecordSoftFailureFenced("unknown", SoftFailureScopeMessages, SoftFailureCategoryConnection, 0); applied {
		t.Fatal("未知端点 fenced 软失败应拒绝")
	}
	if ok := m.SetScopedCooldownFenced("unknown", SoftFailureScopeCountTokens, time.Minute, "x", 0); ok {
		t.Fatal("未知端点 fenced scoped cooldown 应拒绝")
	}
	if ok := m.SetEndpointCooldownFenced("unknown", time.Minute, "x", 0); ok {
		t.Fatal("未知端点 fenced 冷却应拒绝")
	}
}

// TestRecordSoftFailureFencedHoldsRLockAcrossWrite 确定性验证写时 fencing 的 RLock 覆盖:
// 锁住叶子锁(SoftFailureTracker.mu)后启动 fenced 写者——写者通过 epoch 校验后
// 必阻塞在叶子锁上,期间持有 ep.mutex.RLock。此时:
//  1. TryLock 探测写锁必然失败(写者持 RLock)→ 旧实现(校验后释放 RLock 再落写)
//     此处探测必然成功,确定性失败;
//  2. 并发 Reset 必须等待写者释放 RLock(50ms 窗口内不得完成)→ 清理严格
//     happens-after 写入,终态零残留。
func TestRecordSoftFailureFencedHoldsRLockAcrossWrite(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")
	ep := m.GetEndpointByNameAny("ep")

	m.softFailures.mu.Lock()
	leafReleased := false
	releaseLeaf := func() {
		if !leafReleased {
			m.softFailures.mu.Unlock()
			leafReleased = true
		}
	}
	defer releaseLeaf()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		count, _, applied := m.RecordSoftFailureFenced("ep", SoftFailureScopeMessages, SoftFailureCategoryConnection, 0)
		if !applied || count != 1 {
			t.Errorf("写者应成功记录: count=%d applied=%v", count, applied)
		}
	}()

	// 轮询探测:写者通过 epoch 校验并持有 ep.mutex.RLock(阻塞在叶子锁上)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if ep.mutex.TryLock() {
			ep.mutex.Unlock() // 探测成功:锁空闲,释放后继续等待
			if time.Now().After(deadline) {
				t.Fatal("写者未持有 ep.mutex.RLock——RLock 未覆盖实际写入(旧实现特征)")
			}
			time.Sleep(time.Millisecond)
			continue
		}
		break // TryLock 失败:写者持有 RLock
	}

	// 写者持 RLock 期间,Reset 必须等待
	resetDone := make(chan struct{})
	go func() {
		defer close(resetDone)
		m.ResetEndpointFailureState("ep")
	}()
	select {
	case <-resetDone:
		t.Fatal("Reset 不应在写者释放 RLock 前完成——fenced 写入未与 Reset 真互斥")
	case <-time.After(50 * time.Millisecond):
	}

	// 释放叶子锁:写者完成写入并释放 RLock;Reset 随后取得写锁执行清理
	releaseLeaf()
	<-writerDone
	<-resetDone

	if counts := m.GetSoftFailureCounts("ep", SoftFailureScopeMessages); len(counts) != 0 {
		t.Fatalf("Reset 后不应残留计数: %+v", counts)
	}
}

// TestSetScopedCooldownFencedHoldsRLockAcrossWrite 同上的确定性验证,
// 作用于 SetScopedCooldownFenced(scopedCooldowns.mu 为叶子锁)
func TestSetScopedCooldownFencedHoldsRLockAcrossWrite(t *testing.T) {
	m := testManagerWithFailureTracker(t, "ep")
	ep := m.GetEndpointByNameAny("ep")

	m.scopedCooldowns.mu.Lock()
	leafReleased := false
	releaseLeaf := func() {
		if !leafReleased {
			m.scopedCooldowns.mu.Unlock()
			leafReleased = true
		}
	}
	defer releaseLeaf()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		if ok := m.SetScopedCooldownFenced("ep", SoftFailureScopeCountTokens, time.Minute, "soft_failure_server_error", 0); !ok {
			t.Error("写者应成功写入 scoped cooldown")
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if ep.mutex.TryLock() {
			ep.mutex.Unlock()
			if time.Now().After(deadline) {
				t.Fatal("写者未持有 ep.mutex.RLock——RLock 未覆盖实际写入(旧实现特征)")
			}
			time.Sleep(time.Millisecond)
			continue
		}
		break
	}

	resetDone := make(chan struct{})
	go func() {
		defer close(resetDone)
		m.ResetEndpointFailureState("ep")
	}()
	select {
	case <-resetDone:
		t.Fatal("Reset 不应在写者释放 RLock 前完成——fenced 写入未与 Reset 真互斥")
	case <-time.After(50 * time.Millisecond):
	}

	releaseLeaf()
	<-writerDone
	<-resetDone

	if active, _, _ := m.ScopedCooldownActive("ep", SoftFailureScopeCountTokens); active {
		t.Fatal("Reset 后不应残留 scoped cooldown")
	}
}
