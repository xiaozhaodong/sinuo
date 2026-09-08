package service

import (
	"context"
	"fmt"
	"time"

	"cc-forwarder/config"
)

// RecordAccountSoftFailure 在同一个临界区读取当前策略、计数并结算冷却。
// UpdateAccount 使用同一把锁发布策略，避免保存后被旧策略的结算覆盖。
func (s *AccountPoolService) RecordAccountSoftFailure(ctx context.Context, id int64, reason, category string, retryAfter time.Duration) (bool, error) {
	if err := s.ensureRuntimeCache(ctx); err != nil {
		return false, err
	}
	if id <= 0 {
		return false, fmt.Errorf("invalid account id: %d", id)
	}
	s.failurePolicyMu.Lock()
	defer s.failurePolicyMu.Unlock()

	account, ok := s.runtimeCache.get(id)
	if !ok {
		return false, fmt.Errorf("账号不存在: %d", id)
	}
	if err := config.ValidateSoftFailureOverrides(account.SoftFailureWindowMinutes, account.SoftFailureThreshold); err != nil {
		return false, err
	}
	policy := config.ResolveSoftFailurePolicy(account.SoftFailureWindowMinutes, account.SoftFailureThreshold,
		accountPoolSoftFailureWindow(s.config), accountPoolSoftFailureThreshold(s.config))
	tracker := s.softFailureTracker
	now := tracker.Now()
	if tracker.Record(id, policy) < policy.Threshold {
		return false, nil
	}

	cooldownUntil := now.Add(resolveAccountSoftFailureCooldown(s.config, category, retryAfter))
	ok, stateVersion := s.runtimeCache.markTransientFailure(id, reason, cooldownUntil, now)
	if !ok {
		return false, fmt.Errorf("账号不存在: %d", id)
	}
	if s.runtimeWriter != nil {
		s.runtimeWriter.EnqueueTransientFailure(id, stateVersion, reason, cooldownUntil, now)
	}
	tracker.Clear(id)
	return true, nil
}
