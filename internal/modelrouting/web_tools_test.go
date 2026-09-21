package modelrouting

import "testing"

func TestResolveRequest_WebToolsOverridesModelAssignment(t *testing.T) {
	snapshot, err := Compile(
		Settings{Enabled: true},
		[]Group{
			{ID: 1, Name: "claude-sonnet", Members: []string{"a"}, RouteMode: RouteModeAuto},
			{ID: 2, Name: "web", Members: []string{"b"}, RouteMode: RouteModeAuto},
		},
		[]Assignment{
			{ID: 1, ModelName: "claude-sonnet", GroupID: 1, Enabled: true},
			{ID: 2, ModelName: WebToolsKey, GroupID: 2, Enabled: true},
		},
	)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if got, ok := snapshot.ResolveRequest("claude-sonnet", true); !ok || got.Group.ID != 2 || got.MatchedKey != WebToolsKey {
		t.Fatalf("web tools request should hit the web tools binding: %#v, %v", got, ok)
	}
	if got, ok := snapshot.ResolveRequest("claude-sonnet", false); !ok || got.Group.ID != 1 || got.MatchedKey != "claude-sonnet" {
		t.Fatalf("plain request should hit the model binding: %#v, %v", got, ok)
	}
	if got, ok := snapshot.ResolveRequest("unknown", true); !ok || got.Group.ID != 2 {
		t.Fatalf("web tools binding must not depend on the model being assigned: %#v, %v", got, ok)
	}
	if _, ok := snapshot.Resolve(WebToolsKey); ok {
		t.Fatal("reserved key must not be reachable as a model name")
	}
	if groups, assignments := snapshot.Counts(); groups != 2 || assignments != 2 {
		t.Fatalf("counts should include the web tools binding: %d/%d", groups, assignments)
	}
	if got := snapshot.Assignments(); len(got) != 2 {
		t.Fatalf("assignments should include the web tools binding: %#v", got)
	}
}

func TestResolveRequest_WebToolsFallsBackWhenUnconfiguredOrDisabled(t *testing.T) {
	snapshot, err := Compile(
		Settings{Enabled: true},
		[]Group{{ID: 1, Name: "g", Members: []string{"a"}, RouteMode: RouteModeAuto}},
		[]Assignment{
			{ID: 1, ModelName: "claude-sonnet", GroupID: 1, Enabled: true},
			{ID: 2, ModelName: WebToolsKey, GroupID: 1, Enabled: false},
		},
	)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if got, ok := snapshot.ResolveRequest("claude-sonnet", true); !ok || got.MatchedKey != "claude-sonnet" {
		t.Fatalf("disabled web tools binding should fall back to the model binding: %#v, %v", got, ok)
	}
	if _, ok := snapshot.ResolveRequest("unknown", true); ok {
		t.Fatal("no web tools binding and unassigned model should stay unassigned")
	}
}

func TestValidateAssignment_ReservedPrefix(t *testing.T) {
	groups := []Group{{ID: 1, Name: "g", Members: []string{"a"}, RouteMode: RouteModeAuto}}
	if err := ValidateAssignment(Assignment{ModelName: "server_tool:other", GroupID: 1}, MatchExact, nil, groups); err == nil {
		t.Fatal("user model names must not use the reserved prefix")
	}
	if err := ValidateAssignment(Assignment{ModelName: WebToolsKey, GroupID: 1}, MatchExact, nil, groups); err != nil {
		t.Fatalf("the web tools key itself must validate: %v", err)
	}
	siblings := []Assignment{{ID: 9, ModelName: WebToolsKey, GroupID: 1}}
	if err := ValidateAssignment(Assignment{ID: 10, ModelName: WebToolsKey, GroupID: 1}, MatchExact, siblings, groups); err == nil {
		t.Fatal("only one web tools binding is allowed")
	}
	if BindingKind(WebToolsKey) != BindingKindWebTools || BindingKind("claude-sonnet") != BindingKindModel {
		t.Fatal("binding kind classification mismatch")
	}
}
