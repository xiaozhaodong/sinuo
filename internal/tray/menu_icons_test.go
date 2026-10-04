package tray

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

// iconSnapshots 覆盖菜单的各个分支：停止、全局端点、模型路由、两个入口的运行 / 停用 / 监听失败 / 只读、读取失败。
func iconSnapshots() map[string]Snapshot {
	stopped := runningSnapshot()
	stopped.Running = false
	global := runningSnapshot()
	global.ClaudeMode, global.ClaudeTarget = "manual_fixed", "relay"
	global.EntryEnabled, global.EntryRunning, global.EntryMode, global.EntryPort = true, true, "subscription", 18088
	global.CodexEntryState, global.CodexEntryMode, global.CodexEntryPort = CodexEntryReady, "subscription", 18089
	models := runningSnapshot()
	models.ClaudeModels, models.CodexModels = true, true
	models.ClaudeBindings = []Binding{{ID: 1, Name: "联网工具", Kind: "web_tools", Enabled: true, Endpoints: []Endpoint{{Name: "web"}}},
		{ID: 2, Name: "sonnet", Enabled: true, Endpoints: []Endpoint{{Name: "relay"}}}, {ID: 3, Name: "off"}}
	models.CodexBindings = []Binding{{ID: 4, Name: "gpt", Enabled: true, AccountIDs: []int64{1}}, {ID: 5, Name: "off"}}
	models.EntryEnabled, models.EntryMode, models.EntryListenError = true, "endpoint", "端口被占用"
	models.CodexEntryState, models.CodexEntryMode = CodexEntryStopped, "pool"
	empty := runningSnapshot()
	empty.Endpoints, empty.Accounts = nil, nil
	empty.EntryError = "读取失败"
	failed := runningSnapshot()
	failed.ClaudeError, failed.CodexError = "映射读取失败", "账号读取失败"
	readOnly := runningSnapshot()
	readOnly.CodexEntryState, readOnly.CodexEntryLabel = CodexEntryUnavailable, "启动失败"
	return map[string]Snapshot{"stopped": stopped, "global": global, "models": models, "empty": empty, "failed": failed, "readOnly": readOnly}
}

func walkItems(items []Item, visit func(Item)) {
	for _, item := range items {
		visit(item)
		walkItems(item.Children, visit)
	}
}

// macOS 27 不给无图标的行留图标位，同一级菜单混用会让文字左边缘参差，因此每级要么全有、要么全无。
func assertUniformIcons(t *testing.T, name string, items []Item) {
	t.Helper()
	with, without := 0, 0
	for _, item := range items {
		if item.Separator {
			continue
		}
		if item.Icon != "" {
			with++
		} else {
			without++
		}
		assertUniformIcons(t, name, item.Children)
	}
	if with > 0 && without > 0 {
		t.Fatalf("%s: 同一级菜单混用有图标与无图标的项: %+v", name, items)
	}
}

func TestMenuIcons_ReferencedIconsHaveAssets(t *testing.T) {
	for name, s := range iconSnapshots() {
		items := Build(s)
		if items[0].Icon == "" {
			t.Fatalf("%s: 顶层菜单应带图标", name)
		}
		assertUniformIcons(t, name, items)
		walkItems(items, func(item Item) {
			if item.Icon != "" && MenuIcon(item.Icon) == nil {
				t.Fatalf("%s: 菜单项 %q 引用的图标 %q 没有资源", name, item.Label, item.Icon)
			}
		})
	}
}

func TestMenuIcons_TargetChoicesStayPlain(t *testing.T) {
	for name, s := range iconSnapshots() {
		walkItems(Build(s), func(item Item) {
			if item.Label != "优选到" && item.Label != "固定到" {
				return
			}
			if item.Icon == "" {
				t.Fatalf("%s: %q 缺少图标", name, item.Label)
			}
			for _, target := range item.Children {
				if target.Icon != "" {
					t.Fatalf("%s: 端点 / 账号目标项不带图标，状态由勾选表达: %+v", name, target)
				}
			}
		})
	}
}

func TestMenuIcons_TemplateAssets(t *testing.T) {
	if len(menuIcons) == 0 {
		t.Fatal("没有菜单图标资源")
	}
	for name, data := range menuIcons {
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil || img.Bounds().Dx() != 32 || img.Bounds().Dy() != 32 {
			t.Fatalf("%s: 菜单图标应为 32px PNG: %v", name, err)
		}
		visible := 0
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				r, g, b, a := img.At(x, y).RGBA()
				if r != 0 || g != 0 || b != 0 {
					t.Fatalf("%s: 模板图标只能通过 alpha 表达图形", name)
				}
				if a > 0 {
					visible++
				}
			}
		}
		if visible == 0 || visible == 32*32 {
			t.Fatalf("%s: 图标为空或失去透明背景", name)
		}
		// NSImage 按 pHYs 换算点尺寸：144dpi（5669 像素/米）才是 16pt，缺失会按 72dpi 显示成 32pt
		at := bytes.Index(data, []byte("pHYs"))
		if at < 0 || binary.BigEndian.Uint32(data[at+4:]) != 5669 || data[at+12] != 1 {
			t.Fatalf("%s: 菜单图标缺少 144dpi 的 pHYs", name)
		}
	}
}
