package tray

import "testing"

func TestReceiptNotice_GlobalStrategies(t *testing.T) {
	for _, tc := range []struct {
		kind, mode, target, title, body, page string
	}{
		{"claude", "auto", "ignored", "Claude 端点策略已切换", "自动调度", "endpoints"},
		{"claude", "manual_preferred", "venlacy", "Claude 端点策略已切换", "优选「venlacy」", "endpoints"},
		{"claude", "manual_fixed", "venlacy", "Claude 端点策略已切换", "固定「venlacy」", "endpoints"},
		{"codex", "auto", "ignored", "Codex 账号策略已切换", "自动调度（按编排）", "account-pool"},
		{"codex", "manual_fixed", "duckcoding", "Codex 账号策略已切换", "固定「duckcoding」", "account-pool"},
	} {
		t.Run(tc.kind+"/"+tc.mode, func(t *testing.T) {
			assertReceiptNotice(t, SwitchReceipt{Kind: tc.kind, Mode: tc.mode, Target: tc.target, ID: 9},
				Notice{ID: "switch:" + tc.kind, Title: tc.title, Body: tc.body, Target: tc.page})
		})
	}
}

func TestReceiptNotice_BindingStrategies(t *testing.T) {
	for _, tc := range []struct {
		kind, subject, target, title, page string
	}{
		{"claude_binding", "claude-opus-4-1", "venlacy", "Claude 模型路由已切换", "claude-model-routing"},
		{"claude_binding", "联网工具", "venlacy", "Claude 模型路由已切换", "claude-model-routing"},
		{"codex_binding", "gpt-5.6-luna", "duckcoding", "Codex 模型路由已切换", "codex-model-routing"},
	} {
		for _, mode := range []struct{ value, body string }{
			{"auto", "自动调度"}, {"manual_preferred", "优选「" + tc.target + "」"}, {"manual_fixed", "固定「" + tc.target + "」"},
		} {
			t.Run(tc.subject+"/"+mode.value, func(t *testing.T) {
				assertReceiptNotice(t, SwitchReceipt{Kind: tc.kind, Mode: mode.value, Subject: tc.subject, Target: tc.target, ID: 7},
					Notice{ID: "switch:" + tc.kind + ":7", Title: tc.title, Body: tc.subject + "：" + mode.body, Target: tc.page})
			})
		}
	}
}

func TestReceiptNotice_OnlySwitchActions(t *testing.T) {
	for _, kind := range []string{"", "entry", "entry_enable", "entry_disable", "codex_entry", "codex_entry_enable",
		"codex_entry_disable", "endpoint_cooldown", "account_cooldown", "menubar_label", "close_to_tray", "navigate", "copy", "quit"} {
		if IsSwitchAction(kind) {
			t.Errorf("%q 不应产生切换回执", kind)
		}
		if notice, ok := ReceiptNotice(SwitchReceipt{Kind: kind, Mode: "auto"}); ok || notice != (Notice{}) {
			t.Errorf("%q 意外生成回执: %+v", kind, notice)
		}
	}
	for _, kind := range []string{"claude", "claude_binding", "codex", "codex_binding"} {
		if !IsSwitchAction(kind) {
			t.Errorf("%q 应支持切换回执", kind)
		}
	}
	if _, ok := ReceiptNotice(SwitchReceipt{Kind: "claude", Mode: "unknown"}); ok {
		t.Fatal("未知策略不得显示成自动调度")
	}
}

func TestReceiptNotice_IDFollowsObjectNotTarget(t *testing.T) {
	for _, kind := range []string{"claude", "claude_binding", "codex", "codex_binding"} {
		before, _ := ReceiptNotice(SwitchReceipt{Kind: kind, Mode: "manual_fixed", ID: 3, Target: "a"})
		after, _ := ReceiptNotice(SwitchReceipt{Kind: kind, Mode: "auto", ID: 3, Target: "b"})
		if before.ID != after.ID {
			t.Errorf("同一对象切换后应替换通知: %q -> %q", before.ID, after.ID)
		}
		if kind == "claude_binding" || kind == "codex_binding" {
			other, _ := ReceiptNotice(SwitchReceipt{Kind: kind, Mode: "auto", ID: 4})
			if before.ID == other.ID {
				t.Errorf("不同绑定不应互相覆盖: %q", before.ID)
			}
		}
	}
}

func assertReceiptNotice(t *testing.T, receipt SwitchReceipt, want Notice) {
	t.Helper()
	if got, ok := ReceiptNotice(receipt); !ok || got != want {
		t.Fatalf("回执 = %+v (%v)，期望 %+v", got, ok, want)
	}
}
