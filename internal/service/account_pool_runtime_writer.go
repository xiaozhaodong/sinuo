package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"cc-forwarder/internal/store"
)

const (
	defaultAccountRuntimeWriterFlushInterval = 200 * time.Millisecond
	defaultAccountRuntimeWriterFlushTimeout  = 5 * time.Second
)

type accountRuntimeMutationKind string

const (
	accountRuntimeMutationSuccess          accountRuntimeMutationKind = "success"
	accountRuntimeMutationAuthFailed       accountRuntimeMutationKind = "auth_failed"
	accountRuntimeMutationTransientFailure accountRuntimeMutationKind = "transient_failure"
	accountRuntimeMutationClearCooldown    accountRuntimeMutationKind = "clear_cooldown"
)

type accountRuntimeMutation struct {
	accountID        int64
	kind             accountRuntimeMutationKind
	stateVersion     uint64
	reason           string
	cooldownUntil    time.Time
	successAt        time.Time
	attemptStartedAt time.Time
	eventAt          time.Time
	failureDelta     int
}

type accountPoolRuntimeWriter struct {
	store  store.AccountPoolStore
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	pending  map[int64]accountRuntimeMutation
	closing  bool
	closed   bool
	closeErr error

	isCurrentVersion func(id int64, version uint64) bool
	wg               sync.WaitGroup
}

func newAccountPoolRuntimeWriter(parent context.Context, st store.AccountPoolStore, isCurrentVersion func(id int64, version uint64) bool) *accountPoolRuntimeWriter {
	if st == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}

	ctx, cancel := context.WithCancel(parent)
	writer := &accountPoolRuntimeWriter{
		store:            st,
		ctx:              ctx,
		cancel:           cancel,
		pending:          make(map[int64]accountRuntimeMutation),
		isCurrentVersion: isCurrentVersion,
	}
	writer.wg.Add(1)
	go writer.run()
	return writer
}

func (w *accountPoolRuntimeWriter) EnqueueClearCooldown(accountID int64, stateVersion uint64, clearedAt time.Time) {
	if w == nil || accountID <= 0 {
		return
	}
	w.enqueue(accountRuntimeMutation{accountID: accountID, kind: accountRuntimeMutationClearCooldown, stateVersion: stateVersion, eventAt: clearedAt})
}

func (w *accountPoolRuntimeWriter) FlushAccount(ctx context.Context, accountID int64) error {
	if w == nil || accountID <= 0 {
		return nil
	}
	w.mu.Lock()
	mutation, ok := w.pending[accountID]
	if ok {
		delete(w.pending, accountID)
	}
	w.mu.Unlock()
	if !ok {
		return nil
	}
	return w.applyMutation(ctx, &mutation)
}

func (w *accountPoolRuntimeWriter) Close() error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	if w.closed || w.closing {
		w.mu.Unlock()
		return nil
	}
	w.closing = true
	w.mu.Unlock()

	w.cancel()
	w.wg.Wait()

	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return w.closeErr
}

func (w *accountPoolRuntimeWriter) Drop(accountID int64) {
	if w == nil || accountID <= 0 {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.pending, accountID)
}

func (w *accountPoolRuntimeWriter) DropMany(accountIDs []int64) {
	if w == nil || len(accountIDs) == 0 {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			continue
		}
		delete(w.pending, accountID)
	}
}

func (w *accountPoolRuntimeWriter) EnqueueSuccess(accountID int64, stateVersion uint64, successAt, attemptStartedAt time.Time) {
	if w == nil || accountID <= 0 {
		return
	}
	w.enqueue(accountRuntimeMutation{
		accountID:        accountID,
		kind:             accountRuntimeMutationSuccess,
		stateVersion:     stateVersion,
		successAt:        successAt,
		attemptStartedAt: attemptStartedAt,
		eventAt:          successAt,
	})
}

func (w *accountPoolRuntimeWriter) EnqueueAuthFailed(accountID int64, stateVersion uint64, reason string, failedAt time.Time) {
	if w == nil || accountID <= 0 {
		return
	}
	w.enqueue(accountRuntimeMutation{
		accountID:    accountID,
		kind:         accountRuntimeMutationAuthFailed,
		stateVersion: stateVersion,
		reason:       reason,
		eventAt:      failedAt,
	})
}

func (w *accountPoolRuntimeWriter) EnqueueTransientFailure(accountID int64, stateVersion uint64, reason string, cooldownUntil, failedAt time.Time) {
	if w == nil || accountID <= 0 {
		return
	}
	w.enqueue(accountRuntimeMutation{
		accountID:     accountID,
		kind:          accountRuntimeMutationTransientFailure,
		stateVersion:  stateVersion,
		reason:        reason,
		cooldownUntil: cooldownUntil,
		eventAt:       failedAt,
		failureDelta:  1,
	})
}

func (w *accountPoolRuntimeWriter) enqueue(next accountRuntimeMutation) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.closing {
		return
	}

	current, ok := w.pending[next.accountID]
	if !ok {
		w.pending[next.accountID] = next
		return
	}
	w.pending[next.accountID] = mergeAccountRuntimeMutation(current, next)
}

func (w *accountPoolRuntimeWriter) run() {
	defer w.wg.Done()

	ticker := time.NewTicker(defaultAccountRuntimeWriterFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_ = w.flushPending(true)
		case <-w.ctx.Done():
			_ = w.flushPending(false)
			return
		}
	}
}

func (w *accountPoolRuntimeWriter) flushPending(requeueOnFailure bool) error {
	if w == nil {
		return nil
	}

	pending := w.detachPending()
	if len(pending) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultAccountRuntimeWriterFlushTimeout)
	defer cancel()

	var firstErr error
	for _, pendingMutation := range pending {
		mutation := pendingMutation
		if err := w.applyMutation(ctx, &mutation); err != nil {
			slog.Warn("账号池运行时写回失败，稍后重试",
				"account_id", mutation.accountID,
				"kind", mutation.kind,
				"error", err)
			if firstErr == nil {
				firstErr = err
			}
			if requeueOnFailure {
				w.enqueue(mutation)
			}
		}
	}
	if !requeueOnFailure && firstErr != nil {
		w.mu.Lock()
		if w.closeErr == nil {
			w.closeErr = firstErr
		}
		w.mu.Unlock()
	}
	return firstErr
}

func (w *accountPoolRuntimeWriter) detachPending() []accountRuntimeMutation {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.pending) == 0 {
		return nil
	}

	mutations := make([]accountRuntimeMutation, 0, len(w.pending))
	for accountID, mutation := range w.pending {
		if accountID <= 0 {
			continue
		}
		mutations = append(mutations, mutation)
	}
	w.pending = make(map[int64]accountRuntimeMutation)
	return mutations
}

func (w *accountPoolRuntimeWriter) applyMutation(ctx context.Context, mutation *accountRuntimeMutation) error {
	if mutation == nil || mutation.stateVersion == 0 {
		return nil
	}
	if w.isCurrentVersion != nil && !w.isCurrentVersion(mutation.accountID, mutation.stateVersion) {
		return nil
	}

	switch mutation.kind {
	case accountRuntimeMutationSuccess:
		_, err := w.store.MarkAccountSuccessIfNoNewerFailure(ctx, mutation.accountID, mutation.successAt, mutation.attemptStartedAt)
		return err
	case accountRuntimeMutationAuthFailed:
		return w.store.MarkAccountAuthFailed(ctx, mutation.accountID, mutation.reason)
	case accountRuntimeMutationTransientFailure:
		repeats := mutation.failureDelta
		if repeats <= 0 {
			repeats = 1
		}
		for i := 0; i < repeats; i++ {
			if err := w.store.MarkAccountTransientFailure(ctx, mutation.accountID, mutation.reason, mutation.cooldownUntil); err != nil {
				mutation.failureDelta = repeats - i
				return err
			}
		}
		mutation.failureDelta = 0
		return nil
	case accountRuntimeMutationClearCooldown:
		clearer, ok := w.store.(interface {
			ClearAccountCooldown(context.Context, int64, time.Time) error
		})
		if !ok {
			return fmt.Errorf("账号存储不支持清除冷却")
		}
		return clearer.ClearAccountCooldown(ctx, mutation.accountID, mutation.eventAt)
	default:
		return fmt.Errorf("unknown account runtime mutation kind: %s", mutation.kind)
	}
}

func mergeAccountRuntimeMutation(current, next accountRuntimeMutation) accountRuntimeMutation {
	if current.accountID == 0 {
		return next
	}
	if next.kind == accountRuntimeMutationAuthFailed {
		return next
	}
	if current.kind == accountRuntimeMutationAuthFailed {
		return current
	}

	switch next.kind {
	case accountRuntimeMutationTransientFailure:
		if current.kind == accountRuntimeMutationTransientFailure {
			next.failureDelta += current.failureDelta
		}
		return next
	case accountRuntimeMutationSuccess:
		switch current.kind {
		case accountRuntimeMutationTransientFailure:
			if !next.attemptStartedAt.After(current.eventAt) {
				return current
			}
			return next
		case accountRuntimeMutationSuccess:
			if next.attemptStartedAt.After(current.attemptStartedAt) {
				return next
			}
			if next.attemptStartedAt.Equal(current.attemptStartedAt) && next.successAt.After(current.successAt) {
				return next
			}
			return current
		default:
			return next
		}
	default:
		return next
	}
}
