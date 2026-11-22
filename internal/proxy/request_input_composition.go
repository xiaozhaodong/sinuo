package proxy

import (
	"encoding/json"
	"log/slog"
	"strings"

	"sinuo/internal/inputcomposition"
	"sinuo/internal/tracking"
)

// 同一逻辑请求只分析一次正文。仅实际模型的窗口变化时重新编码有界摘要。
type requestInputComposition struct {
	summary      *inputcomposition.Summary
	windowFor    func(string) int64
	windowSource string
}

func captureInput(tracker *tracking.UsageTracker, route string, body []byte, model string) requestInputComposition {
	if !tracker.TrackingEnabled() {
		return requestInputComposition{}
	}
	return withInputWindow(tracker, route, model, inputcomposition.Analyze(route, body))
}

func withInputWindow(tracker *tracking.UsageTracker, route, model string, summary *inputcomposition.Summary) requestInputComposition {
	if summary == nil {
		return requestInputComposition{}
	}
	capture := requestInputComposition{summary: summary}
	if route == "/v1/messages" {
		capture.windowSource = "model_spec"
		capture.windowFor = inputcomposition.MessagesModelWindow
	} else {
		capture.windowSource = "codex_catalog"
		capture.windowFor = tracker.InputWindows.Snapshot()
	}
	capture.setModel(model)
	return capture
}

func (c *requestInputComposition) setModel(model string) bool {
	if c.windowFor == nil || c.summary == nil {
		return false
	}
	limit := c.windowFor(model)
	if limit == c.summary.Window && (limit == 0 || model == c.summary.WindowModel) {
		return false
	}
	updated := c.summary.WithWindow(model, limit, c.windowSource)
	c.summary = &updated
	return true
}

func (c *requestInputComposition) setResponseModel(model string) bool {
	if c.windowFor == nil || c.summary == nil {
		return false
	}
	model = strings.TrimSpace(model)
	if model == "" || model == "unknown" || model == "default" {
		return false
	}
	if c.windowFor(model) == 0 && c.summary.Window > 0 && isDateSnapshotModel(model, c.summary.WindowModel) {
		return false
	}
	return c.setModel(model)
}

// isDateSnapshotModel 判断响应模型是否为基准模型的日期快照（如 gpt-5.4-2026-03-05），
// 避免本地目录仅登记基础 slug 时误清空已匹配的窗口上限。
func isDateSnapshotModel(response, base string) bool {
	response = strings.ToLower(strings.TrimSpace(response))
	base = strings.ToLower(strings.TrimSpace(base))
	if response == "" || base == "" {
		return false
	}
	hasDateSuffix := func(prefix string) bool {
		if !strings.HasPrefix(response, prefix) {
			return false
		}
		suffix := response[len(prefix):]
		if len(suffix) == 9 && suffix[0] == '-' {
			return allASCIIUnsignedDigits(suffix[1:])
		}
		return len(suffix) == 11 && suffix[0] == '-' && suffix[5] == '-' && suffix[8] == '-' &&
			allASCIIUnsignedDigits(suffix[1:5]) && allASCIIUnsignedDigits(suffix[6:8]) && allASCIIUnsignedDigits(suffix[9:11])
	}
	if hasDateSuffix(base) {
		return true
	}
	if trimmed, ok := strings.CutSuffix(base, "-latest"); ok && trimmed != "" {
		return hasDateSuffix(trimmed)
	}
	return false
}

func allASCIIUnsignedDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func (c requestInputComposition) record(tracker *tracking.UsageTracker, id string) {
	if c.summary == nil || tracker == nil {
		return
	}
	data, err := json.Marshal(c.summary)
	if err != nil {
		slog.Warn("无法编码请求输入摘要", "request_id", id, "error", err)
		return
	}
	raw := string(data)
	tracker.RecordRequestUpdate(id, tracking.UpdateOptions{InputCompositionJSON: &raw})
}

func (t *subscriptionRequestTracking) setInputComposition(capture requestInputComposition) {
	if t == nil || capture.summary == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active || t.settled {
		return
	}
	t.inputComposition = capture
	if t.responseModel != "" {
		t.inputComposition.setResponseModel(t.responseModel)
	}
	t.inputComposition.record(t.observer.tracker, t.requestID)
}

func (t *subscriptionRequestTracking) observeToolEcho(frame []byte) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active || t.settled || t.inputComposition.summary == nil || t.inputComposition.summary.ToolEcho == "" {
		return
	}
	updated := *t.inputComposition.summary
	if !updated.ObserveToolEcho(frame) {
		return
	}
	t.inputComposition.summary = &updated
	t.inputComposition.record(t.observer.tracker, t.requestID)
}

func (s *subscriptionSubmissionState) trackCreateInput(sub *subscriptionSubmission, body []byte, parent *inputcomposition.History) {
	o, ok := s.observer.(*subscriptionTrackingObserver)
	if !ok || !sub.Generate || !o.tracker.TrackingEnabled() {
		return
	}
	handle := o.submissionHandle(sub, false)
	if handle == nil {
		return
	}
	summary, history := inputcomposition.AnalyzeCreate(body, parent)
	capture := withInputWindow(o.tracker, "/v1/responses", sub.Model, summary)
	// WS 可以通过附加输入帧继续对话，create 以外的输入不能归给本帧的可见条目。
	if capture.summary != nil {
		capture.summary.NoteWebSocketCreate()
	}
	handle.setInputComposition(capture)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sub.settled && sub.terminalRead == nil && sub.inputHistoryReady == nil {
		sub.inputHistory = history
	}
}

func (s *subscriptionSubmissionState) lookupParentHistoryLocked(previousResponseID string) (*inputcomposition.History, *subscriptionSubmission, <-chan struct{}) {
	if previousResponseID == "" {
		return nil, nil, nil
	}
	if active := s.byResponseID[previousResponseID]; active != nil {
		if active.inputHistoryReady != nil {
			return nil, active, active.inputHistoryReady
		}
		if active.terminalRead != nil {
			return active.inputHistory, nil, nil
		}
	}
	return s.recentIDs[previousResponseID].history, nil, nil
}

func (s *subscriptionSubmissionState) observeToolEcho(ev subscriptionResponseEvent, frame []byte) {
	if ev.typ != "response.created" && ev.typ != "response.completed" && ev.typ != "response.done" {
		return
	}
	if ev.submission == nil || (ev.typ == "response.created" && ev.responseID == "") {
		return
	}
	o, ok := s.observer.(*subscriptionTrackingObserver)
	if !ok {
		return
	}
	if handle := o.submissionHandle(ev.submission, false); handle != nil {
		handle.observeToolEcho(frame)
	}
}

func (s *subscriptionSubmissionState) noteCompletedOutput(sub *subscriptionSubmission, payload []byte) {
	s.mu.Lock()
	input := sub.inputHistory
	s.mu.Unlock()
	idx, item := input.OutputItem(payload)
	if item == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub.settled || sub.terminalRead != nil || sub.inputHistoryReady != nil {
		return
	}
	if sub.outputItems == nil {
		sub.outputItems = make(map[int]*inputcomposition.History)
	}
	if _, seen := sub.outputItems[idx]; !seen {
		sub.outputItems[idx] = item
	}
}

func (s *subscriptionSubmissionState) completeInputHistory(ev subscriptionResponseEvent, payload []byte) {
	sub := ev.submission
	s.mu.Lock()
	if sub.terminalRead != nil || sub.inputHistoryReady != nil {
		s.mu.Unlock()
		return
	}
	input, done := sub.inputHistory, sub.outputItems
	sub.outputItems = nil
	compute := input != nil && (ev.typ == "response.completed" || ev.typ == "response.done") &&
		ev.result.ErrorInfo == nil && !ev.result.IsCancelled
	if !compute {
		sub.terminalRead = ev.result
		sub.inputHistory = nil
		s.mu.Unlock()
		return
	}
	// 释放锁前先登记快照正在生成，让引用同一父响应的流水线子请求在锁外等待完成。
	ready := make(chan struct{})
	sub.inputHistoryReady = ready
	s.mu.Unlock()

	var completed *inputcomposition.History
	defer func() {
		s.mu.Lock()
		sub.terminalRead = ev.result
		sub.inputHistory = completed
		sub.outputItems = nil
		if sub.inputHistoryReady == ready {
			sub.inputHistoryReady = nil
		}
		close(ready)
		s.mu.Unlock()
	}()
	limit := int64(-1)
	if ev.result.HasTokenUsage && ev.result.TokenUsage != nil {
		limit = ev.result.TokenUsage.InputTokens
	}
	completed = input.Completed(payload, limit, done)
}
