package service

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"sinuo/internal/privacy"
	"sinuo/internal/store"
)

func TestPrivacySnapshotPartialCompileKeepsHealthyRules(t *testing.T) {
	svc := newTestPrivacyService(t)
	ctx := context.Background()
	// 直接落库模拟历史坏规则，经真实快照重建保留其余健康规则。
	for _, rule := range []*store.PrivacyRuleRecord{
		{Enabled: true, Name: "Broken", Priority: 100, MatchType: "regex", Pattern: `sk-(unclosed`, Placeholder: "[mask]", Action: "redact", ScopeJSON: "{}", Source: "custom"},
		{Enabled: true, Name: "Healthy", Priority: 110, MatchType: "literal", Pattern: "synthetic-private-value-A1B2", Placeholder: "[mask]", Action: "redact", ScopeJSON: "{}", Source: "custom"},
	} {
		if _, err := svc.store.CreateRule(ctx, rule); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{privacy.ModeDetect, privacy.ModeRedact} {
		for _, restore := range []bool{false, true} {
			for _, onError := range []string{privacy.OnErrorFailOpen, privacy.OnErrorFailClosed} {
				t.Run(fmt.Sprintf("%s/restore=%t/%s", mode, restore, onError), func(t *testing.T) {
					settings, err := svc.GetSettings(ctx)
					if err != nil {
						t.Fatal(err)
					}
					settings.Mode, settings.ResponseRestoreEnabled, settings.OnError = mode, restore, onError
					if _, err := svc.UpdateSettings(ctx, settings); err != nil {
						t.Fatal(err)
					}
					snapshot := svc.CurrentSnapshot()
					if snapshot.CompileError == "" || snapshot.CompileBlocked || svc.Status() != PrivacyStatusDegraded {
						t.Fatal("未构造出部分规则失效、健康规则仍可用的快照")
					}
					for _, tc := range []struct{ path, upstream, body string }{
						{"/v1/messages", privacy.UpstreamTypeEndpoint, `{"messages":[{"role":"user","content":"synthetic-private-value-A1B2"}]}`},
						{"/v1/responses", privacy.UpstreamTypeAccount, `{"input":"synthetic-private-value-A1B2"}`},
					} {
						t.Run(tc.path, func(t *testing.T) {
							raw := []byte(tc.body)
							result, err := svc.Apply(ctx, privacy.Request{Path: tc.path, UpstreamType: tc.upstream, ContentType: "application/json"}, raw)
							if mode == privacy.ModeRedact && restore {
								if !bytes.Equal(result.Body, raw) || result.Changed || result.Restore != nil || result.HitCount != 0 {
									t.Fatal("坏规则快照不得执行可逆脱敏或留下部分改写")
								}
								if onError == privacy.OnErrorFailOpen {
									if err != nil || !result.FailOpen || result.RestoreStatus != "bypassed" {
										t.Fatalf("可逆脱敏失败没有按配置放行: %+v %v", result, err)
									}
								} else if pe, ok := err.(*privacy.PolicyError); !ok || pe.Code != "privacy_restore_rejected" || result.FailOpen {
									t.Fatalf("可逆脱敏失败没有按配置拒绝: %+v %v", result, err)
								}
								return
							}
							if err != nil || result.FailOpen || result.SkippedReason != "" || result.HitCount != 1 {
								t.Fatalf("健康规则未继续扫描: %+v %v", result, err)
							}
							if mode == privacy.ModeDetect {
								if result.Changed || !bytes.Equal(result.Body, raw) {
									t.Fatal("仅检测模式改写了正文")
								}
							} else if !result.Changed || bytes.Contains(result.Body, []byte("synthetic-private-value-A1B2")) || !bytes.Contains(result.Body, []byte("[mask]")) {
								t.Fatalf("普通脱敏未使用健康规则: %s", result.Body)
							}
						})
					}
				})
			}
		}
	}
}

func TestPrivacySnapshotCompileBlockedStillStopsScanning(t *testing.T) {
	for _, mode := range []string{privacy.ModeDetect, privacy.ModeRedact} {
		for _, onError := range []string{privacy.OnErrorFailOpen, privacy.OnErrorFailClosed} {
			t.Run(mode+"/"+onError, func(t *testing.T) {
				settings := privacy.DefaultSettings()
				settings.Mode, settings.OnError = mode, onError
				// 不依赖错误描述字符串，阻断标记本身就必须使快照不可用。
				snapshot := &privacy.Snapshot{Settings: settings, CompileBlocked: true}
				raw := []byte(`{"input":"synthetic"}`)
				result, err := snapshot.Apply(privacy.Request{Path: "/v1/responses"}, raw)
				if !bytes.Equal(result.Body, raw) || result.Changed || result.Restore != nil {
					t.Fatal("不可用快照产生了部分改写")
				}
				if onError == privacy.OnErrorFailOpen {
					if err != nil || !result.FailOpen {
						t.Fatalf("未按配置放行: %+v %v", result, err)
					}
				} else if pe, ok := err.(*privacy.PolicyError); !ok || pe.StatusCode != 503 || pe.Code != privacy.CodeScanFailed || result.FailOpen {
					t.Fatalf("不可用快照未拒绝: %+v %v", result, err)
				}
			})
		}
	}
}
