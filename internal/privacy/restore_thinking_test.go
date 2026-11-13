package privacy

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

// 兼容上游返回空签名的 thinking；签名状态必须按块记录，不能由字段顺序推断。
func TestRestoreClaudeThinkingSignature(t *testing.T) {
	const sample = "138" + "12345678"
	for _, tc := range []struct {
		name, fields string
		sealed       bool
	}{
		{"missing", `"thinking":%q`, false},
		{"empty-before", `"signature":"","thinking":%q`, false},
		{"empty-after", `"thinking":%q,"signature":""`, false},
		{"escaped-empty", `"thinking":%q,"\u0073ignature":""`, false},
		{"signed-before", `"signature":"opaque","thinking":%q`, true},
		{"signed-after", `"thinking":%q,"signature":"opaque"`, true},
		{"escaped-signed", `"thinking":%q,"\u0073ignature":"\u0061"`, true},
		{"whitespace", `"thinking":%q,"signature":" "`, true},
		{"null", `"thinking":%q,"signature":null`, true},
		{"number", `"thinking":%q,"signature":1`, true},
		{"object", `"thinking":%q,"signature":{}`, true},
		{"array", `"thinking":%q,"signature":[]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := BuiltinPIIRules()
			for i := range rules {
				rules[i].ID = int64(i + 1)
			}
			s := newTestSnapshot(t, redactSettings(), rules...)
			s.Settings.ResponseRestoreEnabled = true
			s.Settings.OnError = OnErrorFailClosed
			s.RestoreKey = bytes.Repeat([]byte{1}, 32)
			s.Dict = NewRestoreDict()
			body := func(text string) []byte {
				return []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking",` + fmt.Sprintf(tc.fields, text) + `}]}],"metadata":{"large":9007199254740993}}`)
			}
			clean := body("ordinary reasoning")
			r, err := s.Apply(claudeRequest(), clean)
			if err != nil || r.Changed || !bytes.Equal(r.Body, clean) {
				t.Fatalf("无命中思考块发生变化: %v", err)
			}
			r.Restore.Release()
			raw := body("JSON numeric example: " + sample)
			compareJSONWalkers(t, raw)
			r, err = s.Apply(claudeRequest(), raw)
			if tc.sealed {
				policy, ok := err.(*PolicyError)
				if !ok || policy.Code != codeRestoreRejected || policy.StatusCode != 422 || !bytes.Equal(r.Body, raw) {
					t.Fatal("有效或不明确的签名命中必须拒绝并保留原文")
				}
				return
			}
			if err != nil || !r.Changed || r.HitCount != 1 || r.Restore == nil {
				t.Fatalf("空签名思考应正常脱敏: %v", err)
			}
			defer r.Restore.Release()
			text := gjson.GetBytes(r.Body, "messages.0.content.0.thinking").Str
			token := text[len("JSON numeric example: "):]
			if original, ok := r.Restore.Lookup(token); !ok || original != sample {
				t.Fatal("思考脱敏没有建立正确回填映射")
			}
			if !bytes.Equal(r.Body, bytes.Replace(raw, []byte(sample), []byte(token), 1)) {
				t.Fatal("思考字段之外的字节发生变化")
			}
		})
	}
}

func TestRestoreClaudeThinkingSignatureIsolation(t *testing.T) {
	const secret = "synthetic-thinking-A1B2"
	encoded := fmt.Sprintf("%q", secret)
	signed := `{ "signature":"opaque-` + secret + `", "thinking":"ordinary", "type":"thinking" }`
	unsigned := `{"type":"thinking","thinking":` + encoded + `,"signature":""}`
	raw := []byte(`{"messages":[{"role":"assistant","content":[` + signed + `,` + unsigned + `]},{"role":"assistant","content":[` + unsigned + `,` + signed + `]}]}`)
	for _, historical := range []bool{false, true} {
		t.Run(fmt.Sprintf("historical=%t", historical), func(t *testing.T) {
			s := newTestSnapshot(t, redactSettings(), literalRule(1, 1, secret, "[MASK]", ActionRedact))
			s.Settings.ResponseRestoreEnabled = true
			s.Settings.OnError = OnErrorFailClosed
			s.RestoreKey = bytes.Repeat([]byte{1}, 32)
			s.Dict = NewRestoreDict()
			if historical {
				seed, err := s.Apply(claudeRequest(), []byte(`{"messages":[{"role":"user","content":`+encoded+`}]}`))
				if err != nil {
					t.Fatal(err)
				}
				publishDictionaryTest(t, seed.Restore)
				seed.Restore.Release()
				s.Rules = nil
			}
			compareJSONWalkers(t, raw)
			r, err := s.Apply(claudeRequest(), raw)
			if err != nil || !r.Changed || r.Restore == nil {
				t.Fatalf("其他块的签名不应封住空签名思考: %v", err)
			}
			defer r.Restore.Release()
			for _, path := range []string{"messages.0.content.0", "messages.1.content.1"} {
				if gjson.GetBytes(r.Body, path).Raw != signed {
					t.Fatal("有签名块的原始字节被改动")
				}
			}
			for _, path := range []string{"messages.0.content.1.thinking", "messages.1.content.0.thinking"} {
				token := gjson.GetBytes(r.Body, path).Str
				if v, ok := r.Restore.Lookup(token); !ok || v != secret {
					t.Fatal("不同消息或内容块未分别脱敏")
				}
			}
			if historical {
				blocked := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":` + encoded + `,"signature":"opaque"}]}]}`)
				if _, err := s.Apply(claudeRequest(), blocked); err == nil {
					t.Fatal("有签名思考的历史强值应继续拒绝")
				}
			}
		})
	}
}
