package service

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"sinuo/internal/accountauth"
	"sinuo/internal/store"
)

const (
	defaultAccountPoolQuotaProbeInterval = 10 * time.Minute
	defaultAccountPoolQuotaProbeStaleAge = 30 * time.Minute
	defaultAccountPoolQuotaProbeMaxBatch = 5
)

type accountPoolQuotaProbeScheduler struct {
	ctx        context.Context
	cancel     context.CancelFunc
	listFn     func(context.Context) ([]*store.UpstreamAccountRecord, error)
	enqueueFn  func(int64) bool
	now        func() time.Time
	interval   time.Duration
	staleAfter time.Duration
	maxBatch   int
	wg         sync.WaitGroup
}

func newAccountPoolQuotaProbeScheduler(parent context.Context, listFn func(context.Context) ([]*store.UpstreamAccountRecord, error), enqueueFn func(int64) bool) *accountPoolQuotaProbeScheduler {
	if listFn == nil || enqueueFn == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s := &accountPoolQuotaProbeScheduler{
		ctx:        ctx,
		cancel:     cancel,
		listFn:     listFn,
		enqueueFn:  enqueueFn,
		now:        time.Now,
		interval:   defaultAccountPoolQuotaProbeInterval,
		staleAfter: defaultAccountPoolQuotaProbeStaleAge,
		maxBatch:   defaultAccountPoolQuotaProbeMaxBatch,
	}
	s.wg.Add(1)
	go s.run()
	return s
}

func (s *accountPoolQuotaProbeScheduler) Close() error {
	if s == nil {
		return nil
	}
	s.cancel()
	s.wg.Wait()
	return nil
}

func (s *accountPoolQuotaProbeScheduler) run() {
	defer s.wg.Done()
	s.runProbeCycle()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.runProbeCycle()
		}
	}
}

func (s *accountPoolQuotaProbeScheduler) runProbeCycle() {
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()

	accounts, err := s.listFn(ctx)
	if err != nil {
		slog.Debug("账号池 quota probe 扫描失败", "error", err)
		return
	}

	candidateIDs := selectQuotaProbeAccountIDs(accounts, s.now(), s.staleAfter, s.maxBatch)
	for _, accountID := range candidateIDs {
		s.enqueueFn(accountID)
	}
}

func selectQuotaProbeAccountIDs(accounts []*store.UpstreamAccountRecord, now time.Time, staleAfter time.Duration, limit int) []int64 {
	type candidate struct {
		id          int64
		refreshedAt time.Time
	}

	candidates := make([]candidate, 0)
	for _, account := range accounts {
		if account == nil || account.ID <= 0 {
			continue
		}
		if !account.Enabled {
			continue
		}
		state := strings.TrimSpace(strings.ToLower(account.State))
		if state == "disabled_auth" || state == "disabled" {
			continue
		}
		if !accountauth.IsChatGPTOAuthProvider(account.ProviderType) {
			continue
		}
		if account.QuotaRefreshedAt != nil && !account.QuotaRefreshedAt.IsZero() && now.Sub(*account.QuotaRefreshedAt) < staleAfter {
			continue
		}
		refreshedAt := time.Time{}
		if account.QuotaRefreshedAt != nil {
			refreshedAt = *account.QuotaRefreshedAt
		}
		candidates = append(candidates, candidate{id: account.ID, refreshedAt: refreshedAt})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		switch {
		case left.refreshedAt.IsZero() && !right.refreshedAt.IsZero():
			return true
		case !left.refreshedAt.IsZero() && right.refreshedAt.IsZero():
			return false
		case left.refreshedAt.Equal(right.refreshedAt):
			return left.id < right.id
		default:
			return left.refreshedAt.Before(right.refreshedAt)
		}
	})

	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}

	ids := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.id)
	}
	return ids
}
