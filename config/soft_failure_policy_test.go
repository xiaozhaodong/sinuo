package config

import (
	"testing"
	"time"
)

func softPolicyInt(value int) *int { return &value }

func TestSoftFailurePolicy_默认继承与独立覆盖(t *testing.T) {
	if got := ResolveSoftFailurePolicy(nil, nil, 0, 0); got.Window != 5*time.Minute || got.Threshold != 3 {
		t.Fatalf("默认策略错误: %+v", got)
	}
	if got := ResolveSoftFailurePolicy(nil, nil, 7*time.Minute, 4); got.Window != 7*time.Minute || got.Threshold != 4 {
		t.Fatalf("未继承全局策略: %+v", got)
	}
	window, threshold := 10, 8
	if got := ResolveSoftFailurePolicy(&window, &threshold, time.Minute, 1); got.Window != 10*time.Minute || got.Threshold != 8 {
		t.Fatalf("独立策略未覆盖: %+v", got)
	}
}

func TestSoftFailurePolicy_拒绝不完整和非法覆盖(t *testing.T) {
	for _, tc := range []struct {
		name              string
		window, threshold *int
		valid             bool
	}{
		{"继承", nil, nil, true},
		{"自定义", softPolicyInt(1), softPolicyInt(1), true},
		{"缺少阈值", softPolicyInt(5), nil, false},
		{"缺少窗口", nil, softPolicyInt(3), false},
		{"零窗口", softPolicyInt(0), softPolicyInt(3), false},
		{"负窗口", softPolicyInt(-1), softPolicyInt(3), false},
		{"零阈值", softPolicyInt(5), softPolicyInt(0), false},
		{"负阈值", softPolicyInt(5), softPolicyInt(-3), false},
		{"时长溢出", softPolicyInt(int(MaxSoftFailureWindowMinutes) + 1), softPolicyInt(3), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSoftFailureOverrides(tc.window, tc.threshold); (err == nil) != tc.valid {
				t.Fatalf("校验结果不符: %v", err)
			}
		})
	}
}

func TestSoftFailurePolicy_区分继承并隔离指针(t *testing.T) {
	window, threshold := 5, 3
	if SameSoftFailureOverrides(nil, nil, &window, &threshold) {
		t.Fatal("继承与显式覆盖必须可区分")
	}
	cloned := CloneSoftFailureValue(&window)
	*cloned = 10
	if window != 5 || CloneSoftFailureValue(nil) != nil {
		t.Fatal("配置快照共享了可变指针")
	}
	if !SameSoftFailureOverrides(&window, &threshold, softPolicyInt(5), softPolicyInt(3)) {
		t.Fatal("值相同的覆盖配置应相等")
	}
}
