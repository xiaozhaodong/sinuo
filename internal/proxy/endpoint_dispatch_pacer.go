package proxy

import (
	"context"
	"time"
)

const (
	// 临时能力：Venlacy 明确限制每分钟最多 7 个请求，按 9 秒间隔保守平滑出站。
	// 后续若出现第二个同类端点，再升级为持久化的端点级配置。
	temporaryVenlacyEndpointName        = "venlacy"
	temporaryVenlacyMinDispatchInterval = 9 * time.Second
)

// endpointDispatchPacer 串行放行实际出站 attempt。等待期间不持有 endpoint admission，
// 客户端取消也能立即退出，不会拖住端点停用或删除。
type endpointDispatchPacer struct {
	minInterval  time.Duration
	turn         chan struct{}
	lastDispatch time.Time
}

func newEndpointDispatchPacer(minInterval time.Duration) *endpointDispatchPacer {
	p := &endpointDispatchPacer{
		minInterval: minInterval,
		turn:        make(chan struct{}, 1),
	}
	p.turn <- struct{}{}
	return p
}

func (p *endpointDispatchPacer) Wait(ctx context.Context) (time.Duration, error) {
	if p == nil || p.minInterval <= 0 {
		return 0, nil
	}

	startedAt := time.Now()
	select {
	case <-ctx.Done():
		return time.Since(startedAt), ctx.Err()
	case <-p.turn:
	}
	defer func() { p.turn <- struct{}{} }()

	delay := time.Until(p.lastDispatch.Add(p.minInterval))
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return time.Since(startedAt), ctx.Err()
		case <-timer.C:
		}
	}

	p.lastDispatch = time.Now()
	return time.Since(startedAt), nil
}

func isTemporaryPacedEndpoint(endpointName string) bool {
	return endpointName == temporaryVenlacyEndpointName
}

func (h *Handler) waitForEndpointDispatch(ctx context.Context, endpointName string) (time.Duration, error) {
	if h == nil || !isTemporaryPacedEndpoint(endpointName) {
		return 0, nil
	}
	return h.endpointDispatchPacer.Wait(ctx)
}
