package service

import (
	"context"
	"errors"
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
)

type accountRuntimeMutation struct {
	accountID    int64
	kind         accountRuntimeMutationKind
	stateVersion uint64
	state        store.AccountRuntimeState
}

var errAccountRuntimeWriterClosed = errors.New("账号运行态写回已关闭")

type accountPoolRuntimeWriter struct {
	store  store.AccountPoolStore
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	pending   map[int64]accountRuntimeMutation
	closing   bool
	closed    bool
	closeErr  error
	closeDone chan struct{}

	flushMu          sync.Mutex // 串行队列摘取、后台落库和同步账号写入
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

func (w *accountPoolRuntimeWriter) FlushAccount(ctx context.Context, accountID int64) error {
	return w.flushAccountAndRun(ctx, accountID, nil)
}

// runAccountStateWrite 将同步落库和运行态发布放在同一状态变更临界区。
// 所有请求结算与手动操作都先取得 accountStateMu，后台写回只取得 flushMu，锁序无环。
func (s *AccountPoolService) runAccountStateWrite(ctx context.Context, accountID int64, apply func() error) error {
	if accountID <= 0 {
		return fmt.Errorf("账号不存在: %d", accountID)
	}
	s.accountStateMu.Lock()
	defer s.accountStateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.runtimeWriter != nil {
		return s.runtimeWriter.flushAccountAndRun(ctx, accountID, apply)
	}
	return apply()
}

// publishAccountRuntime 在队列停止接收前，原子完成缓存变更与快照入队。
// 调用者持有 accountStateMu；被拒绝的更新不会先改变缓存再静默丢失。
func (s *AccountPoolService) publishAccountRuntime(accountID int64, kind accountRuntimeMutationKind, mutate func() (uint64, error)) (bool, error) {
	if s.runtimeWriter == nil {
		return false, fmt.Errorf("账号运行态写回未就绪")
	}
	return s.runtimeWriter.publish(func() (accountRuntimeMutation, error) {
		version, err := mutate()
		if err != nil || version == 0 {
			return accountRuntimeMutation{}, err
		}
		state, ok := s.runtimeCache.snapshotState(accountID, version)
		if !ok {
			return accountRuntimeMutation{}, fmt.Errorf("账号 %d 运行态快照版本发生变化", accountID)
		}
		return accountRuntimeMutation{accountID: accountID, kind: kind, stateVersion: version, state: state}, nil
	})
}

func (w *accountPoolRuntimeWriter) checkOpen() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing || w.closed {
		return errAccountRuntimeWriterClosed
	}
	return nil
}

// flushAccountAndRun 先写回该账号已有的变化，再执行同步操作。
// 与后台刷新共用临界区，避免摘取后的旧写入越过同步操作；失败的事件保留重试。
func (w *accountPoolRuntimeWriter) flushAccountAndRun(ctx context.Context, accountID int64, apply func() error) error {
	if w == nil || accountID <= 0 {
		return nil
	}
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	if err := w.checkOpen(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := w.flushAccountLocked(ctx, accountID); err != nil {
		return err
	}
	if apply != nil {
		return apply()
	}
	return nil
}

// flushAccountLocked 供已持有 flushMu 的状态写入复用，避免启停推进版本后遗漏待写回计数。
func (w *accountPoolRuntimeWriter) flushAccountLocked(ctx context.Context, accountID int64) error {
	w.mu.Lock()
	mutation, ok := w.pending[accountID]
	if ok {
		delete(w.pending, accountID)
	}
	w.mu.Unlock()
	if ok {
		if err := w.applyMutation(ctx, &mutation); err != nil {
			return errors.Join(err, w.enqueue(mutation))
		}
	}
	return nil
}

func (w *accountPoolRuntimeWriter) Close() error {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	if w.closeDone != nil {
		done := w.closeDone
		w.mu.Unlock()
		<-done
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.closeErr
	}
	w.closing = true
	w.closeDone = make(chan struct{})
	w.mu.Unlock()

	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
	err := w.flushPending(false)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeErr = err
	w.closed = true
	close(w.closeDone)
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

func (w *accountPoolRuntimeWriter) publish(build func() (accountRuntimeMutation, error)) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.closing {
		return false, errAccountRuntimeWriterClosed
	}
	next, err := build()
	if err != nil || next.stateVersion == 0 {
		return false, err
	}
	w.enqueueLocked(next)
	return true, nil
}

// enqueue 只重排已经受理过的快照。关闭期间的重试由 Close 的最终刷新处理。
func (w *accountPoolRuntimeWriter) enqueue(next accountRuntimeMutation) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errAccountRuntimeWriterClosed
	}
	w.enqueueLocked(next)
	return nil
}

func (w *accountPoolRuntimeWriter) enqueueLocked(next accountRuntimeMutation) {
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
			return
		}
	}
}

func (w *accountPoolRuntimeWriter) flushPending(requeueOnFailure bool) error {
	if w == nil {
		return nil
	}
	w.flushMu.Lock()
	defer w.flushMu.Unlock()

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
				if enqueueErr := w.enqueue(mutation); enqueueErr != nil {
					firstErr = errors.Join(firstErr, enqueueErr)
				}
			}
		}
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

	return w.store.SaveAccountRuntimeState(ctx, mutation.accountID, mutation.state)
}

func mergeAccountRuntimeMutation(current, next accountRuntimeMutation) accountRuntimeMutation {
	if current.accountID == 0 {
		return next
	}
	// 最新快照已经包含成功、清零及再次失败的全部效果，不能再次累加旧事件。
	if next.stateVersion <= current.stateVersion {
		return current
	}
	return next
}
