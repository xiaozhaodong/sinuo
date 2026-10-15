package middleware

import (
	"sync"
	"testing"
	"time"

	"sinuo/internal/events"
	"sinuo/internal/monitor"
)

type tokenStatsEventRecorder struct {
	events.EventBus
	published []events.Event
}

func (r *tokenStatsEventRecorder) Publish(event events.Event) {
	r.published = append(r.published, event)
}

func TestMonitoringMiddleware_CacheWriteBroadcastUsesDeduplicatedTotal(t *testing.T) {
	middleware := NewMonitoringMiddleware(nil)
	recorder := &tokenStatsEventRecorder{}
	middleware.SetEventBus(recorder)

	codex := &monitor.TokenUsage{InputTokens: 272077, OutputTokens: 1671, CacheCreationTokens: 2000, CacheReadTokens: 265159}
	codex.SetProtocol("codex", "/v1/responses")
	middleware.metrics.RecordTokenUsage("codex", "unknown", codex)
	claude := &monitor.TokenUsage{InputTokens: 100, OutputTokens: 50, CacheCreationTokens: 10, CacheReadTokens: 20}
	claude.SetProtocol("claude", "/v1/messages")
	middleware.metrics.RecordTokenUsage("claude", "unknown", claude)
	middleware.broadcastRealConnectionStats("claude", "unknown")

	if len(recorder.published) != 1 || recorder.published[0].Type != events.EventConnectionStatsUpdated {
		t.Fatalf("连接统计事件不正确：%+v", recorder.published)
	}
	if got := recorder.published[0].Data["total_tokens"]; got != int64(273748+180) {
		t.Fatalf("广播总量重复累计缓存：%v", got)
	}
}

func TestMonitoringMiddleware_ShouldBroadcastConcurrentAccess(t *testing.T) {
	middleware := NewMonitoringMiddleware(nil)

	const goroutines = 16
	const iterations = 2000

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				middleware.shouldBroadcast("connection_stats", time.Microsecond)
			}
		}()
	}

	close(start)
	wg.Wait()
}
