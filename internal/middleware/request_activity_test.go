package middleware

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoggingActivity_WaitsForHandlerAndRestoresAdmission(t *testing.T) {
	monitoring := NewMonitoringMiddleware(nil)
	activity := monitoring.GetMetrics().RequestActivity()
	logging := NewLoggingMiddleware(slog.New(slog.NewTextHandler(io.Discard, nil)))
	logging.SetMonitoringMiddleware(monitoring)
	var ids atomic.Uint32
	logging.SetRequestIDGenerator(func(context.Context) (string, error) { return fmt.Sprintf("req-%08x", ids.Add(1)), nil })
	started, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	handler := logging.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-finish
		_, _ = w.Write([]byte("tail"))
	}))
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil))
	}()
	<-started
	if activity.BeginDrain("quit") != 1 {
		t.Fatal("处理中请求未被计入")
	}
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest("POST", "/v1/messages", nil))
	if rejected.Code != http.StatusServiceUnavailable || activity.Count() != 1 {
		t.Fatal("未阻止新请求或干扰了在途请求")
	}
	close(finish)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("原请求未完成")
	}
	if activity.Count() != 0 {
		t.Fatal("HTTP 返回后没有释放请求")
	}
	activity.CancelDrain("quit")
	resumed := logging.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	w := httptest.NewRecorder()
	resumed.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil))
	if w.Code != http.StatusNoContent || activity.Count() != 0 {
		t.Fatal("取消等待后未恢复接收")
	}
}
