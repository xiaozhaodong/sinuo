package monitor

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

var monitorTestID atomic.Uint32

func testMonitorRequestID() string {
	return fmt.Sprintf("req-%08x", monitorTestID.Add(1))
}

func TestMetricsPreservesReservedRequestID(t *testing.T) {
	metrics := NewMetrics()
	const id = "req-57b4970a"
	if got := metrics.RecordRequest(id, "test", "", "", "POST", "/v1/messages"); got != id {
		t.Fatalf("指标层改写了已占号 ID: %q", got)
	}
	if metrics.ActiveConnections[id] == nil {
		t.Fatal("未按已占号 ID 登记活动请求")
	}
}

func TestRequestIDGeneratorRejectsMissingAllocator(t *testing.T) {
	var generator RequestIDGenerator
	if id, err := generator.Next(context.Background()); err == nil || id != "" {
		t.Fatalf("未初始化不能生成备用 ID: %q, %v", id, err)
	}
}
