package modelrouting

import "testing"

func TestCompile_ExactMatchAndDisabledAssignment(t *testing.T) {
	snapshot, err := Compile(
		Settings{Enabled: true},
		[]Group{{ID: 1, Name: "primary", Members: []string{"a"}, RouteMode: RouteModeAuto}},
		[]Assignment{
			{ID: 1, ModelName: "Claude-Sonnet", GroupID: 1, Enabled: true},
			{ID: 2, ModelName: "disabled-model", GroupID: 1, Enabled: false},
		},
	)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if got, ok := snapshot.Resolve(" claude-sonnet "); !ok || got.Group.ID != 1 {
		t.Fatalf("exact case-insensitive match failed: %#v, %v", got, ok)
	}
	if _, ok := snapshot.Resolve("claude-sonnet-preview"); ok {
		t.Fatal("prefix match must not be enabled in P0")
	}
	if _, ok := snapshot.Resolve("disabled-model"); ok {
		t.Fatal("disabled assignment must not be active")
	}
}

func TestCompile_RejectsDuplicateAndMissingGroup(t *testing.T) {
	validGroup := Group{ID: 1, Name: "g", Members: []string{"a"}, RouteMode: RouteModeAuto}
	if _, err := Compile(Settings{}, []Group{validGroup, validGroup}, nil); err == nil {
		t.Fatal("duplicate group id should fail")
	}
	if _, err := Compile(Settings{}, []Group{validGroup}, []Assignment{{ModelName: "m", GroupID: 2, Enabled: true}}); err == nil {
		t.Fatal("assignment referencing missing group should fail")
	}
}

func TestSnapshotReturnsCopies(t *testing.T) {
	members := []string{"a"}
	snapshot, err := Compile(Settings{}, []Group{{ID: 1, Name: "g", Members: members, RouteMode: RouteModeAuto}}, []Assignment{{ModelName: "m", GroupID: 1, Enabled: true}})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	members[0] = "changed"
	groups := snapshot.Groups()
	groups[0].Members[0] = "changed-again"
	resolved, ok := snapshot.Resolve("m")
	if !ok || resolved.Group.Members[0] != "a" {
		t.Fatalf("snapshot was mutated through input/accessor: %#v", resolved.Group)
	}
	assignment := resolved.Assignment
	assignment.ModelName = "changed"
	resolvedAgain, _ := snapshot.Resolve("m")
	if resolvedAgain.Assignment.ModelName != "m" {
		t.Fatal("snapshot assignment was exposed for mutation")
	}
}

func TestValidateGroup_RouteAndEndpointRules(t *testing.T) {
	base := Group{Name: "primary", Members: []string{"a"}, RouteMode: RouteModeAuto}
	if err := ValidateGroup(base, nil, map[string]struct{}{"a": {}}); err != nil {
		t.Fatalf("valid auto group rejected: %v", err)
	}
	invalid := base
	invalid.RouteTarget = "a"
	if err := ValidateGroup(invalid, nil, map[string]struct{}{"a": {}}); err == nil {
		t.Fatal("auto group with target should fail")
	}
	invalid = base
	invalid.RouteMode = RouteModeManualFixed
	if err := ValidateGroup(invalid, nil, map[string]struct{}{"a": {}}); err == nil {
		t.Fatal("manual group without target should fail")
	}
	invalid = base
	invalid.Members = []string{"missing"}
	if err := ValidateGroup(invalid, nil, map[string]struct{}{"a": {}}); err == nil {
		t.Fatal("unknown endpoint should fail when endpoint set is supplied")
	}
}

func TestValidateAssignment_ExactAndBounds(t *testing.T) {
	groups := []Group{{ID: 1}}
	if err := ValidateAssignment(Assignment{ModelName: "claude-sonnet", GroupID: 1, Note: "ok"}, MatchExact, nil, groups); err != nil {
		t.Fatalf("valid assignment rejected: %v", err)
	}
	if err := ValidateAssignment(Assignment{ModelName: "claude-sonnet", GroupID: 1}, "prefix", nil, groups); err == nil {
		t.Fatal("prefix assignment should fail")
	}
	if err := ValidateAssignment(Assignment{ModelName: "claude sonnet", GroupID: 1}, MatchExact, nil, groups); err == nil {
		t.Fatal("whitespace in model name should fail")
	}
	if err := ValidateAssignment(Assignment{ModelName: "claude-sonnet", GroupID: 2}, MatchExact, nil, groups); err == nil {
		t.Fatal("unknown group should fail")
	}
}
