package endpoint

import (
	"sync"
	"testing"
	"time"

	"cc-forwarder/config"
)

func endpointPolicyInt(value int) *int { return &value }

func TestSoftFailurePolicy_独立窗口清理与统计(t *testing.T) {
	tracker := NewSoftFailureTracker(true, 5*time.Minute, 3)
	now := time.Now()
	tracker.now = func() time.Time { return now }
	long, threshold := 10, 3
	short := 2
	tracker.Record("long", SoftFailureScopeMessages, SoftFailureCategoryRateLimit, &long, &threshold)
	tracker.Record("long", SoftFailureScopeMessages, SoftFailureCategoryServerError, &long, &threshold)
	tracker.Record("long", SoftFailureScopeCountTokens, SoftFailureCategoryRateLimit, &long, &threshold)
	tracker.Record("short", SoftFailureScopeMessages, SoftFailureCategoryRateLimit, &short, &threshold)
	tracker.Record("default", SoftFailureScopeMessages, SoftFailureCategoryRateLimit, nil, nil)
	now = now.Add(6 * time.Minute)
	tracker.CleanupExpiredEvents()
	if stats := tracker.Stats(); stats["long"] != 3 || stats["short"] != 0 || stats["default"] != 0 {
		t.Fatalf("窗口清理错误: %v", stats)
	}
	for want := 2; want <= 3; want++ {
		count, hit := tracker.Record("long", SoftFailureScopeMessages, SoftFailureCategoryRateLimit, &long, &threshold)
		if count != want || hit != (want == 3) {
			t.Fatalf("阈值不符: count=%d hit=%v", count, hit)
		}
	}
	if counts := tracker.CountsFor("long", SoftFailureScopeMessages); counts[SoftFailureCategoryRateLimit] != 0 || counts[SoftFailureCategoryServerError] != 1 {
		t.Fatalf("错误类别互相影响: %v", counts)
	}
	if counts := tracker.CountsFor("long", SoftFailureScopeCountTokens); counts[SoftFailureCategoryRateLimit] != 1 {
		t.Fatalf("接口范围互相影响: %v", counts)
	}
	tracker.UpdateConfig(true, time.Minute, 1)
	tracker.CleanupExpiredEvents()
	if tracker.Stats()["long"] != 2 {
		t.Fatal("全局设置修改清除了自定义窗口")
	}
}

func TestSoftFailurePolicy_策略更新隔离旧结算并保留冷却(t *testing.T) {
	cfg := &config.Config{
		FailureTracker: config.FailureTrackerConfig{Enabled: true, TimeWindow: 5 * time.Minute, Threshold: 3},
		Endpoints:      []config.EndpointConfig{{Name: "a", URL: "https://example.test"}, {Name: "b", URL: "https://example.test"}},
	}
	m := NewManager(cfg)
	t.Cleanup(m.Stop)
	for _, name := range []string{"a", "b"} {
		for i := 0; i < 2; i++ {
			m.RecordSoftFailure(name, SoftFailureScopeMessages, SoftFailureCategoryRateLimit)
		}
	}
	ep := m.GetEndpointByNameAny("a")
	oldEpoch := ep.failureEpoch
	m.SetEndpointCooldown("a", time.Hour, "existing-cooldown")
	_, before, _ := m.GetEndpointCooldownInfo("a")
	updated := cfg.Endpoints[0]
	updated.SoftFailureWindowMinutes, updated.SoftFailureThreshold = endpointPolicyInt(10), endpointPolicyInt(4)
	if err := m.UpdateEndpointConfig("a", updated); err != nil {
		t.Fatal(err)
	}
	if counts := m.GetSoftFailureCounts("a", SoftFailureScopeMessages); len(counts) != 0 {
		t.Fatal("策略修改未清除计数")
	}
	if count, hit, applied := m.RecordSoftFailureFenced("a", SoftFailureScopeMessages, SoftFailureCategoryRateLimit, oldEpoch); count != 0 || hit || applied {
		t.Fatal("旧策略结算污染新窗口")
	}
	if inCooldown, after, _ := m.GetEndpointCooldownInfo("a"); !inCooldown || !after.Equal(before) {
		t.Fatal("修改策略提前解除了冷却")
	}
	if count, hit := m.RecordSoftFailure("b", SoftFailureScopeMessages, SoftFailureCategoryRateLimit); count != 3 || !hit {
		t.Fatal("其他端点的计数被重置")
	}
	for want := 1; want <= 4; want++ {
		count, hit := m.RecordSoftFailure("a", SoftFailureScopeMessages, SoftFailureCategoryRateLimit)
		if count != want || hit != (want == 4) {
			t.Fatalf("自定义阈值未生效: count=%d hit=%v", count, hit)
		}
	}
	if m.SoftFailureThreshold("a") != 4 {
		t.Fatal("展示的阈值与实际策略不一致")
	}
	*updated.SoftFailureThreshold = 1
	if m.SoftFailureThreshold("a") != 4 {
		t.Fatal("外部配置指针改变了运行态")
	}
}

func TestSoftFailurePolicy_普通编辑保留计数且恢复默认重置(t *testing.T) {
	cfg := config.EndpointConfig{Name: "a", URL: "https://example.test", SoftFailureWindowMinutes: endpointPolicyInt(10), SoftFailureThreshold: endpointPolicyInt(3)}
	m := NewManager(&config.Config{FailureTracker: config.FailureTrackerConfig{Enabled: true, TimeWindow: 5 * time.Minute, Threshold: 3}, Endpoints: []config.EndpointConfig{cfg}})
	t.Cleanup(m.Stop)
	m.RecordSoftFailure("a", SoftFailureScopeMessages, SoftFailureCategoryRateLimit)
	cfg.Priority = 20
	if err := m.UpdateEndpointConfig("a", cfg); err != nil {
		t.Fatal(err)
	}
	if count, hit := m.RecordSoftFailure("a", SoftFailureScopeMessages, SoftFailureCategoryRateLimit); count != 2 || hit {
		t.Fatal("普通编辑清空了计数")
	}
	cfg.SoftFailureWindowMinutes, cfg.SoftFailureThreshold = nil, nil
	if err := m.UpdateEndpointConfig("a", cfg); err != nil {
		t.Fatal(err)
	}
	if count, hit := m.RecordSoftFailure("a", SoftFailureScopeMessages, SoftFailureCategoryRateLimit); count != 1 || hit {
		t.Fatal("恢复默认未重新累计")
	}
}

func TestSoftFailurePolicy_并发修改和分类计数(t *testing.T) {
	cfg := config.EndpointConfig{Name: "a", URL: "https://example.test", SoftFailureWindowMinutes: endpointPolicyInt(2), SoftFailureThreshold: endpointPolicyInt(1000)}
	m := NewManager(&config.Config{FailureTracker: config.FailureTrackerConfig{Enabled: true, TimeWindow: 5 * time.Minute, Threshold: 3}, Endpoints: []config.EndpointConfig{cfg}})
	t.Cleanup(m.Stop)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			m.RecordSoftFailure("a", SoftFailureScopeMessages, SoftFailureCategoryRateLimit)
			m.softFailures.CleanupExpiredEvents()
			m.GetFailureStats()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			input := cfg
			input.SoftFailureWindowMinutes = endpointPolicyInt(2 + i%2)
			if err := m.UpdateEndpointConfig("a", input); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	if m.SoftFailureThreshold("a") != 1000 {
		t.Fatal("并发发布丢失自定义阈值")
	}
}

func TestSoftFailurePolicy_全局更新只重置变化的继承策略(t *testing.T) {
	for _, tt := range []struct {
		name                       string
		oldWindow, newWindow       time.Duration
		oldThreshold, newThreshold int
		changed                    bool
	}{
		{"缩短窗口", 10 * time.Minute, 5 * time.Minute, 3, 3, true},
		{"延长窗口", 5 * time.Minute, 10 * time.Minute, 3, 3, true},
		{"降低阈值", 5 * time.Minute, 5 * time.Minute, 5, 3, true},
		{"提高阈值", 5 * time.Minute, 5 * time.Minute, 3, 5, true},
		{"相同配置", 5 * time.Minute, 5 * time.Minute, 3, 3, false},
		{"默认值等价", 0, 5 * time.Minute, 0, 3, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewSoftFailureTracker(true, tt.oldWindow, tt.oldThreshold)
			for _, scope := range []SoftFailureScope{SoftFailureScopeMessages, SoftFailureScopeCountTokens} {
				for _, category := range []SoftFailureCategory{SoftFailureCategoryRateLimit, SoftFailureCategoryServerError} {
					for i := 0; i < 2; i++ {
						tracker.Record("inherited", scope, category, nil, nil)
						tracker.Record("custom", scope, category, endpointPolicyInt(10), endpointPolicyInt(5))
					}
				}
			}
			tracker.UpdateConfig(true, tt.newWindow, tt.newThreshold)
			wantInherited := 2
			if tt.changed {
				wantInherited = 0
			}
			for _, scope := range []SoftFailureScope{SoftFailureScopeMessages, SoftFailureScopeCountTokens} {
				for _, category := range []SoftFailureCategory{SoftFailureCategoryRateLimit, SoftFailureCategoryServerError} {
					if got := tracker.CountsFor("inherited", scope)[category]; got != wantInherited {
						t.Errorf("继承策略 %s/%s: count=%d want=%d", scope, category, got, wantInherited)
					}
					if got := tracker.CountsFor("custom", scope)[category]; got != 2 {
						t.Errorf("自定义策略 %s/%s 的计数改变: %d", scope, category, got)
					}
				}
			}
			count, hit := tracker.Record("inherited", SoftFailureScopeMessages, SoftFailureCategoryServerError, nil, nil)
			if count != wantInherited+1 || hit != (count >= tt.newThreshold) {
				t.Errorf("更新后计数错误: count=%d hit=%v", count, hit)
			}
			tracker.UpdateConfig(false, tt.newWindow, tt.newThreshold)
			if len(tracker.events) != 0 {
				t.Fatal("禁用后仍保留累计事件")
			}
		})
	}
}

func TestSoftFailurePolicy_全局更新隔离旧结算并保留冷却(t *testing.T) {
	cfg := &config.Config{
		FailureTracker: config.FailureTrackerConfig{Enabled: true, TimeWindow: 5 * time.Minute, Threshold: 3},
		Endpoints: []config.EndpointConfig{
			{Name: "inherited", URL: "https://example.test"},
			{Name: "empty", URL: "https://example.test"},
			{Name: "custom", URL: "https://example.test", SoftFailureWindowMinutes: endpointPolicyInt(10), SoftFailureThreshold: endpointPolicyInt(5)},
		},
	}
	m := NewManager(cfg)
	t.Cleanup(m.Stop)
	type generation struct{ revision, epoch int64 }
	before := make(map[string]generation)
	for _, name := range []string{"inherited", "empty", "custom"} {
		ep := m.GetEndpointByNameAny(name)
		before[name] = generation{ep.configRevision, ep.failureEpoch}
		if name != "empty" {
			for i := 0; i < 2; i++ {
				m.RecordSoftFailure(name, SoftFailureScopeMessages, SoftFailureCategoryRateLimit)
			}
		}
	}
	m.SetEndpointCooldown("inherited", time.Hour, "existing-cooldown")
	m.SetScopedCooldown("inherited", SoftFailureScopeCountTokens, time.Hour, "existing-scoped-cooldown")
	_, cooldownBefore, reasonBefore := m.GetEndpointCooldownInfo("inherited")
	_, scopedBefore, scopedReasonBefore := m.ScopedCooldownActive("inherited", SoftFailureScopeCountTokens)

	// 设置页会原地修改同一个配置对象，旧策略必须从 tracker 的值快照判断。
	cfg.FailureTracker.TimeWindow = 10 * time.Minute
	cfg.FailureTracker.Threshold = 4
	m.UpdateConfig(cfg)
	for _, name := range []string{"inherited", "empty"} {
		old := before[name]
		ep := m.GetEndpointByNameAny(name)
		if ep.configRevision == old.revision || ep.failureEpoch == old.epoch {
			t.Errorf("%s 未推进策略版本", name)
		}
		if count, hit, applied := m.RecordSoftFailureFenced(name, SoftFailureScopeMessages, SoftFailureCategoryRateLimit, old.epoch); count != 0 || hit || applied {
			t.Errorf("%s 的旧失败结果污染了新策略", name)
		}
		if m.ApplyEndpointAttemptSettlement(name, old.revision, func() { t.Errorf("%s 旧配置结算仍被执行", name) }) {
			t.Errorf("%s 仍接受旧配置 revision", name)
		}
	}
	old := before["inherited"]
	if m.SetEndpointCooldownFenced("inherited", 2*time.Hour, "late-cooldown", old.epoch) ||
		m.SetScopedCooldownFenced("inherited", SoftFailureScopeCountTokens, 2*time.Hour, "late-scoped-cooldown", old.epoch) {
		t.Error("旧策略结算延长了已有冷却")
	}
	if active, until, reason := m.GetEndpointCooldownInfo("inherited"); !active || !until.Equal(cooldownBefore) || reason != reasonBefore {
		t.Error("全局策略更新改变了已有冷却")
	}
	if active, until, reason := m.ScopedCooldownActive("inherited", SoftFailureScopeCountTokens); !active || !until.Equal(scopedBefore) || reason != scopedReasonBefore {
		t.Error("全局策略更新改变了 count_tokens 冷却")
	}
	custom := m.GetEndpointByNameAny("custom")
	if custom.configRevision != before["custom"].revision || custom.failureEpoch != before["custom"].epoch {
		t.Error("全局策略更新使自定义端点的在途结果失效")
	}
	if count, hit := m.RecordSoftFailure("inherited", SoftFailureScopeMessages, SoftFailureCategoryRateLimit); count != 1 || hit {
		t.Errorf("继承端点未从新窗口开始: count=%d hit=%v", count, hit)
	}
	inherited := m.GetEndpointByNameAny("inherited")
	revision, epoch := inherited.configRevision, inherited.failureEpoch
	m.UpdateConfig(cfg)
	if inherited.configRevision != revision || inherited.failureEpoch != epoch {
		t.Error("相同全局配置重复发布改变了版本")
	}
	if count, hit := m.RecordSoftFailure("inherited", SoftFailureScopeMessages, SoftFailureCategoryRateLimit); count != 2 || hit {
		t.Error("相同全局配置重复发布清空了计数")
	}
	if count, hit := m.RecordSoftFailure("custom", SoftFailureScopeMessages, SoftFailureCategoryRateLimit); count != 3 || hit {
		t.Error("自定义端点的累计计数被清空")
	}
	m.UpdateFailureTrackerConfig(true, 12*time.Minute, 4)
	if _, _, applied := m.RecordSoftFailureFenced("inherited", SoftFailureScopeCountTokens, SoftFailureCategoryRateLimit, epoch); applied {
		t.Error("直接发布全局策略未隔离旧结算")
	}
}

func TestSoftFailurePolicy_全局发布与结算并发(t *testing.T) {
	m := NewManager(&config.Config{
		FailureTracker: config.FailureTrackerConfig{Enabled: true, TimeWindow: 5 * time.Minute, Threshold: 1000},
		Endpoints: []config.EndpointConfig{
			{Name: "inherited", URL: "https://example.test"},
			{Name: "custom", URL: "https://example.test", SoftFailureWindowMinutes: endpointPolicyInt(10), SoftFailureThreshold: endpointPolicyInt(1000)},
		},
	})
	t.Cleanup(m.Stop)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			m.UpdateFailureTrackerConfig(true, time.Duration(1+i%2)*time.Minute, 1000)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			ep := m.GetEndpointByNameAny("inherited")
			ep.mutex.RLock()
			epoch := ep.failureEpoch
			ep.mutex.RUnlock()
			m.RecordSoftFailureFenced("inherited", SoftFailureScopeMessages, SoftFailureCategoryRateLimit, epoch)
			m.RecordSoftFailure("custom", SoftFailureScopeMessages, SoftFailureCategoryRateLimit)
			m.GetFailureStats()
		}
	}()
	wg.Wait()
	m.UpdateFailureTrackerConfig(true, 7*time.Minute, 1000)
	if counts := m.GetSoftFailureCounts("inherited", SoftFailureScopeMessages); len(counts) != 0 {
		t.Fatalf("最终全局策略仍残留旧计数: %v", counts)
	}
	if got := m.GetSoftFailureCounts("custom", SoftFailureScopeMessages)[SoftFailureCategoryRateLimit]; got != 100 {
		t.Fatalf("自定义端点被全局更新影响: count=%d", got)
	}
}
