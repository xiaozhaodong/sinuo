package privacy

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestRestoreSealedReasoningReplay(t *testing.T) {
	const value = "private-value-123"
	sealed := `{ "id":"rs_1", "summary":[{"type":"summary_text","text":"` + exampleRestoreToken + `"}], "type":"reasoning", "encrypted_content":"private-value-123\u002f=" }`
	s := newTestSnapshot(t, redactSettings(), literalRule(1, 1, value, "[MASK]", ActionRedact))
	s.Settings.ResponseRestoreEnabled = true
	s.Settings.OnError = OnErrorFailClosed
	s.RestoreKey = bytes.Repeat([]byte{1}, 32)
	req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount, StreamingHTTP: true}
	apply := func(user, item string) (ApplyResult, error) {
		body := fmt.Sprintf(`{"stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]},%s]}`, user, item)
		return s.Apply(req, []byte(body))
	}
	for _, user := range []string{value, "ordinary"} {
		res, err := apply(user, sealed)
		if err != nil || res.Restore == nil || gjson.GetBytes(res.Body, "input.1").Raw != sealed {
			t.Fatalf("密文项未原样转发或缺少请求级校验: %v", err)
		}
		masked := gjson.GetBytes(res.Body, "input.0.content.0.text").Str
		if user == value {
			if original, ok := res.Restore.Lookup(masked); !ok || original != value {
				t.Fatal("可见消息没有建立映射")
			}
		} else if res.Changed || masked != user {
			t.Fatal("无命中的可见内容发生变化")
		}
		if _, ok := res.Restore.Lookup(exampleRestoreToken); ok {
			t.Fatal("从密文项借用了其他请求的标识")
		}
	}
	for _, item := range []string{
		strings.Replace(sealed, exampleRestoreToken, value, 1),
		`{"type":"reasoning","encrypted_content":"opaque","content":[{"type":"reasoning_text","text":"private-value-123"}]}`,
		`{"type":"reasoning","encrypted_content":"opaque","encrypted_output":"opaque"}`,
		`{"type":"reasoning","encrypted_content":"opaque","encrypted_function_args":"opaque"}`,
		`{"type":"reasoning","encrypted_content":{"bad":"type"}}`,
	} {
		if _, err := apply("ordinary", item); err == nil {
			t.Fatal("允许发送需要改写或格式不支持的封装项")
		}
	}
	if _, err := apply("  ", sealed); err != nil {
		t.Fatal("无需前置用户明文也应允许密文重放")
	}
}

func TestRestoreHistoricalWeakValues(t *testing.T) {
	rule := openAIKeyRule()
	rule.Pattern = `password=(?P<redact>[a-zA-Z0-9]+)`
	s := newTestSnapshot(t, redactSettings(), rule)
	s.Dict = NewRestoreDict()
	s.Settings.ResponseRestoreEnabled = true
	s.Settings.OnError = OnErrorFailClosed
	s.RestoreKey = bytes.Repeat([]byte{2}, 32)
	req := claudeRequest()
	seed, err := s.Apply(req, []byte(`{"messages":[{"role":"user","content":"password=admin admin password=8080 password=123456 password=abcdef012345"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(seed.Body), "admin") {
		t.Fatal("本轮发现的短值应补遮")
	}
	publishDictionaryTest(t, seed.Restore)
	seed.Restore.Release()
	for _, tc := range []struct {
		req    Request
		format string
	}{
		{req, `{"messages":[{"role":"assistant","content":[{"type":"thinking","signature":"opaque","thinking":%q}]}]}`},
		{Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount}, `{"input":[{"type":"reasoning","encrypted_content":"opaque","summary":[{"type":"summary_text","text":%q}]}]}`},
	} {
		raw := []byte(fmt.Sprintf(tc.format, "port is 123456 here"))
		r, err := s.Apply(tc.req, raw)
		if err != nil || !bytes.Equal(raw, r.Body) || r.RemaskMatches != 0 {
			t.Fatalf("弱值阻止了历史重放 %s: %v", tc.req.Path, err)
		}
		r.Restore.Release()
		for _, value := range []string{"password=123456", "abcdef012345"} {
			if _, err := s.Apply(tc.req, []byte(fmt.Sprintf(tc.format, value))); err == nil {
				t.Fatal("放行了规则或强值命中", tc.req.Path)
			}
		}
	}
	r, err := s.Apply(req, []byte(`{"messages":[{"role":"user","content":"admin 8080 123456"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Restore.Release()
	if text := gjson.GetBytes(r.Body, "messages.0.content").Str; !strings.HasPrefix(text, "admin 8080 "+RestorePrefix) || r.RemaskMatches != 1 {
		t.Fatal("历史弱值长度边界错误", text)
	}
}

func TestRestoreSealedCurrentRequestValues(t *testing.T) {
	rule := openAIKeyRule()
	rule.Pattern = `password=(?P<redact>[a-zA-Z0-9]+)`
	s := newTestSnapshot(t, redactSettings(), rule)
	s.Settings.ResponseRestoreEnabled = true
	s.Settings.OnError = OnErrorFailClosed
	s.RestoreKey = bytes.Repeat([]byte{2}, 32)
	for _, tc := range []struct {
		req                          Request
		format, sealedPath, userPath string
	}{
		{claudeRequest(), `{"messages":[{"role":"assistant","content":[{"type":"thinking","signature":"opaque","thinking":%q}]},{"role":"user","content":%q}]}`, "messages.0", "messages.1.content"},
		{Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount}, `{"input":[{"type":"reasoning","encrypted_content":"opaque","summary":[{"type":"summary_text","text":%q}]},{"role":"user","content":%q}]}`, "input.0", "input.1.content"},
	} {
		for _, value := range []string{"admin", "123456", "root1234", "abcdef012345"} {
			user := "password=" + value + " " + value
			raw := []byte(fmt.Sprintf(tc.format, value, user))
			result, err := s.Apply(tc.req, raw)
			if value == "abcdef012345" {
				if err == nil {
					t.Fatal("本轮强值不应进入签名历史", tc.req.Path)
				}
				continue
			}
			if err != nil {
				t.Fatal(tc.req.Path, err)
			}
			first, _, _ := strings.Cut(gjson.GetBytes(result.Body, tc.userPath).Str, " ")
			token := strings.TrimPrefix(first, "password=")
			mapped, ok := result.Restore.Lookup(token)
			result.Restore.Release()
			if !ok || mapped != value || gjson.GetBytes(result.Body, tc.userPath).Str != "password="+token+" "+token ||
				gjson.GetBytes(result.Body, tc.sealedPath).Raw != gjson.GetBytes(raw, tc.sealedPath).Raw {
				t.Fatal("普通消息应遮蔽两个位置，签名历史应逐字节保留", tc.req.Path)
			}
			if _, err = s.Apply(tc.req, []byte(fmt.Sprintf(tc.format, "password="+value, user))); err == nil {
				t.Fatal("签名段内直接命中规则应拒绝", tc.req.Path)
			}
		}
	}
}
