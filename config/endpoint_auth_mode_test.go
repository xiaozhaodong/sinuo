package config

import "testing"

func TestNormalizeEndpointAuthMode(t *testing.T) {
	cases := map[string]string{
		"":               "configured",
		"   ":            "configured",
		" client_oauth ": "client_oauth",
		"configured":     "configured",
		"something-else": "something-else",
	}
	for input, want := range cases {
		if got := NormalizeEndpointAuthMode(input); got != want {
			t.Errorf("NormalizeEndpointAuthMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidateEndpointAuthSettings(t *testing.T) {
	cases := []struct {
		name     string
		settings EndpointAuthSettings
		wantErr  bool
	}{
		{"configured 不限制 URL 与凭据", EndpointAuthSettings{AuthMode: EndpointAuthModeConfigured, URL: "https://relay.example.com", Token: "t"}, false},
		{"订阅直通官方地址", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "https://api.anthropic.com"}, false},
		{"订阅直通允许末尾斜杠", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "https://api.anthropic.com/"}, false},
		{"订阅直通拒绝其他主机", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "https://relay.example.com"}, true},
		{"订阅直通拒绝 http", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "http://api.anthropic.com"}, true},
		{"订阅直通拒绝路径", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "https://api.anthropic.com/v1"}, true},
		{"订阅直通拒绝仿冒主机", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "https://api.anthropic.com.evil.com"}, true},
		{"订阅直通拒绝带 userinfo", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "https://user@api.anthropic.com"}, true},
		{"订阅直通拒绝 Token", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "https://api.anthropic.com", Token: "t"}, true},
		{"订阅直通拒绝 API Key", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "https://api.anthropic.com", ApiKey: "k"}, true},
		{"订阅直通拒绝自定义 Authorization", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "https://api.anthropic.com", Headers: map[string]string{"authorization": "x"}}, true},
		{"订阅直通拒绝自定义 X-Api-Key", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "https://api.anthropic.com", Headers: map[string]string{"X-Api-Key": "x"}}, true},
		{"订阅直通允许普通自定义头", EndpointAuthSettings{AuthMode: EndpointAuthModeClientOAuth, URL: "https://api.anthropic.com", Headers: map[string]string{"X-Trace": "1"}}, false},
		{"未知认证方式", EndpointAuthSettings{AuthMode: "oauth_pool", URL: "https://api.anthropic.com"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEndpointAuthSettings(tc.settings)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestEndpointConfigIsClientOAuth(t *testing.T) {
	var nilCfg *EndpointConfig
	if nilCfg.IsClientOAuth() {
		t.Fatal("nil 配置不应视为订阅直通")
	}
	if (&EndpointConfig{}).IsClientOAuth() {
		t.Fatal("空认证方式不应视为订阅直通")
	}
	if !(&EndpointConfig{AuthMode: EndpointAuthModeClientOAuth}).IsClientOAuth() {
		t.Fatal("client_oauth 应视为订阅直通")
	}
}
