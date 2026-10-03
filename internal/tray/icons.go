package tray

import (
	"embed"
	"fmt"
)

// 图标与流光帧由 scripts/generate-tray-icons.py 从 BrandMark 同一路径生成。
//
//go:embed assets/*.png assets/flow/*.png
var assets embed.FS

// FlowFrameCount 是流光一个周期的帧数，与生成脚本的 FLOW_FRAMES 一致。
const FlowFrameCount = 16

var (
	NormalTemplate   = mustAsset("normal-template.png")
	DegradedTemplate = mustAsset("degraded-template.png")
	StoppedTemplate  = mustAsset("stopped-template.png")
	NormalColor      = mustAsset("normal-color.png")
	normalFlow       = flowFrames("normal")
	degradedFlow     = flowFrames("degraded")
)

func mustAsset(name string) []byte {
	data, err := assets.ReadFile("assets/" + name)
	if err != nil {
		panic(fmt.Sprintf("托盘图标资源缺失 %s: %v", name, err))
	}
	return data
}

func flowFrames(kind string) [][]byte {
	frames := make([][]byte, FlowFrameCount)
	for i := range frames {
		frames[i] = mustAsset(fmt.Sprintf("flow/%s-%02d.png", kind, i))
	}
	return frames
}

// IconSetter 隔离平台图标设置；静态图标与动画帧都经同一入口下发。
type IconSetter interface {
	SetFrame(png []byte, template bool)
}

// IconFrame 返回状态与帧号对应的图标。frame < 0 为静态图标；只有 macOS 用模板图标与动画，
// 其他平台始终是彩色应用图标，状态由悬停提示表达。
func IconFrame(state IconState, frame int, mac bool) (png []byte, template bool) {
	if !mac {
		return NormalColor, false
	}
	if frame >= 0 {
		if state == IconDegraded {
			return degradedFlow[frame%FlowFrameCount], true
		}
		return normalFlow[frame%FlowFrameCount], true
	}
	switch state {
	case IconDegraded:
		return DegradedTemplate, true
	case IconStopped:
		return StoppedTemplate, true
	}
	return NormalTemplate, true
}
