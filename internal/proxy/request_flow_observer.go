package proxy

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"sinuo/internal/tracking"
)

type downstreamWriteStats struct {
	BytesWritten int64
	WriteErrors  int64
	FlushErrors  int64
}

// downstreamObservedResponseWriter 位于响应还原之后，统计写给客户端的字节、写失败及 Flush 错误。
type downstreamObservedResponseWriter struct {
	w                http.ResponseWriter
	mu               sync.Mutex
	firstWrite       time.Time
	statusCode       int
	settlementStatus string
	settlementReason string
	bytesWritten     int64
	writeErrors      int64
	flushErrors      int64
	deliveryStopped  bool
	streaming        bool
	streamTerminal   bool

	onFirstWrite func(bytesWritten int64, at time.Time)
}

func wrapDownstreamObservedWriter(
	w http.ResponseWriter,
	onFirstWrite func(bytesWritten int64, at time.Time),
) (http.ResponseWriter, *downstreamObservedResponseWriter) {
	if w == nil {
		return nil, nil
	}
	// 订阅入口的 Flush 保留原有返回语义。将观测器放在它的内侧，捕获底层真实错误。
	if entry, ok := w.(*subscriptionResponseWriter); ok {
		observed, dw := wrapDownstreamObservedWriter(entry.ResponseWriter, onFirstWrite)
		entry.ResponseWriter = observed
		return entry, dw
	}
	if existing, ok := w.(*downstreamObservedResponseWriter); ok {
		return existing, existing
	}
	dw := &downstreamObservedResponseWriter{
		w:            w,
		onFirstWrite: onFirstWrite,
	}
	return dw, dw
}

func (d *downstreamObservedResponseWriter) Header() http.Header {
	return d.w.Header()
}

func (d *downstreamObservedResponseWriter) WriteHeader(statusCode int) {
	d.w.WriteHeader(statusCode)
	if statusCode >= 200 {
		d.mu.Lock()
		if d.statusCode == 0 {
			d.statusCode = statusCode
		}
		d.mu.Unlock()
	}
}

func (d *downstreamObservedResponseWriter) Write(p []byte) (int, error) {
	n, err := d.w.Write(p)

	d.mu.Lock()
	if d.statusCode == 0 {
		d.statusCode = http.StatusOK
	}
	if n > 0 {
		d.bytesWritten += int64(n)
	}
	if err != nil || n < len(p) {
		d.writeErrors++
	}
	first := n > 0 && d.firstWrite.IsZero()
	if first {
		d.firstWrite = time.Now()
	}
	firstAt := d.firstWrite
	totalWritten := d.bytesWritten
	firstCb := d.onFirstWrite
	d.mu.Unlock()

	if first && firstCb != nil {
		firstCb(totalWritten, firstAt)
	}
	return n, err
}

func (d *downstreamObservedResponseWriter) Flush() {
	_ = d.FlushError()
}

// FlushError 让 http.NewResponseController(w).Flush() 返回并计入真实下游 Flush 错误。
func (d *downstreamObservedResponseWriter) FlushError() error {
	err := http.NewResponseController(d.w).Flush()
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		d.mu.Lock()
		d.flushErrors++
		d.mu.Unlock()
	}
	return err
}

func (d *downstreamObservedResponseWriter) Unwrap() http.ResponseWriter {
	return d.w
}

func (d *downstreamObservedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := d.w.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (d *downstreamObservedResponseWriter) Stats() downstreamWriteStats {
	if d == nil {
		return downstreamWriteStats{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return downstreamWriteStats{
		BytesWritten: d.bytesWritten,
		WriteErrors:  d.writeErrors,
		FlushErrors:  d.flushErrors,
	}
}

// 结算可能发生在最后一次 Write 之前，HTTP 入口返回时再记录最终交付。
func (d *downstreamObservedResponseWriter) recordSettlement(status, reason string) {
	d.mu.Lock()
	d.settlementStatus, d.settlementReason = status, reason
	d.mu.Unlock()
}

// tail drain 继续收取上游用量时，下游可能尚未发生任何 Write 错误。
func (d *downstreamObservedResponseWriter) markDeliveryStopped() {
	d.mu.Lock()
	d.deliveryStopped = true
	d.mu.Unlock()
}

func (d *downstreamObservedResponseWriter) observeStreamTerminal(terminal bool) {
	d.mu.Lock()
	d.streaming, d.streamTerminal = true, terminal
	d.mu.Unlock()
}

func (d *downstreamObservedResponseWriter) finishDelivery(tracker *tracking.UsageTracker, requestID, copyOutcome string) {
	if d == nil || tracker == nil || requestID == "" {
		return
	}
	d.mu.Lock()
	status, reason := d.settlementStatus, d.settlementReason
	firstAt, total := d.firstWrite, d.bytesWritten
	flushFailed := d.flushErrors > 0
	outcome := tracking.FlowDeliveryDelivered
	switch {
	case d.writeErrors > 0 || flushFailed:
		outcome = tracking.FlowDeliveryPartialOrFailed
	case copyOutcome != "":
		// 透明转发时复制结果已知，即使用量解析没有读到协议终态也可确认交付。
		outcome = copyOutcome
	case reason == "privacy_restore_failed" || reason == "subscription_response_interrupted":
		outcome = tracking.FlowDeliveryPartialOrFailed
	case d.deliveryStopped && total == 0:
		outcome = tracking.FlowDeliveryAborted
	case d.deliveryStopped:
		outcome = tracking.FlowDeliveryPartialOrFailed
	case d.streaming && total > 0 && d.streamTerminal:
		// 完整写出的协议错误帧也完成了下游交付。
		outcome = tracking.FlowDeliveryDelivered
	case d.streaming && total == 0:
		outcome = tracking.FlowDeliveryAborted
	case d.streaming && d.statusCode < 400:
		outcome = tracking.FlowDeliveryPartialOrFailed
	case status == "cancelled" && reason != "subscription_cancelled", status == "failed" && total == 0 && d.statusCode == 0:
		outcome = tracking.FlowDeliveryAborted
	case status == "failed" && d.statusCode < 400 && reason != "subscription_response_failed":
		outcome = tracking.FlowDeliveryPartialOrFailed
	}
	d.mu.Unlock()
	if status == "" {
		return
	}
	now := time.Now()
	var events []tracking.RequestFlowEvent
	if !firstAt.IsZero() {
		events = append(events, tracking.RequestFlowEvent{Kind: tracking.FlowEventDownstreamFirstWrite, At: &firstAt})
	}
	events = append(events, tracking.RequestFlowEvent{Kind: tracking.FlowEventDownstreamDone, At: &now, Bytes: total, DeliveryOutcome: outcome, FlushError: flushFailed})
	tracker.RecordRequestUpdate(requestID, tracking.UpdateOptions{FlowEvents: events})
}

// upstreamObservedReadCloser 包在 resp.Body 最外层，记录原始上游响应体的首字节到达时间。
type upstreamObservedReadCloser struct {
	io.ReadCloser
	firstOnce   sync.Once
	onFirstByte func(time.Time)
}

func wrapUpstreamObservedBody(
	resp *http.Response,
	onFirstByte func(time.Time),
) {
	if resp == nil || resp.Body == nil {
		return
	}
	if _, already := resp.Body.(*upstreamObservedReadCloser); already {
		return
	}
	resp.Body = &upstreamObservedReadCloser{
		ReadCloser:  resp.Body,
		onFirstByte: onFirstByte,
	}
}

func (u *upstreamObservedReadCloser) Read(p []byte) (int, error) {
	n, err := u.ReadCloser.Read(p)
	if n > 0 {
		u.firstOnce.Do(func() {
			if u.onFirstByte != nil {
				u.onFirstByte(time.Now())
			}
		})
	}
	return n, err
}

// Unwrap 暴露底层响应体，便于上层识别隐私还原等外层包装之前的原始包装器。
func (u *upstreamObservedReadCloser) Unwrap() io.ReadCloser {
	if u == nil {
		return nil
	}
	return u.ReadCloser
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
