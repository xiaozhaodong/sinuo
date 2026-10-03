package monitor

import (
	"sync"
	"testing"
	"time"
)

func TestActiveConnections_NotifyAddFinishAndTimeout(t *testing.T) {
	m := NewMetrics()
	changes, cancel := m.SubscribeActiveConnections()
	defer cancel()
	<-changes
	id := m.RecordRequest("test", "", "", "POST", "/v1/messages")
	<-changes
	if m.ActiveConnectionCount() != 1 {
		t.Fatal("新增请求计数错误")
	}
	m.RecordResponse(id, 200, time.Millisecond, 0, "test")
	<-changes
	if m.ActiveConnectionCount() != 0 {
		t.Fatal("完成请求计数错误")
	}
	id = m.RecordRequest("test", "", "", "POST", "/v1/responses")
	<-changes
	m.RecordRequestSuspended(id)
	m.RecordRequestSuspendTimeout(id)
	<-changes
	if m.ActiveConnectionCount() != 0 {
		t.Fatal("挂起超时应通知归零")
	}
}

func TestActiveConnections_CoalesceAndConcurrentUnsubscribe(t *testing.T) {
	m := NewMetrics()
	changes, cancel := m.SubscribeActiveConnections()
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				id := m.RecordRequest("test", "", "", "POST", "/v1/responses")
				m.RecordResponse(id, 200, time.Millisecond, 0, "test")
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("不读取通知不能阻塞请求")
	}
	if m.ActiveConnectionCount() != 0 {
		t.Fatal("合并通知后最新计数应为零")
	}
	<-changes
	var cancellations sync.WaitGroup
	for range 20 {
		cancellations.Add(1)
		go func() { defer cancellations.Done(); ch, stop := m.SubscribeActiveConnections(); <-ch; stop(); stop() }()
	}
	cancel()
	cancel()
	cancellations.Wait()
	if _, ok := <-changes; ok {
		t.Fatal("取消后必须关闭通道")
	}
}
