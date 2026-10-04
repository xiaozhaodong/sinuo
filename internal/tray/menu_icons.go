package tray

import "strings"

// 菜单项图标名即 assets/menu 下的文件名，由 scripts/generate-tray-icons.py 从 lucide 生成（brand 取自 BrandMark）。
// 改动这里时同步脚本中的 MENU_LUCIDE。
const (
	iconStatus    = "activity"
	iconStats     = "chart-no-axes-column"
	iconClaude    = "sparkle"
	iconEntry     = "plug"
	iconSubscribe = "key-round"
	iconEndpoint  = "route"
	iconQuota     = "gauge"
	iconCodex     = "code-xml"
	iconApp       = "brand"
	iconRequests  = "waypoints"
	iconLogs      = "scroll-text"
	iconMenuBar   = "panel-top"
	iconQuit      = "power"
	iconAuto      = "wand-sparkles"
	iconPrefer    = "star"
	iconPin       = "pin"
	iconModel     = "box"
	iconWebTools  = "globe"
	iconManage    = "settings-2"
	iconAlert     = "circle-alert"
	iconInfo      = "info"
	iconEnable    = "circle-play"
	iconDisable   = "circle-stop"
	iconPool      = "user-round"
)

var menuIcons = loadMenuIcons()

func loadMenuIcons() map[string][]byte {
	entries, err := assets.ReadDir("assets/menu")
	if err != nil {
		panic("托盘菜单图标资源缺失: " + err.Error())
	}
	icons := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		icons[strings.TrimSuffix(entry.Name(), ".png")] = mustAsset("menu/" + entry.Name())
	}
	return icons
}

// MenuIcon 返回菜单项图标（32px、144dpi 的黑色透明图，即 16pt @2x）；未知名称返回 nil。
func MenuIcon(name string) []byte { return menuIcons[name] }

func (i Item) withIcon(icon string) Item {
	i.Icon = icon
	return i
}
