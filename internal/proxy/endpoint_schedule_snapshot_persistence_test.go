package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cc-forwarder/internal/endpoint"
)

func TestMarshalEndpointScheduleSnapshot_WhitelistsRoutingFacts(t *testing.T) {
	now := time.Now()
	payload := marshalEndpointScheduleSnapshot(&endpoint.EndpointScheduleSnapshot{
		RequestID:           "req-endpoint-persist",
		CapturedAt:          now,
		UpdatedAt:           now,
		RequestPath:         "/v1/messages",
		SelectedEndpoint:    "member",
		RouteMode:           endpoint.RouteModeAuto,
		RouteScopeKind:      endpoint.RouteScopeUpstreamGroup,
		RouteScope:          endpoint.RouteScopeUpstreamGroup,
		RouteScopeGroupID:   7,
		RouteScopeGroupName: "claude-group",
		UpstreamGroup:       &endpoint.UpstreamGroupSnapshot{ID: 7, Name: "claude-group", Members: []string{"member"}},
		RequestModel:        "claude-test",
		ModelRoutingVersion: 3,
		FinalOutcome:        endpoint.EndpointScheduleOutcomeSuccess,
		Summary:             "成功命中端点 member",
		FinalError:          "upstream leaked secret",
		Decisions: []endpoint.EndpointScheduleDecision{{
			Name:           "member",
			Decision:       "candidate",
			Reason:         "healthy",
			RuntimeOutcome: endpoint.EndpointScheduleOutcomeSuccess,
			RuntimeError:   "token=secret",
		}},
	})

	if payload == "" {
		t.Fatal("expected endpoint schedule snapshot payload")
	}
	if strings.Contains(payload, "upstream leaked secret") || strings.Contains(payload, "token=secret") {
		t.Fatalf("persisted endpoint snapshot leaked runtime error text: %s", payload)
	}

	var persisted map[string]any
	if err := json.Unmarshal([]byte(payload), &persisted); err != nil {
		t.Fatalf("unmarshal endpoint snapshot: %v", err)
	}
	if persisted["route_scope_group_id"] != float64(7) || persisted["route_scope_group_name"] != "claude-group" {
		t.Fatalf("expected group identity in persisted snapshot, got %+v", persisted)
	}
	if persisted["request_model"] != "claude-test" || persisted["final_outcome"] != endpoint.EndpointScheduleOutcomeSuccess {
		t.Fatalf("expected model and outcome in persisted snapshot, got %+v", persisted)
	}
	if _, ok := persisted["final_error"]; ok {
		t.Fatalf("final_error must not be persisted: %+v", persisted)
	}
	decisions, ok := persisted["decisions"].([]any)
	if !ok || len(decisions) != 1 {
		t.Fatalf("expected one persisted decision, got %+v", persisted["decisions"])
	}
	if _, ok := decisions[0].(map[string]any)["runtime_error"]; ok {
		t.Fatalf("runtime_error must not be persisted: %+v", decisions[0])
	}
}

func TestCompleteEndpointSchedule_PersistsFinalSnapshotAfterArchive(t *testing.T) {
	handler, manager := newEndpointPipelineTestHandler(t)
	tracker := newLifecyclePanelTestTracker(t)
	handler.SetUsageTracker(tracker)

	const requestID = "req-endpoint-persist"
	scope := endpoint.RouteScope{
		Kind:            endpoint.RouteScopeUpstreamGroup,
		GroupID:         7,
		GroupName:       "claude-group",
		Members:         []string{"member"},
		Mode:            endpoint.RouteModeAuto,
		Revision:        3,
		SnapshotVersion: 9,
		Model:           "claude-test",
	}
	manager.BeginEndpointScheduleSnapshot(requestID, "/v1/messages", &endpoint.EndpointScheduleSnapshot{
		RequestModel:        scope.Model,
		RouteScopeKind:      scope.Kind,
		RouteScope:          scope.Kind,
		RouteScopeGroupID:   scope.GroupID,
		RouteScopeGroupName: scope.GroupName,
		UpstreamGroup:       &endpoint.UpstreamGroupSnapshot{ID: scope.GroupID, Name: scope.GroupName, Members: scope.Members},
		Decisions:           []endpoint.EndpointScheduleDecision{{Name: "member", Decision: "candidate", Reason: "healthy"}},
	})

	lifecycle := NewRequestLifecycleManager(tracker, nil, requestID, nil)
	lifecycle.StartRequest("127.0.0.1", "test-agent", "POST", "/v1/messages", false)
	lifecycle.CompleteRequest(nil)
	handler.completeEndpointSchedule(requestID, scope, "member", endpoint.EndpointScheduleOutcomeSuccess, "")

	data, err := tracker.GetRequestLifecycleData(context.Background(), requestID)
	if err != nil {
		t.Fatalf("GetRequestLifecycleData failed: %v", err)
	}
	if data == nil || data.ScheduleSnapshotJSON == "" {
		t.Fatalf("expected endpoint schedule snapshot in request history, got %+v", data)
	}
	var persisted map[string]any
	if err := json.Unmarshal([]byte(data.ScheduleSnapshotJSON), &persisted); err != nil {
		t.Fatalf("unmarshal persisted endpoint snapshot: %v", err)
	}
	if persisted["route_scope_group_id"] != float64(7) || persisted["selected_endpoint"] != "member" {
		t.Fatalf("expected final endpoint/group identity, got %+v", persisted)
	}

	// 归档批次提交后，历史详情应从数据库继续读到同一份终态快照。
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err = tracker.GetRequestLifecycleData(context.Background(), requestID)
		if err != nil {
			t.Fatalf("GetRequestLifecycleData after archive failed: %v", err)
		}
		if data != nil && data.Source == "database" {
			var persistedFromDB map[string]any
			if err := json.Unmarshal([]byte(data.ScheduleSnapshotJSON), &persistedFromDB); err != nil {
				t.Fatalf("unmarshal database endpoint snapshot: %v", err)
			}
			if persistedFromDB["route_scope_group_name"] != "claude-group" {
				t.Fatalf("database snapshot lost group identity: %+v", persistedFromDB)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for endpoint snapshot to reach database, data=%+v", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
