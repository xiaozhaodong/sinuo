package events

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestRequestUpdateWhitelistKeepsRetryAnchors(t *testing.T) {
	filter := NewEventBus(nil).(*eventBus).filters[EventRequestUpdated]
	at := "2026-10-30T00:00:00.125000Z"
	data := filter.DataTransformer(Event{Type: EventRequestUpdated, Data: map[string]interface{}{
		"request_id": "synthetic-retry", "change_type": "upstream_retry",
		"retry_count": 1, "retry_started_at": at,
		"credential_raw": "synthetic-must-be-filtered",
	}})
	if data["retry_count"] != 1 || data["retry_started_at"] != at {
		t.Fatalf("L1重试锚点必须透传: %+v", data)
	}
	if _, ok := data["credential_raw"]; ok {
		t.Fatal("禁止放宽其他字段白名单")
	}
}

type capturedFrontendEvent struct {
	eventType string
	data      map[string]interface{}
}

type captureBroadcaster struct {
	events chan capturedFrontendEvent
}

func (b *captureBroadcaster) BroadcastEvent(eventType string, data map[string]interface{}) {
	b.events <- capturedFrontendEvent{eventType: eventType, data: data}
}

func (b *captureBroadcaster) IsEventManagerActive() bool {
	return true
}

func TestEventBus_RequestChangesAreSafeAndNotRateLimited(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := NewEventBus(logger)
	broadcaster := &captureBroadcaster{events: make(chan capturedFrontendEvent, 2)}
	bus.SetSSEBroadcaster(broadcaster)
	if err := bus.Start(); err != nil {
		t.Fatalf("start event bus: %v", err)
	}
	defer bus.Stop()

	bus.Publish(Event{
		Type:     EventRequestStarted,
		Source:   "test",
		Priority: PriorityNormal,
		Data: map[string]interface{}{
			"request_id":  "req-1",
			"change_type": "request_started",
			"client_ip":   "127.0.0.1",
			"user_agent":  "secret-agent",
			"path":        "/v1/responses",
		},
	})
	bus.Publish(Event{
		Type:     EventRequestUpdated,
		Source:   "test",
		Priority: PriorityNormal,
		Data: map[string]interface{}{
			"request_id":    "req-1",
			"change_type":   "request_completed",
			"status":        "completed",
			"completion_ms": int64(1234),
		},
	})

	for i := 0; i < 2; i++ {
		select {
		case event := <-broadcaster.events:
			if event.eventType != "request" {
				t.Fatalf("unexpected frontend event type: %s", event.eventType)
			}
			if event.data["request_id"] != "req-1" {
				t.Fatalf("unexpected request id: %#v", event.data["request_id"])
			}
			for _, sensitiveKey := range []string{"client_ip", "user_agent", "path"} {
				if _, exists := event.data[sensitiveKey]; exists {
					t.Fatalf("sensitive field %s should not be broadcast", sensitiveKey)
				}
			}
			if _, exists := event.data["occurred_at"]; !exists {
				t.Fatal("request change should include occurred_at")
			}
			if event.data["change_type"] == "request_completed" && event.data["completion_ms"] != int64(1234) {
				t.Fatalf("timing field should be broadcast, got %#v", event.data["completion_ms"])
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for request change event")
		}
	}
}
