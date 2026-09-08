package config

import (
	"fmt"
	"time"
)

const (
	DefaultSoftFailureWindow    = 5 * time.Minute
	DefaultSoftFailureThreshold = 3
	// MaxSoftFailureWindowMinutes 保证分钟换算不会溢出 time.Duration。
	MaxSoftFailureWindowMinutes = int64(^uint64(0)>>1) / int64(time.Minute)
)

// SoftFailurePolicy 是解析后的值快照，不持有可变的配置指针。
type SoftFailurePolicy struct {
	Window    time.Duration
	Threshold int
}

// ValidateSoftFailureOverrides 空值对表示继承，覆盖值必须成对提供。
func ValidateSoftFailureOverrides(windowMinutes, threshold *int) error {
	if windowMinutes == nil && threshold == nil {
		return nil
	}
	if windowMinutes == nil || threshold == nil {
		return fmt.Errorf("失败统计窗口和失败阈值必须同时填写或同时留空")
	}
	if *windowMinutes <= 0 || int64(*windowMinutes) > MaxSoftFailureWindowMinutes {
		return fmt.Errorf("失败统计窗口必须为 1 至 %d 分钟的整数", MaxSoftFailureWindowMinutes)
	}
	if *threshold <= 0 {
		return fmt.Errorf("失败阈值必须为正整数")
	}
	return nil
}

// ResolveSoftFailurePolicy 只在边界校验通过后调用；未覆盖时沿用全局配置。
func ResolveSoftFailurePolicy(windowMinutes, threshold *int, defaultWindow time.Duration, defaultThreshold int) SoftFailurePolicy {
	if defaultWindow <= 0 {
		defaultWindow = DefaultSoftFailureWindow
	}
	if defaultThreshold <= 0 {
		defaultThreshold = DefaultSoftFailureThreshold
	}
	policy := SoftFailurePolicy{Window: defaultWindow, Threshold: defaultThreshold}
	if windowMinutes != nil {
		policy.Window = time.Duration(*windowMinutes) * time.Minute
	}
	if threshold != nil {
		policy.Threshold = *threshold
	}
	return policy
}

// SameSoftFailureOverrides 比较覆盖配置，继承与显式填写相同数值仍是不同策略。
func SameSoftFailureOverrides(windowA, thresholdA, windowB, thresholdB *int) bool {
	equal := func(a, b *int) bool {
		return a == nil && b == nil || a != nil && b != nil && *a == *b
	}
	return equal(windowA, windowB) && equal(thresholdA, thresholdB)
}

// CloneSoftFailureValue 防止编辑配置时修改已发布的运行时快照。
func CloneSoftFailureValue(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
