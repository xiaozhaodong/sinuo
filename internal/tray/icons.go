package tray

import _ "embed"

//go:embed assets/normal-template.png
var NormalTemplate []byte

//go:embed assets/normal-color.png
var NormalColor []byte

// IconSetter 隔离平台图标设置。后续动画只需向同一入口传递预生成帧。
type IconSetter interface {
	SetFrame(png []byte, template bool)
}

func SetNormalIcon(setter IconSetter, mac bool) {
	if mac {
		setter.SetFrame(NormalTemplate, true)
		return
	}
	setter.SetFrame(NormalColor, false)
}
