package handlers

import (
	"net/http"
	"testing"
)

func TestStreamPhases_UpstreamRequestIDPriority(t *testing.T) {
	header := http.Header{}
	for i := len(upstreamRequestIDHeaders) - 1; i >= 0; i-- {
		key := upstreamRequestIDHeaders[i]
		header.Set(key, "synthetic-"+key)
		if got := UpstreamRequestID(header); got != "synthetic-"+key {
			t.Fatalf("优先级 %s: %s", key, got)
		}
	}
	header.Set("Request-Id", " \t")
	if got := UpstreamRequestID(header); got != "synthetic-anthropic-request-id" {
		t.Fatal(got)
	}
	if UpstreamRequestID(nil) != "" {
		t.Fatal("没有头时应为空")
	}
}
