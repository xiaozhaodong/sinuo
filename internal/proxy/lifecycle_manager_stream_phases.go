package proxy

import (
	"net/http"
	"time"

	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

// SetUpstreamResponse 每次尝试覆盖，包括无响应或无ID，避免遗留前一次的ID。
func (rlm *RequestLifecycleManager) SetUpstreamResponse(resp *http.Response) {
	id := ""
	if resp != nil {
		id = handlers.UpstreamRequestID(resp.Header)
	}
	rlm.timingMu.Lock()
	defer rlm.timingMu.Unlock()
	if !rlm.upstreamSettled && rlm.firstTokenAt.IsZero() {
		rlm.phaseRequestID = &id
		rlm.phaseStopReason = nil
	}
}

// ObserveStreamResult 与连接追踪共用计时锁，但首响后仍继续收集内容。
func (rlm *RequestLifecycleManager) ObserveStreamResult(result *ParseResult, readBytes int, at time.Time) {
	rlm.timingMu.Lock()
	if rlm.upstreamSettled || rlm.phasesRecorded {
		rlm.timingMu.Unlock()
		return
	}
	if rlm.phaseCollector == nil {
		rlm.phaseCollector = newStreamPhaseCollector(rlm.startTime)
	}
	firstAt := rlm.phaseCollector.Observe(result, readBytes, at)
	if firstAt == nil {
		rlm.timingMu.Unlock()
		return
	}
	ms := upstreamOffset(rlm.startTime, *firstAt)
	if rlm.usageTracker != nil {
		rlm.usageTracker.RecordRequestUpdate(rlm.requestID, tracking.UpdateOptions{
			StreamPhases: tracking.StreamPhases{FirstContentMs: ms}, FirstContentAt: firstAt,
		})
	}
	rlm.timingMu.Unlock()
	rlm.publishTimingEvent(firstContentEventData(rlm.requestID, *firstAt))
}

func (rlm *RequestLifecycleManager) streamPhasesLocked() tracking.StreamPhases {
	phases := tracking.StreamPhases{StopReason: rlm.phaseStopReason, UpstreamRequestID: rlm.phaseRequestID}
	if rlm.phaseCollector != nil {
		phases = rlm.phaseCollector.Snapshot(0)
		phases.UpstreamRequestID = rlm.phaseRequestID
	}
	return phases
}

func (rlm *RequestLifecycleManager) flushStreamPhasesLocked() {
	if rlm.phasesRecorded || rlm.usageTracker == nil || rlm.requestID == "" {
		return
	}
	if rlm.phaseCollector != nil || rlm.phaseRequestID != nil || rlm.phaseStopReason != nil {
		rlm.usageTracker.RecordRequestUpdate(rlm.requestID, tracking.UpdateOptions{StreamPhases: rlm.streamPhasesLocked()})
	}
	rlm.phasesRecorded = true
}

func (rlm *RequestLifecycleManager) recordRegularStopReason(reason string) {
	if reason != "" {
		rlm.timingMu.Lock()
		if !rlm.upstreamSettled {
			rlm.phaseStopReason = &reason
		}
		rlm.timingMu.Unlock()
	}
}
