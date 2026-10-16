package tray

import "testing"

func TestCodexSearchMenu_OrderIconAndModelWarning(t *testing.T) {
	s := runningSnapshot()
	s.CodexModels = true
	s.CodexBindings = []Binding{
		{ID: 1, Kind: "model", Name: "a-model", Enabled: true},
		{ID: 2, Kind: "search", Name: "联网搜索", Enabled: true},
	}
	rows := codexBindings(s)
	if len(rows) != 2 || rows[0].Icon != iconWebTools || rows[1].Icon != iconModel {
		t.Fatalf("%+v", rows)
	}
	assertProviderRows(t, Build(s), "Codex  模型路由", []string{"联网搜索", "a-model"})
	s.CodexBindings = s.CodexBindings[1:]
	rows = codexBindings(s)
	if len(rows) != 2 || rows[1].Label != "尚无启用的模型绑定，未匹配的模型将返回未分配错误" || rows[1].Icon != iconAlert {
		t.Fatalf("只有搜索仍要显示模型警告: %+v", rows)
	}
	s.CodexModels = false
	if hasItem(Build(s), "联网搜索") {
		t.Fatal("模型开关关闭时托盘不显示搜索映射")
	}
}
