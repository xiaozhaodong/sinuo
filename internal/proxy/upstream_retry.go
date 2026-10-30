package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

// upstreamContactState 由所属生命周期的计时锁保护；联系次数是重试数的唯一来源。
type upstreamContactState struct {
	upstreamContacts  int
	firstDispatchAt   time.Time
	persistedContacts int
	upstreamSettled   bool
}

func (s *upstreamContactState) recordContact(at time.Time) {
	s.upstreamContacts++
	if s.firstDispatchAt.IsZero() {
		s.firstDispatchAt = at
	}
}

func (s *upstreamContactState) contactUpdate(start time.Time) tracking.UpdateOptions {
	retries := max(s.upstreamContacts-1, 0)
	return tracking.UpdateOptions{
		UpstreamFirstDispatchMs: upstreamOffset(start, s.firstDispatchAt),
		RetryCount:              &retries,
	}
}

func upstreamOffset(start, at time.Time) *int64 {
	if at.IsZero() {
		return nil
	}
	ms := max(at.Sub(start).Milliseconds(), 0)
	return &ms
}

func logTransportRetry(requestID, upstream string, contacts int, attempt handlers.UpstreamConnAttempt) {
	if !attempt.TransportRetry {
		return
	}
	prev := attempt.Previous
	level := slog.LevelInfo
	if prev.Elapsed >= time.Second {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level,
		fmt.Sprintf("⚠️ [传输层重发] [%s] 上游中断上一次尝试，客户端已自动重发", requestID),
		"upstream", upstream, "attempt", contacts, "prev_elapsed", prev.Elapsed,
		"prev_got_conn", prev.GotConn, "prev_reused", prev.Reused,
		"prev_was_idle", prev.WasIdle, "prev_idle", prev.IdleTime,
		"prev_wrote_headers", prev.WroteHeaders,
		"prev_wrote_request", prev.WroteRequest && prev.WriteError == nil,
		"prev_write_error", prev.WriteError)
}
