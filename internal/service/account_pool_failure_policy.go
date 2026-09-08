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
	s.accountStateMu.Lock()
	defer s.accountStateMu.Unlock()

	shouldFailover := false
	_, err := s.publishAccountRuntime(id, accountRuntimeMutationTransientFailure, func() (uint64, error) {
		account, ok := s.runtimeCache.get(id)
		if !ok {
			return 0, fmt.Errorf("账号不存在: %d", id)
		}
		if account.State == "disabled_auth" {
			shouldFailover = true
			return 0, nil
		}
		if err := config.ValidateSoftFailureOverrides(account.SoftFailureWindowMinutes, account.SoftFailureThreshold); err != nil {
			return 0, err
		}
		policy := config.ResolveSoftFailurePolicy(account.SoftFailureWindowMinutes, account.SoftFailureThreshold,
			accountPoolSoftFailureWindow(s.config), accountPoolSoftFailureThreshold(s.config))
		tracker := s.softFailureTracker
		now := tracker.Now()
		if tracker.Record(id, policy) < policy.Threshold {
			return 0, nil
		}
		cooldownUntil := now.Add(resolveAccountSoftFailureCooldown(s.config, category, retryAfter))
		ok, version := s.runtimeCache.markTransientFailure(id, reason, cooldownUntil, now)
		if !ok {
			return 0, fmt.Errorf("账号不存在: %d", id)
		}
		tracker.Clear(id)
		shouldFailover = true
		return version, nil
	})
	return shouldFailover, err
}
