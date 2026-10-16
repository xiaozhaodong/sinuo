package main

import (
	"testing"
	"time"

	"sinuo/internal/tray"
)

func TestTray_CodexSearchOnlyStillConfirmsModelRouting(t *testing.T) {
	fake := &trayModelRoutingFake{}
	fake.codex = []CodexModelRouteBindingInfo{{Kind: "search", Enabled: true}}
	question, err := trayModelRoutingEnableConfirm(fake, "codex")
	if err != nil || question.Title != "开启 Codex 模型路由" {
		t.Fatalf("%+v %v", question, err)
	}
	fake.codex = append(fake.codex, CodexModelRouteBindingInfo{Kind: "model", Enabled: true})
	question, err = trayModelRoutingEnableConfirm(fake, "codex")
	if err != nil || question.Title != "" {
		t.Fatalf("启用模型后不应确认: %+v %v", question, err)
	}
}

func TestTray_CodexSearchPatchAndReceiptPreserveKind(t *testing.T) {
	fake := newTrayReceiptAPIFake()
	fake.codex = []CodexModelRouteBindingInfo{{AssignmentID: 9, Kind: "search", Model: "server_tool:search", AccountIDs: []int64{2}, Enabled: true}}
	if err := updateTrayCodexBinding(fake, tray.Action{ID: 9, Mode: "manual_fixed", Target: "2"}); err != nil {
		t.Fatal(err)
	}
	if fake.codexInput.Kind != "search" || fake.codexInput.RouteTargetID != 2 {
		t.Fatalf("%+v", fake.codexInput)
	}
	if subject := trayReceiptCodexSubject(fake, 9); subject != "联网搜索" {
		t.Fatal(subject)
	}
}

func TestTray_CodexSearchSnapshotUsesDisplayName(t *testing.T) {
	app := newCodexSearchRoutingApp(t)
	if _, err := app.CreateCodexModelRouteBinding(CodexModelRouteBindingInput{Kind: "search", AccountIDs: []int64{1}, RouteMode: "auto", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	snap := tray.Snapshot{CodexModels: true, Now: time.Now()}
	(&appTray{app: app}).readCodexRoute(&snap)
	if snap.CodexError != "" || len(snap.CodexBindings) != 1 || snap.CodexBindings[0].Kind != "search" || snap.CodexBindings[0].Name != "联网搜索" {
		t.Fatalf("%+v", snap)
	}
	snap = tray.Snapshot{Now: time.Now()}
	(&appTray{app: app}).readCodexRoute(&snap)
	if len(snap.CodexBindings) != 0 {
		t.Fatal("关闭模型路由时托盘不应列映射")
	}
}
