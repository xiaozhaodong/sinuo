package service

import (
	"strings"
	"sync"
	"time"

	"sinuo/config"
)

const (
	accountSoftFailureCategoryRateLimit   = "rate_limit"
	accountSoftFailureCategoryServerError = "server_error"

	defaultAccountSoftFailureWindow    = config.DefaultSoftFailureWindow
	defaultAccountSoftFailureThreshold = config.DefaultSoftFailureThreshold
	defaultAccountRateLimitCooldown    = 180 * time.Second
	defaultAccountServerErrorCooldown  = 120 * time.Second
)

type accountSoftFailureTracker struct {
	mu     sync.Mutex
	events map[int64][]time.Time
	now    func() time.Time
}

func newAccountSoftFailureTracker() *accountSoftFailureTracker {
	return &accountSoftFailureTracker{
		events: make(map[int64][]time.Time),
		now:    time.Now,
	}
}

func (t *accountSoftFailureTracker) Now() time.Time {
	if t == nil || t.now == nil {
		return time.Now()
	}
	return t.now()
}

func (t *accountSoftFailureTracker) Record(accountID int64, policy config.SoftFailurePolicy) int {
	if t == nil || accountID <= 0 {
		return 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.Now()
	windowStart := now.Add(-policy.Window)
	valid := make([]time.Time, 0, len(t.events[accountID])+1)
	for _, item := range t.events[accountID] {
		if item.After(windowStart) {
			valid = append(valid, item)
		}
	}
	valid = append(valid, now)
	t.events[accountID] = valid
	return len(valid)
}

func (t *accountSoftFailureTracker) Clear(accountID int64) {
	if t == nil || accountID <= 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.events, accountID)
}

func resolveAccountSoftFailureCooldown(cfg *config.Config, category string, retryAfter time.Duration) time.Duration {
	switch strings.TrimSpace(strings.ToLower(category)) {
	case accountSoftFailureCategoryRateLimit:
		if accountPoolRespectRetryAfter(cfg) && retryAfter > 0 {
			return retryAfter
		}
		return accountPoolRateLimitDefaultCooldown(cfg)
	default:
		return accountPoolServerErrorCooldown(cfg)
	}
}

func accountPoolSoftFailureWindow(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.AccountPool.FailurePolicy.SoftFailureWindow > 0 {
		return cfg.AccountPool.FailurePolicy.SoftFailureWindow
	}
	return defaultAccountSoftFailureWindow
}

func accountPoolSoftFailureThreshold(cfg *config.Config) int {
	if cfg != nil && cfg.AccountPool.FailurePolicy.SoftFailureThreshold > 0 {
		return cfg.AccountPool.FailurePolicy.SoftFailureThreshold
	}
	return defaultAccountSoftFailureThreshold
}

func accountPoolRespectRetryAfter(cfg *config.Config) bool {
	if cfg != nil && cfg.AccountPool.FailurePolicy.RespectRetryAfter != nil {
		return *cfg.AccountPool.FailurePolicy.RespectRetryAfter
	}
	return true
}

func accountPoolRateLimitDefaultCooldown(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.AccountPool.FailurePolicy.Cooldowns.RateLimitDefault > 0 {
		return cfg.AccountPool.FailurePolicy.Cooldowns.RateLimitDefault
	}
	return defaultAccountRateLimitCooldown
}

func accountPoolServerErrorCooldown(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.AccountPool.FailurePolicy.Cooldowns.ServerError > 0 {
		return cfg.AccountPool.FailurePolicy.Cooldowns.ServerError
	}
	return defaultAccountServerErrorCooldown
}
