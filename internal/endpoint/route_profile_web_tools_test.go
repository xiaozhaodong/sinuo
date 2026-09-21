package endpoint

import "testing"

func TestBuildRouteRequestProfile_DetectsWebServerTools(t *testing.T) {
	cases := map[string]struct {
		body string
		want bool
	}{
		"web_search new variant":  {`{"model":"m","tools":[{"type":"web_search_20260209","name":"web_search"}]}`, true},
		"web_fetch old variant":   {`{"model":"m","tools":[{"type":"web_fetch_20250910","name":"web_fetch"}]}`, true},
		"mixed with custom tools": {`{"model":"m","tools":[{"name":"fn","input_schema":{}},{"type":"web_search_20250305","name":"x"}]}`, true},
		"custom tool only":        {`{"model":"m","tools":[{"name":"web_search","input_schema":{}}]}`, false},
		"other server tool":       {`{"model":"m","tools":[{"type":"code_execution_20260521","name":"code_execution"}]}`, false},
		"no tools":                {`{"model":"m"}`, false},
		"tools not an array":      {`{"model":"m","tools":{"type":"web_search_20260209"}}`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			profile := BuildRouteRequestProfile("/v1/messages", []byte(tc.body))
			if profile.WebTools != tc.want {
				t.Fatalf("WebTools=%v, want %v", profile.WebTools, tc.want)
			}
		})
	}

	profile := BuildRouteRequestProfile("/v1/messages", []byte(`{"model":"m","tools":[
		{"type":"web_search_20260209","name":"web_search"},
		{"type":"custom","name":"fn","input_schema":{}},
		{"name":"fn2","input_schema":{}},
		{"type":"WEB_SEARCH_20260209","name":"dup"},
		{"type":"code_execution_20260521","name":"code_execution"}]}`))
	if got := profile.ServerToolTypes; len(got) != 2 || got[0] != "web_search_20260209" || got[1] != "code_execution_20260521" {
		t.Fatalf("server tool types should be deduplicated, lowercased and exclude custom tools: %#v", got)
	}
}

// recordingRouteScopePolicy 记录 ResolveScope 实际收到的 webTools 标志，
// 用来锁定联网覆盖的路径边界。
type recordingRouteScopePolicy struct {
	testRouteScopePolicy
	webTools *bool
}

func (p recordingRouteScopePolicy) ResolveScope(model string, webTools bool) (RouteScope, bool) {
	*p.webTools = webTools
	return p.testRouteScopePolicy.ResolveScope(model, webTools)
}

// TestResolveRouteScope_WebToolsOverrideSkipsCountTokens 锁定联网工具覆盖只作用于
// /v1/messages：count_tokens 即便带 web_search 定义也按模型映射归组，避免联网映射
// 固定到不支持计数的端点后把同一条请求的计数打成 422。
func TestResolveRouteScope_WebToolsOverrideSkipsCountTokens(t *testing.T) {
	manager := newSchedulerTestManager(t, schedulerTestConfig(true, "a"))
	var seen bool
	manager.SetRouteScopePolicy(recordingRouteScopePolicy{
		testRouteScopePolicy: testRouteScopePolicy{scope: RouteScope{Kind: RouteScopeUnassigned}},
		webTools:             &seen,
	})
	body := []byte(`{"model":"claude-x","tools":[{"type":"web_search_20260209","name":"web_search"}]}`)

	manager.ResolveRouteScope(BuildRouteRequestProfile("/v1/messages", body))
	if !seen {
		t.Fatal("/v1/messages with web tools must resolve with webTools=true")
	}
	manager.ResolveRouteScope(BuildRouteRequestProfile("/v1/messages/count_tokens", body))
	if seen {
		t.Fatal("/v1/messages/count_tokens must not apply the web tools override")
	}
}
