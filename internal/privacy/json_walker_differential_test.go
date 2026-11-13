package privacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func walkerReplay(claude, wrapped bool) *restoreInput {
	r := &restoreInput{claude: claude, items: make(map[int]*restoreInputItem)}
	if wrapped {
		r.wrapped = &restoreInput{items: make(map[int]*restoreInputItem)}
	}
	return r
}

func compareJSONWalkers(t *testing.T, body []byte) {
	t.Helper()
	for mode := 0; mode < 4; mode++ {
		var oldReplay, newReplay *restoreInput
		if mode > 0 {
			oldReplay = walkerReplay(mode == 1, mode == 3)
			newReplay = walkerReplay(mode == 1, mode == 3)
		}
		match := func([]pathElem) bool { return true }
		oldSegments, oldErr := referenceWalkJSONText(body, match, oldReplay)
		newSegments, newErr := walkJSONText(body, match, newReplay)
		if (oldErr == nil) != (newErr == nil) {
			t.Fatalf("mode=%d body=%q referenceErr=%v newErr=%v", mode, body, oldErr, newErr)
		}
		if oldErr != nil {
			continue
		}
		if !reflect.DeepEqual(oldSegments, newSegments) || !reflect.DeepEqual(oldReplay, newReplay) {
			t.Fatalf("mode=%d body=%q segments or replay differ\nold=%+v\nnew=%+v\noldReplay=%+v\nnewReplay=%+v", mode, body, oldSegments, newSegments, oldReplay, newReplay)
		}
	}
}

func TestJSONWalkerDifferential(t *testing.T) {
	cases := []string{
		``, ` `, `{}`, `[]`, `{} {}`, `truefalse`, `01`, `1.`, `1e+`, `-0.3e-12`,
		`{"a":1,"a":2}`, `{"a":1,"\u0061":2}`, `{"x":"\ud800"}`, `{"x":"\ud83d\ude00"}`,
		`{"x":"a\\b\"c\/d\b\f\n\r\t"}`, `{"stream":true}`, `{"stream":"true"}`,
		`{"input":[{"type":"function_call","arguments":"{}","encrypted_content":"opaque","call_id":"c","name":"fn"}]}`,
		`{"conversation":{"id":"c"},"input":[{"type":"item_reference","id":"i"}]}`,
		`{"response":{"stream":false,"input":[{"type":"reasoning","summary":[{"text":"text"}]}]}}`,
		`{"input":[{"encrypted_output":[]}]}`, `{"input":[{"encrypted_output":[null]}]}`,
		`{"a":}`, `{"a":true,}`, `[1,]`, `{"a" 1}`, `{"x":"\q"}`, `{"x":"\u12XZ"}`,
		"{\"x\":\"\xff\"}", "{\"\xff\":1,\"\ufffd\":2}",
	}
	for _, n := range []int{255, 256, 257, 258, 1000} {
		cases = append(cases, `{"x":`+strings.Repeat("[", n)+`"text"`+strings.Repeat("]", n)+`}`)
	}
	for _, body := range cases {
		compareJSONWalkers(t, []byte(body))
	}
	for _, fixture := range jsonWalkerFixtures() {
		compareJSONWalkers(t, fixture.body)
	}
	rng := rand.New(rand.NewSource(1337))
	var value func(int) any
	value = func(depth int) any {
		limit := 6
		if depth > 4 {
			limit = 4
		}
		switch rng.Intn(limit) {
		case 0:
			return nil
		case 1:
			return rng.Intn(2) == 0
		case 2:
			return json.Number(fmt.Sprintf("%d.%03de%d", rng.Intn(2000)-1000, rng.Intn(1000), rng.Intn(12)-6))
		case 3:
			texts := []string{"plain", "中文🙂", "\n\t\\\"", "", "stream", "input", "password=sample123456", "\ufffd"}
			return texts[rng.Intn(len(texts))]
		case 4:
			a := make([]any, rng.Intn(5))
			for i := range a {
				a[i] = value(depth + 1)
			}
			return a
		default:
			m := map[string]any{}
			keys := []string{"input", "messages", "content", "stream", "type", "arguments", "response", "conversation", "id", "encrypted_output", "namespace"}
			for i, n := 0, rng.Intn(6); i < n; i++ {
				m[keys[rng.Intn(len(keys))]] = value(depth + 1)
			}
			return m
		}
	}
	for i := 0; i < 6000; i++ {
		body, err := json.Marshal(value(0))
		if err != nil {
			t.Fatal(err)
		}
		compareJSONWalkers(t, body)
		if len(body) > 0 {
			changed := bytes.Clone(body)
			switch i % 3 {
			case 0:
				changed = changed[:rng.Intn(len(changed))]
			case 1:
				changed[rng.Intn(len(changed))] = byte(rng.Intn(256))
			case 2:
				pos := rng.Intn(len(changed))
				changed = append(changed[:pos], changed[pos+1:]...)
			}
			compareJSONWalkers(t, changed)
		}
	}
	t.Log("固定边界样例、12 组正文及 12000 组随机/变异输入，逐项比较 4 种模式的接纳结果、区间、路径、解码文本和 replay 状态")
}

func TestJSONWalkerEscapedPathAndBytePreservation(t *testing.T) {
	bodies := []string{
		`{"messages":[{"role":"user","content":"line\n\"quote\"\\path\t\u4e2d\u6587"}],"metadata":{"ignored":"\uD800\uDC00"}}`,
		`{"input":[{"type":"function_call","arguments":"{\"text\":\"中文\\n\\\"quoted\\\"\",\"nested\":{\"value\":\"secret\"}}"}],"stream":true}`,
		`{"messages":[{"content":"\ud800\uDC00"}],"\u0061":"one","a":"two"}`,
		"{\"messages\":[{\"content\":\"\xff\\n\xfe\"}]}",
	}
	for _, body := range bodies {
		for _, path := range []string{"/v1/messages", "/v1/responses", "/v1/responses/compact", "/v1/alpha/search"} {
			for _, match := range []segmentMatcher{walkerForPath(path), nil, func([]pathElem) bool { return false }} {
				oldSegments, oldErr := referenceWalkJSONText([]byte(body), match, nil)
				got, err := walkJSONText([]byte(body), match, nil)
				if (oldErr == nil) != (err == nil) || !reflect.DeepEqual(oldSegments, got) {
					t.Fatalf("path=%s body=%q old=%v new=%v", path, body, oldErr, err)
				}
				if err == nil && !bytes.Equal(replaceSegments([]byte(body), got, nil), []byte(body)) {
					t.Fatal("零命中正文被改写")
				}
			}
		}
		compareJSONWalkers(t, []byte(body))
	}
}

// 旧 Token 实现作为独立行为参照，持续探索非法输入、Unicode 和容器组合。
func FuzzJSONWalkerDifferential(f *testing.F) {
	for _, seed := range []string{
		`{"messages":[{"content":"text\\path\n\uD83D\uDE00"}]}`,
		`{"input":[{"type":"function_call","arguments":"{\"q\":\"value\"}"}]}`,
		`{"response":{"input":[{"type":"reasoning","summary":[{"text":"summary"}]}]}}`,
		`{"stream":true,"str\u0065am":false}`, `{"x":"\ud800"}`, `{"x":1e+}`, `truefalse`, "{\"x\":\"\xff\"}",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 64<<10 {
			t.Skip()
		}
		compareJSONWalkers(t, body)
	})
}

func TestJSONWalkerDeepNonText(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/responses"} {
		for _, shape := range []string{"arrays", "objects", "mixed"} {
			for _, depth := range []int{256, 1000, 10000} {
				t.Run(fmt.Sprintf("%s/%s/depth=%d", path, shape, depth), func(t *testing.T) {
					body := deepNonTextJSON(path, shape, depth, `[0,true,false,null,{},[]]`)
					compareJSONWalkers(t, body)
					want, oldErr := referenceWalkJSONText(body, walkerForPath(path), nil)
					got, err := walkJSONText(body, walkerForPath(path), nil)
					if oldErr != nil || err != nil || !reflect.DeepEqual(want, got) || len(got) != 2 {
						t.Fatalf("deep segments: oldErr=%v err=%v count=%d", oldErr, err, len(got))
					}
					if got[0].Value != "before fixture-secret" || got[1].Value != "after fixture-secret" {
						t.Fatalf("深层字段前后的正文丢失: %+v", got)
					}
					s := newTestSnapshot(t, redactSettings(), literalRule(1, 1, "fixture-secret", "[redacted]", ActionRedact))
					req := claudeRequest()
					if path == "/v1/responses" {
						req.Path, req.UpstreamType = path, UpstreamTypeAccount
					}
					r, err := s.Apply(req, body)
					if err != nil || !r.Changed || r.HitCount != 2 || r.Truncated || r.SkippedReason != "" {
						t.Fatalf("deep apply: err=%v changed=%t hits=%d truncated=%t skipped=%s", err, r.Changed, r.HitCount, r.Truncated, r.SkippedReason)
					}
					if !bytes.Equal(r.Body, bytes.ReplaceAll(body, []byte("fixture-secret"), []byte("[redacted]"))) {
						t.Fatal("正文脱敏改写了深层非文本字段")
					}
					clean := bytes.ReplaceAll(body, []byte("fixture-secret"), []byte("ordinary"))
					r, err = s.Apply(req, clean)
					if err != nil || r.Changed || r.HitCount != 0 || r.Truncated || r.SkippedReason != "" || !bytes.Equal(r.Body, clean) {
						t.Fatalf("深层零命中请求未保持原样: err=%v changed=%t hits=%d truncated=%t skipped=%s", err, r.Changed, r.HitCount, r.Truncated, r.SkippedReason)
					}
				})
			}
		}
	}
}

func TestJSONWalkerDeepNonTextSyntax(t *testing.T) {
	for _, shape := range []string{"arrays", "objects", "mixed"} {
		for _, leaf := range []string{"1e+", "tru", "0,", `{"bad":}`, `[null,]`} {
			t.Run(shape+"/"+leaf, func(t *testing.T) {
				body := deepNonTextJSON("/v1/messages", shape, 1000, leaf)
				for _, match := range []segmentMatcher{nil, matchClaudeMessagesTextPath} {
					if _, err := referenceWalkJSONText(body, match, nil); err == nil {
						t.Fatal("对照意外接受非法 JSON")
					}
					if _, err := walkJSONText(body, match, nil); err == nil {
						t.Fatal("未匹配字段中的深层非法 JSON 被跳过")
					}
				}
			})
		}
	}
	body := deepNonTextJSON("/v1/messages", "mixed", 10000, "0")
	for _, malformed := range [][]byte{body[:len(body)-1], append(bytes.Clone(body), ']')} {
		if _, err := walkJSONText(malformed, nil, nil); err == nil {
			t.Fatal("深层输入的缺失或多余括号未被拒绝")
		}
	}
}
