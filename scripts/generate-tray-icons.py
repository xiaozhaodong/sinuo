#!/usr/bin/env python3
"""生成托盘静态资源；默认只预览，--write 写入，--check 检查可重复生成。"""
import argparse
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "internal/tray/assets"
NAMES = ("normal-template.png", "normal-color.png")
# Wails 把托盘图标缩放到菜单栏高度（NSStatusBar.thickness，macOS 为 22pt），
# 画布按 22pt @2x 出图才能 1:1 显示；画布偏小会被放大、字形变大且发虚。
CANVAS = 44
# 字形高 29px（约 14.5pt）：真机确认的观感是 36px 画布、24px 字形被放大到 22pt 后的效果，
# 按 44px 画布 1:1 出图保持同一大小，只是不再被放大发虚。
# 字母 S 比系统符号图标更「满」，与之等高时显得偏大，因此比邻居略小一档。
GLYPH_HEIGHT = 29
STROKE_WIDTH = 3.9


def generate(directory, glyph_height=GLYPH_HEIGHT, stroke_width=STROKE_WIDTH):
    if glyph_height <= 0 or stroke_width <= 0:
        raise ValueError("字形高度和描边宽度必须为正数")
    source = (ROOT / "frontend/src/components/layout/BrandMark.jsx").read_text()
    paths = re.findall(r'<path\s+d="([^"]+)"', source)
    if len(paths) != 1:
        raise RuntimeError("BrandMark 必须包含一条明确的路径，请检查品牌标识变更")
    common = ["-strip", "-define", "png:exclude-chunks=date,time"]
    # 直接渲染 SVG path，避免本机 ImageMagick 的 SVG delegate 把 stroke-only 路径输出为空图。
    drawing = f"stroke-linecap round stroke-linejoin round scale 12,12 path '{paths[0]}'"
    subprocess.run(["magick", "-size", "288x288", "canvas:none", "-fill", "none",
                    "-stroke", "black", "-strokewidth", str(stroke_width), "-draw", drawing,
                    "-trim", "+repage", "-resize", f"x{glyph_height}", "-gravity", "center",
                    "-background", "none", "-extent", f"{CANVAS}x{CANVAS}", *common,
                    "-units", "PixelsPerInch", "-density", "144",
                    "PNG32:" + str(directory / NAMES[0])], check=True)
    subprocess.run(["magick", str(ROOT / "build/appicon.png"), "-resize", "48x48",
                    *common, "PNG32:" + str(directory / NAMES[1])], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--write", action="store_true")
    mode.add_argument("--check", action="store_true")
    parser.add_argument("--glyph-height", type=int, default=GLYPH_HEIGHT, help="模板图标字形高度（像素）")
    parser.add_argument("--stroke-width", type=float, default=STROKE_WIDTH, help="BrandMark 24 单位坐标中的描边宽度")
    args = parser.parse_args()
    for name in NAMES:
        print(f"{'覆盖' if (OUTPUT / name).exists() else '新增'}: {OUTPUT / name}")
    if not (args.write or args.check):
        return
    if not shutil.which("magick"):
        raise RuntimeError("生成图标需要 ImageMagick 的 magick 命令；运行 App 不需要它")
    with tempfile.TemporaryDirectory(prefix="tray-icons-") as temp:
        directory = Path(temp)
        generate(directory, args.glyph_height, args.stroke_width)
        if args.check:
            for name in NAMES:
                if not (OUTPUT / name).exists() or (OUTPUT / name).read_bytes() != (directory / name).read_bytes():
                    raise RuntimeError(f"图标不是当前来源的生成结果: {name}")
            print("图标与生成结果一致")
        else:
            OUTPUT.mkdir(parents=True, exist_ok=True)
            for name in NAMES:
                (OUTPUT / name).write_bytes((directory / name).read_bytes())


if __name__ == "__main__":
    main()
