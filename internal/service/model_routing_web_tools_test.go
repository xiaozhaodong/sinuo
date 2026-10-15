package service

import (
	"context"
	"testing"

	"sinuo/internal/endpoint"
	"sinuo/internal/modelrouting"
	"sinuo/internal/store"
)

func TestModelRoutingService_WebToolsBinding(t *testing.T) {
	service, _ := newModelRoutingServiceForTest(t, func(name string) bool { return name == "endpoint-a" || name == "endpoint-web" })
	ctx := context.Background()
	if err := service.Initialize(ctx); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	if _, err := service.CreateBinding(ctx, ModelRouteBindingInput{Model: "claude-sonnet", Endpoints: []string{"endpoint-a"}, RouteMode: modelrouting.RouteModeAuto, Enabled: true}); err != nil {
		t.Fatalf("create model binding failed: %v", err)
	}
	webID, err := service.CreateBinding(ctx, ModelRouteBindingInput{Kind: modelrouting.BindingKindWebTools, Model: "ignored", Endpoints: []string{"endpoint-web"}, RouteMode: modelrouting.RouteModeAuto, Enabled: true})
	if err != nil {
		t.Fatalf("create web tools binding failed: %v", err)
	}
	if _, err := service.CreateBinding(ctx, ModelRouteBindingInput{Kind: modelrouting.BindingKindWebTools, Endpoints: []string{"endpoint-a"}, RouteMode: modelrouting.RouteModeAuto, Enabled: true}); err == nil {
		t.Fatal("second web tools binding must be rejected")
	}
	if _, err := service.CreateBinding(ctx, ModelRouteBindingInput{Kind: "bogus", Model: "x", Endpoints: []string{"endpoint-a"}, RouteMode: modelrouting.RouteModeAuto, Enabled: true}); err == nil {
		t.Fatal("unknown binding kind must be rejected")
	}
	if _, err := service.CreateBinding(ctx, ModelRouteBindingInput{Model: "server_tool:evil", Endpoints: []string{"endpoint-a"}, RouteMode: modelrouting.RouteModeAuto, Enabled: true}); err == nil {
		t.Fatal("reserved prefix must be rejected for model bindings")
	}
	// kind=model 直接填保留键本身也必须被拒：下层校验放行 server_tool:web 是给
	// kind=web_tools 入库用的，普通模型入口不能借此绕过前端造出第二条联网映射。
	for _, kind := range []string{"", modelrouting.BindingKindModel} {
		if _, err := service.CreateBinding(ctx, ModelRouteBindingInput{Kind: kind, Model: " Server_Tool:Web ", Endpoints: []string{"endpoint-a"}, RouteMode: modelrouting.RouteModeAuto, Enabled: true}); err == nil {
			t.Fatalf("kind=%q must not be able to claim the web tools reserved key", kind)
		}
	}
	if _, err := service.UpdateSettings(ctx, store.ModelRoutingSettingsPatch{ClaudeEnabled: boolPtr(true)}); err != nil {
		t.Fatalf("enable model routing failed: %v", err)
	}

	bindings, err := service.ListBindings(ctx)
	if err != nil {
		t.Fatalf("list bindings failed: %v", err)
	}
	var web *ModelRouteBinding
	for i := range bindings {
		if bindings[i].AssignmentID == webID {
			web = &bindings[i]
		}
	}
	if web == nil || web.Kind != modelrouting.BindingKindWebTools || web.Model != modelrouting.WebToolsKey || len(web.Endpoints) != 1 || web.Endpoints[0] != "endpoint-web" {
		t.Fatalf("unexpected web tools binding view: %#v", web)
	}

	scope, ok := service.ResolveScope("claude-sonnet", true)
	if !ok || scope.Kind != endpoint.RouteScopeUpstreamGroup || scope.MatchedKey != modelrouting.WebToolsKey || len(scope.Members) != 1 || scope.Members[0] != "endpoint-web" {
		t.Fatalf("web tools request should route to the web tools endpoints: %#v, %v", scope, ok)
	}
	scope, ok = service.ResolveScope("claude-sonnet", false)
	if !ok || scope.MatchedKey != "claude-sonnet" || scope.Members[0] != "endpoint-a" {
		t.Fatalf("plain request should keep the model binding: %#v, %v", scope, ok)
	}
	models, err := service.ModelsUsingEndpoint(ctx, "endpoint-web")
	if err != nil || len(models) != 1 || models[0] != "联网工具" {
		t.Fatalf("endpoint usage guard should name the web tools binding: %#v, %v", models, err)
	}

	if err := service.UpdateBinding(ctx, webID, ModelRouteBindingInput{Kind: modelrouting.BindingKindWebTools, Endpoints: []string{"endpoint-a"}, RouteMode: modelrouting.RouteModeAuto, Enabled: false}); err != nil {
		t.Fatalf("update web tools binding failed: %v", err)
	}
	if scope, ok := service.ResolveScope("claude-sonnet", true); !ok || scope.MatchedKey != "claude-sonnet" {
		t.Fatalf("disabled web tools binding should fall back to the model binding: %#v, %v", scope, ok)
	}
	if err := service.DeleteBinding(ctx, webID); err != nil {
		t.Fatalf("delete web tools binding failed: %v", err)
	}
	if groups, _ := service.ListGroups(ctx); len(groups) != 1 {
		t.Fatalf("dedicated web tools group should be reclaimed: %d groups", len(groups))
	}
}
