package privacy

import (
	"bytes"
	"encoding/json"
	"testing"
)

func aiKeyPresetSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	preset, ok := FindPreset(PresetAIAPIKeys)
	if !ok {
		t.Fatal("preset not found")
	}
	rules := make([]Rule, len(preset.Rules))
	copy(rules, preset.Rules)
	for i := range rules {
		rules[i].ID = int64(i + 1)
	}
	return newTestSnapshot(t, redactSettings(), rules...)
}

// 字段类预设覆盖 .env / JSON / YAML / 驼峰写法：关键词前面是 _ 或字母、键名带引号时，旧的 \b 写法匹配不上
func TestAIAPIKeysPresetCoversEnvAndQuotedKeys(t *testing.T) {
	snapshot := aiKeyPresetSnapshot(t)
	cases := map[string]string{
		"OPENAI_API_KEY=Zx9Qw8Er7Ty6Ui5Op4":                        "OPENAI_API_KEY=[密钥]",
		"export GITHUB_TOKEN=Zx9Qw8Er7Ty6Ui5Op4As3":                "export GITHUB_TOKEN=[密钥]",
		"ANTHROPIC_AUTH_TOKEN=cr_Zx9Qw8Er7Ty6Ui5Op4As3":            "ANTHROPIC_AUTH_TOKEN=[密钥]",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENGbPxRfiCYEXKEY": "AWS_SECRET_ACCESS_KEY=[密钥]",
		`"apiKey": "Zx9Qw8Er7Ty6Ui5Op4"`:                           `"apiKey": "[密钥]"`,
		"client_secret: Zx9Qw8Er7Ty6Ui5Op4":                        "client_secret: [密钥]",
		"DB_PASSWORD=hunter2hunter":                                "DB_PASSWORD=[密码]",
		`"password": "hunter2hunter"`:                              `"password": "[密码]"`,
		"spring.datasource.password: hunter2":                      "spring.datasource.password: [密码]",
		"dbPassword = 'hunter2hunter'":                             "dbPassword = '[密码]'",
		"VITE_AMAP_KEY=0123456789abcdef0123456789abcdef":           "VITE_AMAP_KEY=[高德地图密钥]",
		"https://api.example.com/v1?token=Zx9Qw8Er7Ty6Ui5Op4&x=1":  "https://api.example.com/v1?token=[密钥]&x=1",
	}
	for text, want := range cases {
		if got := string(snapshot.ApplyToText(claudeRequest(), text).Body); got != want {
			t.Errorf("%q => %q, want %q", text, got, want)
		}
	}
}

// 放宽键名后不能把代码引用、计数字段当成密钥
func TestAIAPIKeysPresetAvoidsCodeReferences(t *testing.T) {
	snapshot := aiKeyPresetSnapshot(t)
	for _, text := range []string{
		"token = response.json()",
		`secret = os.environ.get("APP_SECRET")`,
		"const apiKey = process.env.OPENAI_API_KEY;",
		"client({apiKey: config.openaiApiKey})",
		"max_tokens: 128000000000",
		"passwordInput = document.getElementById",
		"token_count = 1234567890123",
	} {
		if result := snapshot.ApplyToText(claudeRequest(), text); result.Changed {
			t.Errorf("%q should stay untouched, got %q", text, result.Body)
		}
	}
}

// Claude 历史中的 tool_use.input（Bash 命令、Write 内容）同样脱敏，非字符串值与结构保持不变
func TestApplyRedactsClaudeToolUseInput(t *testing.T) {
	snapshot := newTestSnapshot(t, redactSettings(), openAIKeyRule())
	body := []byte(`{"model":"m","messages":[{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"curl -H 'Authorization: Bearer ` + claudeSecret + `'","timeout":120000}},` +
		`{"type":"tool_use","id":"t2","name":"Write","input":{"file_path":"/app/.env","content":"OPENAI_API_KEY=` + claudeSecret + `\n","append":false}}]}]}`)
	result, err := snapshot.Apply(claudeRequest(), body)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result.Body, []byte(claudeSecret)) || result.HitCount != 2 {
		t.Fatalf("tool_use input must be redacted: hits=%d body=%s", result.HitCount, result.Body)
	}
	var decoded struct {
		Messages []struct {
			Content []struct {
				Input map[string]any `json:"input"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(result.Body, &decoded); err != nil {
		t.Fatalf("redacted body invalid json: %v", err)
	}
	blocks := decoded.Messages[0].Content
	if blocks[0].Input["timeout"] != float64(120000) || blocks[1].Input["append"] != false || blocks[1].Input["file_path"] != "/app/.env" {
		t.Fatalf("non-matching input fields must stay intact: %+v", blocks)
	}
}
