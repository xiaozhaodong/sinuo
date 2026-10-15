#!/bin/bash
# 更新 wails3 dev 开发模式下的应用图标

PNG_ICON="build/appicon.png"
APP_ICON="bin/Sinuo.app/Contents/Resources/icons.icns"
ICONSET_DIR="build/tmp.iconset"

echo "🎨 更新开发模式应用图标..."

# 1. 创建 iconset 目录
mkdir -p "$ICONSET_DIR"

# 2. 生成多种尺寸的图标（macOS 要求）
sips -z 16 16     "$PNG_ICON" --out "$ICONSET_DIR/icon_16x16.png"
sips -z 32 32     "$PNG_ICON" --out "$ICONSET_DIR/icon_16x16@2x.png"
sips -z 32 32     "$PNG_ICON" --out "$ICONSET_DIR/icon_32x32.png"
sips -z 64 64     "$PNG_ICON" --out "$ICONSET_DIR/icon_32x32@2x.png"
sips -z 128 128   "$PNG_ICON" --out "$ICONSET_DIR/icon_128x128.png"
sips -z 256 256   "$PNG_ICON" --out "$ICONSET_DIR/icon_128x128@2x.png"
sips -z 256 256   "$PNG_ICON" --out "$ICONSET_DIR/icon_256x256.png"
sips -z 512 512   "$PNG_ICON" --out "$ICONSET_DIR/icon_256x256@2x.png"
sips -z 512 512   "$PNG_ICON" --out "$ICONSET_DIR/icon_512x512.png"
sips -z 1024 1024 "$PNG_ICON" --out "$ICONSET_DIR/icon_512x512@2x.png"

# 3. 转换为 .icns
iconutil -c icns "$ICONSET_DIR" -o "$APP_ICON"

# 4. 清理临时文件
rm -rf "$ICONSET_DIR"

# 5. 清理 macOS 图标缓存
rm -rf ~/Library/Caches/com.apple.iconservices.store
killall Dock 2>/dev/null

echo "✅ 图标更新完成！Dock 已刷新"
echo "💡 请重启应用: 先 Ctrl+C 停止 wails3 dev，然后重新运行"
