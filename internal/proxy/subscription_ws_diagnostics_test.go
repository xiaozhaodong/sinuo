package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"cc-forwarder/internal/events"
	"cc-forwarder/internal/proxy/subscriptionws"
	"cc-forwarder/internal/tracking"

	"github.com/coder/websocket"
)

type subscriptionPresentationEventBus struct {
	events.EventBus
	published []events.Event
}

func (b *subscriptionPresentationEventBus) Publish(event events.Event) {
	b.published = append(b.published, event)
}

func TestSubscriptionWSClientInterruptionPresentation(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	for _, tc := range []struct {
		name          string
		direction     subscriptionws.Direction
		phase, kind   string
		parent, other string
		stopping      bool
		interrupted   bool
	}{
		{"client_eof", subscriptionws.DirectionDownstreamToUpstream, "read", "eof", "none", "context_canceled", false, true},
		{"client_normal_close", subscriptionws.DirectionDownstreamToUpstream, "read", "normal_close", "none", "connection_closed", false, true},
		{"upstream_eof", subscriptionws.DirectionUpstreamToDownstream, "read", "eof", "none", "", false, false},
		{"upstream_normal_close_without_terminal", subscriptionws.DirectionUpstreamToDownstream, "read", "normal_close", "none", "", false, false},
		{"write_eof", subscriptionws.DirectionUpstreamToDownstream, "write", "eof", "none", "", false, false},
		{"truncated_frame", subscriptionws.DirectionDownstreamToUpstream, "read", "unexpected_eof", "none", "", false, false},
		{"client_reset", subscriptionws.DirectionDownstreamToUpstream, "read", "connection_reset", "none", "", false, false},
		{"abnormal_close", subscriptionws.DirectionDownstreamToUpstream, "read", "peer_close", "none", "", false, false},
		{"parent_cancel", subscriptionws.DirectionDownstreamToUpstream, "read", "eof", "context_canceled", "", false, false},
		{"proxy_stop", subscriptionws.DirectionDownstreamToUpstream, "read", "eof", "none", "", true, false},
		{"write_timeout", subscriptionws.DirectionDownstreamToUpstream, "read", "eof", "none", "deadline_exceeded", false, false},
		{"compression_error", subscriptionws.DirectionDownstreamToUpstream, "read", "eof", "none", "invalid_compressed_data", false, false},
		{"protocol_error", subscriptionws.DirectionDownstreamToUpstream, "read", "eof", "none", "websocket_protocol_error", false, false},
		{"message_limit", subscriptionws.DirectionDownstreamToUpstream, "read", "eof", "none", "message_too_big", false, false},
		{"unknown_error", subscriptionws.DirectionDownstreamToUpstream, "read", "eof", "none", "other", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stats := subscriptionws.BridgeStats{
				ParentContextError:   tc.parent,
				FirstObserved:        subscriptionws.IOEnd{Direction: tc.direction, Phase: tc.phase, Error: subscriptionws.ErrorDiagnostic{Kind: tc.kind}},
				UpstreamToDownstream: subscriptionws.LaneStats{WriteEnd: subscriptionws.IOEnd{Error: subscriptionws.ErrorDiagnostic{Kind: tc.other}}},
			}
			bus := &subscriptionPresentationEventBus{}
			observer := newSubscriptionTrackingObserver(tracker, bus)
			state := newSubscriptionSubmissionState(context.Background(), "conn-"+tc.name, observer)
			submission, _ := state.beginCreate([]byte(`{"type":"response.create","model":"synthetic-model"}`))
			state.close(subscriptionWSInterruptionReason(stats, tc.stopping))
			row := waitSubscriptionDetail(t, tracker, submission.RequestID)
			wantStatus, wantReason, wantChange := "failed", "subscription_interrupted", "request_failed"
			wantPriority := events.PriorityHigh
			if tc.interrupted {
				wantStatus, wantReason, wantChange = "cancelled", tracking.SubscriptionClientDisconnected, "request_cancelled"
				wantPriority = events.PriorityNormal
			}
			if row.Status != wantStatus || row.FailureReason != wantReason {
				t.Fatalf("结算分类不符: status=%s reason=%s", row.Status, row.FailureReason)
			}
			if row.UsageKnown || row.CostKnown || row.HTTPStatusCode != nil {
				t.Fatalf("中断不得伪造用量、成本或 HTTP 成功状态: %+v", row)
			}
			last := bus.published[len(bus.published)-1]
			if last.Type != events.EventRequestCompleted || last.Priority != wantPriority || last.Data["change_type"] != wantChange {
				t.Fatalf("实时展示的终态与分类不一致: %+v", last)
			}
		})
	}
}

func TestSubscriptionWSClientClosePreservesTerminalResult(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	for _, terminal := range []string{"completed", "failed", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			observer := newSubscriptionTrackingObserver(tracker, nil)
			state := newSubscriptionSubmissionState(context.Background(), "terminal-"+terminal, observer)
			submission, _ := state.beginCreate([]byte(`{"type":"response.create","model":"synthetic-model"}`))
			state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"resp-synthetic"}}`))
			state.handleUpstream([]byte(fmt.Sprintf(`{"type":"response.%s","response":{"id":"resp-synthetic","usage":{"input_tokens":2,"output_tokens":1}}}`, terminal)))
			state.close(tracking.SubscriptionClientDisconnected)
			row := waitSubscriptionDetail(t, tracker, submission.RequestID)
			if row.Status != terminal || row.FailureReason == tracking.SubscriptionClientDisconnected || !row.UsageKnown {
				t.Fatalf("连接关闭覆盖了已有终态或用量: %+v", row)
			}
		})
	}
}

// 追踪器会异步写日志，测试读取时也要持锁。
type subscriptionDiagnosticBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *subscriptionDiagnosticBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(data)
}

func (b *subscriptionDiagnosticBuffer) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestSubscriptionWSFailureLogsCorrelateRequestAndConnection(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	var output subscriptionDiagnosticBuffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	observer := newSubscriptionTrackingObserver(tracker, nil)
	state := newSubscriptionSubmissionState(context.Background(), "connection-test", observer)
	submission, ok := state.beginCreate([]byte(`{"type":"response.create","model":"synthetic-model"}`))
	if !ok || submission == nil {
		t.Fatal("未建立逻辑请求")
	}
	state.close("connection_closed")
	detail := waitSubscriptionDetail(t, tracker, submission.RequestID)
	if detail.Status != "failed" || detail.FailureReason != "subscription_interrupted" {
		t.Fatalf("诊断改动不应改变结算状态: %+v", detail)
	}
	end := subscriptionws.IOEnd{
		Direction: subscriptionws.DirectionUpstreamToDownstream, Phase: "read", At: time.Now(),
		Error: subscriptionws.DescribeError(fmt.Errorf("SYNTHETIC-CREDENTIAL: %w", io.ErrUnexpectedEOF)),
	}
	proxy := &subscriptionWSProxy{logger: logger}
	proxy.logBridgeEnd("connection-test", subscriptionws.BridgeStats{
		FirstObserved: end, ParentContextError: "none",
		UpstreamToDownstream: subscriptionws.LaneStats{Messages: 8, Bytes: 123, ReadEnd: end},
	}, time.Second, subscriptionws.Options{}, false)
	logText := output.text()
	if strings.Contains(logText, "SYNTHETIC-CREDENTIAL") {
		t.Fatal("原始错误文本泄漏到日志")
	}
	var settlementFound, bridgeFound bool
	for _, line := range strings.Split(strings.TrimSpace(logText), "\n") {
		var record map[string]interface{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["request_id"] == submission.RequestID && record["failure_reason"] == "subscription_interrupted" {
			settlementFound = record["conn_id"] == "connection-test"
		}
		if first, ok := record["first_observed"].(map[string]interface{}); ok {
			bridgeFound = record["conn_id"] == "connection-test" && record["level"] == "WARN" &&
				first["phase"] == "read" && first["error_kind"] == "unexpected_eof" &&
				record["first_direction"] == "upstream_to_downstream"
		}
	}
	if !settlementFound || !bridgeFound {
		t.Fatalf("默认日志级别下无法从请求关联到断连原因: %s", logText)
	}
}

func TestSubscriptionWSNormalAndShutdownLogsAreInfo(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		var output bytes.Buffer
		proxy := &subscriptionWSProxy{logger: slog.New(slog.NewJSONHandler(&output, nil))}
		diagnostic := subscriptionws.DescribeError(websocket.CloseError{Code: 1000, Reason: "SYNTHETIC-PRIVATE-REASON"})
		if shutdown {
			diagnostic = subscriptionws.DescribeError(io.EOF)
		}
		proxy.logBridgeEnd("conn", subscriptionws.BridgeStats{
			FirstObserved: subscriptionws.IOEnd{Error: diagnostic}, ParentContextError: "none",
		}, time.Second, subscriptionws.Options{}, shutdown)
		for index, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
			var record map[string]interface{}
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatal(err)
			}
			if record["level"] != "INFO" || (index == 0 && record["proxy_stopping"] != shutdown) || strings.Contains(line, "SYNTHETIC") {
				t.Fatalf("正常关闭或代理停用不应误报故障或泄漏原因: %s", line)
			}
		}
	}
}

type subscriptionLogLengthHandler struct {
	slog.Handler
	t     *testing.T
	count int
}

func (h *subscriptionLogLengthHandler) Handle(_ context.Context, record slog.Record) error {
	// 生产 SimpleHandler 在增加时间/PID 前，对这种文本格式执行 500 字节截断。
	line := record.Message
	record.Attrs(func(attr slog.Attr) bool {
		line += fmt.Sprintf(" %s=%v", attr.Key, attr.Value)
		return true
	})
	if len(line) > 500 {
		h.t.Errorf("诊断字段会被生产日志截断，长度 %d: %s", len(line), line)
	}
	h.count++
	return nil
}

func TestSubscriptionWSDiagnosticsFitFileLogLimit(t *testing.T) {
	handler := &subscriptionLogLengthHandler{Handler: slog.NewTextHandler(io.Discard, nil), t: t}
	proxy := &subscriptionWSProxy{logger: slog.New(handler)}
	end := subscriptionws.IOEnd{
		Direction: subscriptionws.DirectionDownstreamToUpstream, Phase: "reply_enqueue", At: time.Now(),
		Error: subscriptionws.DescribeError(fmt.Errorf("received invalid close payload: %w", io.ErrUnexpectedEOF)),
	}
	// 两端都有错误、持续时间和流量均较大，仍须保留完整的各个字段。
	lane := subscriptionws.LaneStats{Messages: 123456, Bytes: 1234567890, ReadEnd: end, WriteEnd: end}
	proxy.logBridgeEnd("req-01234567", subscriptionws.BridgeStats{
		FirstObserved: end, ParentContextError: "deadline_exceeded", DownstreamToUpstream: lane, UpstreamToDownstream: lane,
	}, time.Hour, subscriptionws.Options{ReadLimit: 64 << 20, WriteTimeout: 30 * time.Second, CompressionMode: websocket.CompressionContextTakeover}, false)
	if handler.count != 3 {
		t.Fatalf("应产生一条连接摘要和两条方向摘要，实际 %d", handler.count)
	}
}
