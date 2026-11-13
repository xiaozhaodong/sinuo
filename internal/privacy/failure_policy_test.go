package privacy

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func policySnapshot(t *testing.T) *Snapshot {
	t.Helper()
	s := newTestSnapshot(t, redactSettings(), literalRule(1, 1, "private-fixture-A1B2", "[MASK]", ActionRedact))
	s.Settings.ResponseRestoreEnabled = true
	s.RestoreKey = bytes.Repeat([]byte{1}, 32)
	s.Dict = NewRestoreDict()
	return s
}

func TestRestoreLocalFailuresRespectOnError(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		prepare    func(*Snapshot)
	}{
		{"signed-thinking", `{"messages":[{"content":[{"type":"thinking","thinking":"private-fixture-A1B2","signature":"signed"}]}]}`, nil},
		{"deep-input", `{"messages":[],"metadata":` + strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300) + `}`, nil},
		{"unsupported-history", `{"messages":[{"encrypted_function_args":"opaque","content":"private-fixture-A1B2"}]}`, nil},
		{"key-unavailable", `{"messages":[{"content":"private-fixture-A1B2"}]}`, func(s *Snapshot) { s.RestoreKey = nil }},
		{"rules-unavailable", `{"messages":[{"content":"private-fixture-A1B2"}]}`, func(s *Snapshot) { s.CompileError = "untrusted detail" }},
		{"dictionary-retired", `{"messages":[{"content":"private-fixture-A1B2"}]}`, func(s *Snapshot) { s.Dict.Retire() }},
		{"dictionary-unavailable", `{"messages":[{"content":"private-fixture-A1B2"}]}`, func(s *Snapshot) { s.Dict = nil }},
		{"mapping-overflow", `{"messages":[{"content":` + fmt.Sprintf("%q", strings.Repeat("x", maxRestoreBytes+1)) + `}]}`, func(s *Snapshot) {
			rule := openAIKeyRule()
			rule.Pattern = `.+`
			s.Rules, _ = CompileRules([]Rule{rule})
			s.Settings.ScanMaxBytes = 8 << 20
		}},
		{"marker-boundary", `{"messages":[{"content":"` + exampleRestoreToken + `XYZ"}]}`, func(s *Snapshot) {
			s.Rules, _ = CompileRules([]Rule{literalRule(1, 1, "}}XYZ", "[MASK]", ActionRedact)})
		}},
	} {
		for _, mode := range []string{OnErrorFailOpen, OnErrorFailClosed} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				s := policySnapshot(t)
				s.Settings.OnError = mode
				if tc.prepare != nil {
					tc.prepare(s)
				}
				raw := []byte(tc.body)
				r, err := s.Apply(claudeRequest(), raw)
				if !bytes.Equal(r.Body, raw) || r.Changed || r.Restore != nil || r.RemaskMatches != 0 {
					t.Fatal("失败后留下了部分改写或回填")
				}
				if mode == OnErrorFailOpen {
					if err != nil || !r.FailOpen || r.RestoreStatus != "bypassed" || r.SkippedReason != SkippedFailOpen {
						t.Fatalf("未按配置放行: %+v %v", r, err)
					}
				} else if err == nil || r.FailOpen {
					t.Fatal("未按配置拒绝")
				}
			})
		}
	}
}

func TestRestoreScanLimitIndependentOfOnError(t *testing.T) {
	for _, limitAction := range []string{OverLimitScanPrefix, OverLimitFailClosed} {
		for _, mode := range []string{OnErrorFailOpen, OnErrorFailClosed} {
			t.Run(limitAction+"/"+mode, func(t *testing.T) {
				s := policySnapshot(t)
				s.Settings.ScanMaxBytes, s.Settings.OnError, s.Settings.OverLimitAction = 8, mode, limitAction
				raw := []byte(`{"messages":[{"content":"private-fixture-A1B2"}]}`)
				r, err := s.Apply(claudeRequest(), raw)
				if !bytes.Equal(r.Body, raw) || r.Changed || r.Restore != nil || !r.Truncated {
					t.Fatal("超限必须完整撤销")
				}
				if limitAction == OverLimitScanPrefix {
					if err != nil || !r.FailOpen || r.SkippedReason != SkippedOverLimitOpen {
						t.Fatal("出错策略覆盖了超限放行", err)
					}
				} else if e, ok := err.(*PolicyError); !ok || e.Code != CodeScanBodyTooLarge || e.StatusCode != 413 {
					t.Fatal("超限拒绝被忽略", err)
				}
			})
		}
	}
}

func TestRestoreProtocolErrorsNeverForward(t *testing.T) {
	for _, raw := range []string{`{"messages":[`, `[]`, `{"stream":"true"}`, `{"conversation":{}}`, `{"input":[{"type":"item_reference"}]}`, `{"input":[{"type":"function_call","arguments":"{broken"}]}`, `{"input":[{"type":"function_call","arguments":"{} {}"}]}`, `{"input":[{"type":"function_call","arguments":" "}]}`} {
		s := policySnapshot(t)
		req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount}
		r, err := s.Apply(req, []byte(raw))
		if e, ok := err.(*PolicyError); !ok || e.Code != CodeInvalidRequest || e.StatusCode != 400 || r.FailOpen {
			t.Fatalf("协议错误未明确拒绝 %s: %v", raw, err)
		}
	}
}

func TestRestoreFailedReplayRollsBackCompletedRewrite(t *testing.T) {
	s := policySnapshot(t)
	req := Request{Path: "/v1/responses", UpstreamType: UpstreamTypeAccount, SessionID: "policy", AccountID: 1}
	seed, err := s.Apply(req, []byte(`{"input":"private-fixture-A1B2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Restore.Publish(); err != nil {
		t.Fatal(err)
	}
	token := string(restoreTokenFromBody(seed.Body))
	seed.Restore.RememberFunctionReplay("call", "demo", "", "cipher", `{"v":"`+token+`"}`)
	seed.Restore.Release()
	raw := []byte(`{"input":[{"type":"function_call","name":"demo","call_id":"call","arguments":"{\"v\":\"private-fixture-A1B2!\"}","encrypted_content":"cipher"}]}`)
	r, err := s.Apply(req, raw)
	if err != nil || !r.FailOpen || !bytes.Equal(raw, r.Body) || r.Changed || r.Restore != nil {
		t.Fatal("工具复验失败未撤销已完成的正文改写", err)
	}
}

func restoreTokenFromBody(body []byte) []byte {
	start := bytes.Index(body, []byte(RestorePrefix))
	return body[start : start+RestoreTokenSize]
}

func TestRestorePublishCapacityRespectsFailurePolicy(t *testing.T) {
	for _, mode := range []string{OnErrorFailOpen, OnErrorFailClosed} {
		t.Run(mode, func(t *testing.T) {
			s := policySnapshot(t)
			s.Settings.OnError = mode
			s.Dict = newRestoreDict(1, 4096)
			seed, err := s.Apply(claudeRequest(), []byte(`{"messages":[{"content":"private-fixture-A1B2"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := seed.Restore.Publish(); err != nil {
				t.Fatal(err)
			}
			defer seed.Restore.Release()
			s.Rules, err = CompileRules([]Rule{literalRule(2, 1, "second-fixture-C3D4", "[MASK]", ActionRedact)})
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"messages":[{"content":"second-fixture-C3D4"}]}`)
			r, err := s.Apply(claudeRequest(), raw)
			if err != nil || !r.Changed {
				t.Fatal("未成功暂存第二份正文", err)
			}
			publishErr := r.Restore.Publish()
			if publishErr == nil {
				t.Fatal("未触发已固定字典的容量限制")
			}
			resolved, failure := r.ResolveFailure(raw, publishErr)
			if !bytes.Equal(resolved.Body, raw) || resolved.Changed || resolved.Restore != nil || len(s.Dict.entries) != 1 || s.Dict.capacity.entries != 1 {
				t.Fatal("发布失败没有原子回滚")
			}
			if (mode == OnErrorFailClosed) != (failure != nil) || resolved.FailOpen != (mode == OnErrorFailOpen) {
				t.Fatal("容量失败忽略了出错配置", failure)
			}
		})
	}
}
