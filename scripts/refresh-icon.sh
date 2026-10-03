#!/bin/bash
# 刷新 macOS 应用图标缓存

echo "🔄 刷新 macOS 图标缓存..."

# 1. 删除 Wails v3 生成的 macOS 图标，不触碰 Taskfile.yml/Info.plist 源文件
if [ -f build/darwin/icons.icns ]; then
  rm build/darwin/icons.icns
fi
echo "  ✅ 清理 Wails v3 生成的 macOS 图标"

# 2. 清理 macOS 图标缓存
rm -rf ~/Library/Caches/com.apple.iconservices.store
killall Dock 2>/dev/null
killall Finder 2>/dev/null
echo "  ✅ 清理 macOS 图标缓存并重启 Dock"

# 3. 触摸图标文件更新时间戳
touch build/appicon.png
echo "  ✅ 更新图标时间戳"

echo ""
echo "✅ 图标缓存刷新完成！"
echo "💡 请重新运行: wails3 dev -config ./build/config.yml -port 9245"
