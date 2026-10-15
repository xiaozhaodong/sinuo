package service

import (
	"context"
	"strings"
	"testing"

	"sinuo/internal/store"

	_ "modernc.org/sqlite"
)

func knownEndpoints(names ...string) EndpointNameChecker {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	return func(name string) bool {
		_, ok := allowed[name]
		return ok
	}
}

func newBindingServiceForTest(t *testing.T, endpoints ...string) *ModelRoutingService {
	t.Helper()
	svc, _ := newModelRoutingServiceForTest(t, knownEndpoints(endpoints...))
	if err := svc.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	return svc
}

func mustCreateBinding(t *testing.T, svc *ModelRoutingService, input ModelRouteBindingInput) int64 {
	t.Helper()
	id, err := svc.CreateBinding(context.Background(), input)
	if err != nil {
		t.Fatalf("create binding %q failed: %v", input.Model, err)
	}
	return id
}

func findBinding(t *testing.T, svc *ModelRoutingService, model string) ModelRouteBinding {
	t.Helper()
	bindings, err := svc.ListBindings(context.Background())
	if err != nil {
		t.Fatalf("list bindings failed: %v", err)
	}
	for _, binding := range bindings {
		if strings.EqualFold(binding.Model, model) {
			return binding
		}
	}
	t.Fatalf("binding for model %q not found in %d bindings", model, len(bindings))
	return ModelRouteBinding{}
}

func TestCreateBinding_CreatesDedicatedGroupAndResolves(t *testing.T) {
	svc := newBindingServiceForTest(t, "relay-a", "relay-b")
	ctx := context.Background()

	mustCreateBinding(t, svc, ModelRouteBindingInput{
		Model: "claude-opus-4-5", Endpoints: []string{"relay-a", "relay-b"},
		RouteMode: "manual_preferred", RouteTarget: "relay-b", Enabled: true, Note: "主力",
	})

	binding := findBinding(t, svc, "claude-opus-4-5")
	if len(binding.Endpoints) != 2 || binding.RouteTarget != "relay-b" || binding.Note != "主力" {
		t.Fatalf("unexpected binding: %#v", binding)
	}
	if len(binding.SharedModels) != 0 {
		t.Fatalf("fresh binding must own its group exclusively, got shared=%v", binding.SharedModels)
	}

	group, err := svc.GetGroup(ctx, binding.GroupID)
	if err != nil || group == nil {
		t.Fatalf("dedicated group must exist: %#v, %v", group, err)
	}
	if group.Name != "claude-opus-4-5" {
		t.Fatalf("group name should default to the model name, got %q", group.Name)
	}
}

func TestCreateBinding_RollsBackGroupWhenAssignmentRejected(t *testing.T) {
	svc := newBindingServiceForTest(t, "relay-a")
	ctx := context.Background()
	mustCreateBinding(t, svc, ModelRouteBindingInput{Model: "claude-opus-4-5", Endpoints: []string{"relay-a"}, RouteMode: "auto", Enabled: true})

	// 大小写不同但归一化后同名：分配唯一索引会拒绝，此时刚建的组必须一并回滚。
	if _, err := svc.CreateBinding(ctx, ModelRouteBindingInput{
		Model: "Claude-Opus-4-5", Endpoints: []string{"relay-a"}, RouteMode: "auto", Enabled: true,
	}); err == nil {
		t.Fatal("duplicated model must be rejected")
	}

	groups, err := svc.ListGroups(ctx)
	if err != nil {
		t.Fatalf("list groups failed: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("failed create must not leave an orphan group, got %d groups", len(groups))
	}
}

func TestCreateBinding_RejectsUnknownEndpoint(t *testing.T) {
	svc := newBindingServiceForTest(t, "relay-a")
	if _, err := svc.CreateBinding(context.Background(), ModelRouteBindingInput{
		Model: "claude-opus-4-5", Endpoints: []string{"relay-ghost"}, RouteMode: "auto", Enabled: true,
	}); err == nil {
		t.Fatal("binding to a non-existent endpoint must be rejected")
	}
	groups, _ := svc.ListGroups(context.Background())
	if len(groups) != 0 {
		t.Fatalf("rejected binding must not persist a group, got %d", len(groups))
	}
}

func TestUpdateBinding_BumpsRevisionAndRenamesGroup(t *testing.T) {
	svc := newBindingServiceForTest(t, "relay-a", "relay-b")
	ctx := context.Background()
	id := mustCreateBinding(t, svc, ModelRouteBindingInput{Model: "claude-opus-4-5", Endpoints: []string{"relay-a"}, RouteMode: "auto", Enabled: true})

	if err := svc.UpdateBinding(ctx, id, ModelRouteBindingInput{
		Model: "claude-opus-4-6", Endpoints: []string{"relay-a", "relay-b"}, RouteMode: "manual_fixed", RouteTarget: "relay-b", Enabled: false,
	}); err != nil {
		t.Fatalf("update binding failed: %v", err)
	}

	binding := findBinding(t, svc, "claude-opus-4-6")
	if len(binding.Endpoints) != 2 || binding.RouteMode != "manual_fixed" || binding.Enabled {
		t.Fatalf("update did not apply: %#v", binding)
	}
	group, err := svc.GetGroup(ctx, binding.GroupID)
	if err != nil || group == nil {
		t.Fatalf("group lookup failed: %#v, %v", group, err)
	}
	// revision 必须 bump，否则旧 retained 的 scope key 不变，会继续影响候选排序。
	if group.RouteRevision != 1 {
		t.Fatalf("member change must bump route revision, got %d", group.RouteRevision)
	}
	if group.Name != "claude-opus-4-6" {
		t.Fatalf("group name should follow the model rename, got %q", group.Name)
	}
}

func TestUpdateBinding_SplitsLegacySharedGroup(t *testing.T) {
	svc := newBindingServiceForTest(t, "relay-a", "relay-b")
	ctx := context.Background()

	// 模拟旧版本手工编排出的共享组：两条分配指向同一个组。
	group, err := svc.CreateGroup(ctx, &store.ModelUpstreamGroupRecord{Name: "shared", MembersJSON: `["relay-a"]`, RouteMode: "auto"})
	if err != nil {
		t.Fatalf("create shared group failed: %v", err)
	}
	for _, model := range []string{"model-x", "model-y"} {
		if _, err := svc.CreateAssignment(ctx, &store.ModelRoutingAssignmentRecord{ModelName: model, GroupID: group.ID, Enabled: true}); err != nil {
			t.Fatalf("create assignment %q failed: %v", model, err)
		}
	}
	if shared := findBinding(t, svc, "model-x").SharedModels; len(shared) != 1 || shared[0] != "model-y" {
		t.Fatalf("legacy shared group should be reported, got %v", shared)
	}

	target := findBinding(t, svc, "model-x")
	if err := svc.UpdateBinding(ctx, target.AssignmentID, ModelRouteBindingInput{
		Model: "model-x", Endpoints: []string{"relay-a", "relay-b"}, RouteMode: "auto", Enabled: true,
	}); err != nil {
		t.Fatalf("update shared binding failed: %v", err)
	}

	updated := findBinding(t, svc, "model-x")
	untouched := findBinding(t, svc, "model-y")
	if updated.GroupID == untouched.GroupID {
		t.Fatal("editing a shared binding must split it into a dedicated group")
	}
	if len(untouched.Endpoints) != 1 || untouched.Endpoints[0] != "relay-a" {
		t.Fatalf("the other model must keep its endpoints, got %v", untouched.Endpoints)
	}
	if len(updated.SharedModels) != 0 || len(untouched.SharedModels) != 0 {
		t.Fatalf("both bindings should own their groups after the split: %v / %v", updated.SharedModels, untouched.SharedModels)
	}
}

func TestDeleteBinding_ReclaimsDedicatedGroupOnly(t *testing.T) {
	svc := newBindingServiceForTest(t, "relay-a")
	ctx := context.Background()
	first := mustCreateBinding(t, svc, ModelRouteBindingInput{Model: "model-a", Endpoints: []string{"relay-a"}, RouteMode: "auto", Enabled: true})
	mustCreateBinding(t, svc, ModelRouteBindingInput{Model: "model-b", Endpoints: []string{"relay-a"}, RouteMode: "auto", Enabled: true})

	if err := svc.DeleteBinding(ctx, first); err != nil {
		t.Fatalf("delete binding failed: %v", err)
	}
	groups, err := svc.ListGroups(ctx)
	if err != nil {
		t.Fatalf("list groups failed: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "model-b" {
		t.Fatalf("deleting a binding must reclaim exactly its own group, got %#v", groups)
	}
	bindings, _ := svc.ListBindings(ctx)
	if len(bindings) != 1 || bindings[0].Model != "model-b" {
		t.Fatalf("unexpected remaining bindings: %#v", bindings)
	}
}

func TestDedicatedGroupName_DeduplicatesAgainstExistingGroups(t *testing.T) {
	records := []*store.ModelUpstreamGroupRecord{
		{ID: 1, Name: "claude-opus"},
		{ID: 2, Name: "Claude-Opus #2"},
	}
	if name := dedicatedGroupName("claude-opus", records, 0); name != "claude-opus #3" {
		t.Fatalf("expected the first free suffix, got %q", name)
	}
	// 自己的组名不算冲突，改端点时不应该被迫改名。
	if name := dedicatedGroupName("claude-opus", records, 1); name != "claude-opus" {
		t.Fatalf("own name must stay stable, got %q", name)
	}
	long := strings.Repeat("m", 200)
	if runes := []rune(dedicatedGroupName(long, nil, 0)); len(runes) > 64 {
		t.Fatalf("generated name must respect the length cap, got %d runes", len(runes))
	}
}
