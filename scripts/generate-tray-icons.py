#!/usr/bin/env python3
"""生成托盘图标与流光动画帧；默认只预览，--write 写入，--check 检查可重复生成。"""
import argparse
import math
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "internal/tray/assets"
# Wails 把托盘图标缩放到菜单栏高度（NSStatusBar.thickness，macOS 为 22pt），
# 画布按 22pt @2x 出图才能 1:1 显示；画布偏小会被放大、字形变大且发虚。
CANVAS = 44
# 字形高 29px（约 14.5pt）：真机确认的观感是 36px 画布、24px 字形被放大到 22pt 后的效果，
# 按 44px 画布 1:1 出图保持同一大小，只是不再被放大发虚。
# 字母 S 比系统符号图标更「满」，与之等高时显得偏大，因此比邻居略小一档。
GLYPH_HEIGHT = 29
STROKE_WIDTH = 3.9
SUPERSAMPLE = 8  # 先按 8 倍绘制再缩小，边缘抗锯齿更平滑

# 流光（docs/prototypes/tray-icon-animation.html 方案 A）：底色 40%，
# 一段占全长 30% 的高亮沿书写方向（右上 → 左下）流过，每周期 16 帧
FLOW_FRAMES = 16
FLOW_SEGMENT = 0.30
FLOW_BASE_OPACITY = 0.4
# 降级小点与外圈间隙（BrandMark 24 单位坐标）：放在 S 右下角外侧，
# 间隙只削掉下弯末端一点，避免 S 看起来断了一截
DOT = (22.0, 20.6, 2.7)
DOT_GAP_RADIUS = 4.2
# 停止态：空心 S，内部挖掉的描边宽度占外描边的比例
HOLLOW_RATIO = 0.42

STATIC_NAMES = ("normal-template.png", "degraded-template.png", "stopped-template.png", "normal-color.png")

# 菜单项图标：16pt @2x 画布，与前端同一版本的 lucide 图标（ISC 许可）。
# 文件名即 lucide 图标名，与 internal/tray/menu_icons.go 的常量一一对应；
# 只是黑色透明图，macOS 打开菜单时由 App 标记为模板图，深色、高亮、置灰由系统着色。
MENU_CANVAS = 32
LUCIDE_ICONS = ROOT / "frontend/node_modules/lucide-react/dist/esm/icons"
# lucide 默认描边 2 在 16pt 下比菜单文字笔画重，减到 1.75（约 1.17pt）
LUCIDE_STROKE_WIDTH = 1.75
MENU_LUCIDE = ("activity", "chart-no-axes-column", "sparkle", "plug", "key-round", "route", "gauge", "code-xml",
               "waypoints", "scroll-text", "panel-top", "power", "wand-sparkles", "star", "pin", "box", "globe",
               "settings-2", "circle-alert", "info", "circle-play", "circle-stop", "user-round")
# 「打开 AI Switchboard」用 BrandMark：字形高 12pt；描边 2.4 单位约 2.6px，
# 比相邻 lucide 图标（约 2.3px）略重一档，保留品牌字形的分量
MENU_BRAND_GLYPH_HEIGHT = 24
MENU_BRAND_STROKE_WIDTH = 2.4


def flow_names():
    return [f"flow/{kind}-{i:02d}.png" for kind in ("normal", "degraded") for i in range(FLOW_FRAMES)]


def menu_names():
    return ["menu/brand.png"] + [f"menu/{name}.png" for name in MENU_LUCIDE]


def all_names():
    return list(STATIC_NAMES) + flow_names() + menu_names()


def brand_path():
    source = (ROOT / "frontend/src/components/layout/BrandMark.jsx").read_text()
    paths = re.findall(r'<path\s+d="([^"]+)"', source)
    if len(paths) != 1:
        raise RuntimeError("BrandMark 必须包含一条明确的路径，请检查品牌标识变更")
    return paths[0]


def sample_path(d, steps=400):
    """把只含 M / L / C 的路径采样成折线，用于按弧长截取流光段。"""
    tokens = re.findall(r"[A-Za-z]|-?\d+\.?\d*", d)
    points, i, cur = [], 0, (0.0, 0.0)
    while i < len(tokens):
        op = tokens[i]
        i += 1
        if op in ("M", "L"):
            cur = (float(tokens[i]), float(tokens[i + 1]))
            points.append(cur)
            i += 2
        elif op == "C" and points:
            x1, y1, x2, y2, x3, y3 = (float(v) for v in tokens[i:i + 6])
            i += 6
            for s in range(1, steps + 1):
                t = s / steps
                m = 1 - t
                points.append((m ** 3 * cur[0] + 3 * m * m * t * x1 + 3 * m * t * t * x2 + t ** 3 * x3,
                               m ** 3 * cur[1] + 3 * m * m * t * y1 + 3 * m * t * t * y2 + t ** 3 * y3))
            cur = (x3, y3)
        else:
            raise RuntimeError(f"BrandMark 路径含有不支持的指令: {op}")
    return points


class Geometry:
    """BrandMark 24 单位坐标 → 超采样画布像素的变换，所有图标共用，切换状态时字形不跳动。"""

    def __init__(self, points, glyph_height, stroke_width, canvas=CANVAS):
        xs, ys = [p[0] for p in points], [p[1] for p in points]
        half = stroke_width / 2
        left, top = min(xs) - half, min(ys) - half
        width, height = max(xs) + half - left, max(ys) + half - top
        size = canvas * SUPERSAMPLE
        self.scale = glyph_height * SUPERSAMPLE / height
        self.tx = (size - width * self.scale) / 2 - left * self.scale
        self.ty = (size - height * self.scale) / 2 - top * self.scale
        self.stroke_width = stroke_width
        self.prefix = (f"translate {self.tx:.3f},{self.ty:.3f} scale {self.scale:.5f},{self.scale:.5f} "
                       "stroke-linecap round stroke-linejoin round")


def stroke(geo, shape, opacity=1.0, width=None):
    return ["-fill", "none", "-stroke", f"rgba(0,0,0,{opacity})", "-strokewidth", str(width or geo.stroke_width),
            "-draw", f"{geo.prefix} {shape}"]


def polyline(points):
    return "polyline " + " ".join(f"{x:.3f},{y:.3f}" for x, y in points)


def cut(shape_args):
    """从已绘制的图层中挖掉一个形状（DstOut），之后恢复正常叠加。"""
    size = CANVAS * SUPERSAMPLE
    return ["(", "-size", f"{size}x{size}", "canvas:none", *shape_args, ")",
            "-compose", "DstOut", "-composite", "-compose", "Over"]


def degraded_dot(geo):
    x, y, r = DOT
    gap = ["-fill", "black", "-stroke", "none", "-draw", f"{geo.prefix} circle {x},{y} {x + DOT_GAP_RADIUS},{y}"]
    dot = ["-fill", "black", "-stroke", "none", "-draw", f"{geo.prefix} circle {x},{y} {x + r},{y}"]
    return cut(gap) + dot


def flow_segment(points, frame):
    """第 frame 帧高亮段的折线：从起点外进入，在终点外离开。"""
    lengths = [0.0]
    for a, b in zip(points, points[1:]):
        lengths.append(lengths[-1] + math.dist(a, b))
    total, seg = lengths[-1], lengths[-1] * FLOW_SEGMENT
    start = frame / FLOW_FRAMES * (total + seg) - seg
    lo, hi = max(0.0, start), min(total, start + seg)
    if hi <= lo:
        return None

    def at(s):
        for j in range(len(lengths) - 1):
            if lengths[j + 1] >= s:
                r = (s - lengths[j]) / max(lengths[j + 1] - lengths[j], 1e-9)
                (x0, y0), (x1, y1) = points[j], points[j + 1]
                return x0 + (x1 - x0) * r, y0 + (y1 - y0) * r
        return points[-1]

    return [at(lo)] + [p for p, s in zip(points, lengths) if lo < s < hi] + [at(hi)]


def render(directory, name, layers, canvas=CANVAS, keep_density=False):
    """keep_density 保留 144dpi（pHYs）：菜单项图片不经缩放，NSImage 按它把 32px 认作 16pt。"""
    size = canvas * SUPERSAMPLE
    target = directory / name
    target.parent.mkdir(parents=True, exist_ok=True)
    density = ["-define", "png:include-chunk=pHYs"] if keep_density else []
    subprocess.run(["magick", "-size", f"{size}x{size}", "canvas:none", *layers,
                    "-resize", f"{canvas}x{canvas}", "-strip", "-define", "png:exclude-chunks=date,time", *density,
                    "-units", "PixelsPerInch", "-density", "144", "PNG32:" + str(target)], check=True)


def lucide_nodes(name):
    """读取 lucide-react 图标文件中的 __iconNode：[(标签, 属性), ...]。"""
    path = LUCIDE_ICONS / f"{name}.js"
    if not path.exists():
        raise RuntimeError(f"找不到 lucide 图标 {name}，请先在 frontend 目录执行 npm install")
    block = re.search(r"const __iconNode = \[(.*?)\];", path.read_text(), re.S)
    if not block:
        raise RuntimeError(f"无法解析 lucide 图标 {name}")
    nodes = [(tag, dict(re.findall(r'(\w+): "([^"]*)"', attrs)))
             for tag, attrs in re.findall(r'\[\s*"(\w+)",\s*\{(.*?)\}\s*\]', block.group(1), re.S)]
    if not nodes:
        raise RuntimeError(f"lucide 图标 {name} 没有图形节点")
    return nodes


def lucide_primitive(tag, attrs):
    """把 lucide 的 SVG 节点换成 ImageMagick draw 图元，坐标保持 24 单位网格。"""
    a = {k: float(v) for k, v in attrs.items() if k not in ("key", "d", "points", "fill")}
    if tag == "path":
        return f"path '{attrs['d']}'"
    if tag == "circle":
        return f"circle {a['cx']},{a['cy']} {a['cx'] + a['r']},{a['cy']}"
    if tag == "ellipse":
        return f"ellipse {a['cx']},{a['cy']} {a['rx']},{a['ry']} 0,360"
    if tag == "line":
        return f"line {a['x1']},{a['y1']} {a['x2']},{a['y2']}"
    if tag in ("polyline", "polygon"):
        return f"{tag} " + " ".join(re.findall(r"-?[\d.]+,-?[\d.]+", re.sub(r"\s+", ",", attrs["points"].strip())))
    if tag == "rect":
        x0, y0 = a.get("x", 0.0), a.get("y", 0.0)
        x1, y1 = x0 + a["width"], y0 + a["height"]
        rx = a.get("rx", a.get("ry", 0.0))
        if rx:
            return f"roundrectangle {x0},{y0} {x1},{y1} {rx},{a.get('ry', rx)}"
        return f"rectangle {x0},{y0} {x1},{y1}"
    raise RuntimeError(f"lucide 图标含有不支持的节点: {tag}")


def lucide_layers(name):
    scale = MENU_CANVAS * SUPERSAMPLE / 24
    prefix = f"scale {scale:.5f},{scale:.5f} stroke-linecap round stroke-linejoin round"
    layers = []
    for tag, attrs in lucide_nodes(name):
        fill = "black" if attrs.get("fill") == "currentColor" else "none"
        layers += ["-fill", fill, "-stroke", "black", "-strokewidth", str(LUCIDE_STROKE_WIDTH),
                   "-draw", f"{prefix} {lucide_primitive(tag, attrs)}"]
    return layers


def generate_menu(directory, points, glyph):
    geo = Geometry(points, MENU_BRAND_GLYPH_HEIGHT, MENU_BRAND_STROKE_WIDTH, MENU_CANVAS)
    render(directory, "menu/brand.png", stroke(geo, glyph), MENU_CANVAS, keep_density=True)
    for name in MENU_LUCIDE:
        render(directory, f"menu/{name}.png", lucide_layers(name), MENU_CANVAS, keep_density=True)


def generate(directory, glyph_height=GLYPH_HEIGHT, stroke_width=STROKE_WIDTH):
    if glyph_height <= 0 or stroke_width <= 0:
        raise ValueError("字形高度和描边宽度必须为正数")
    d = brand_path()
    points = sample_path(d)
    geo = Geometry(points, glyph_height, stroke_width)
    glyph = f"path '{d}'"
    render(directory, "normal-template.png", stroke(geo, glyph))
    render(directory, "degraded-template.png", stroke(geo, glyph) + degraded_dot(geo))
    hollow = ["-fill", "none", "-stroke", "black", "-strokewidth", str(stroke_width * HOLLOW_RATIO),
              "-draw", f"{geo.prefix} {glyph}"]
    render(directory, "stopped-template.png", stroke(geo, glyph) + cut(hollow))
    for frame in range(FLOW_FRAMES):
        layers = stroke(geo, glyph, FLOW_BASE_OPACITY)
        segment = flow_segment(points, frame)
        if segment:
            layers += stroke(geo, polyline(segment))
        render(directory, f"flow/normal-{frame:02d}.png", layers)
        render(directory, f"flow/degraded-{frame:02d}.png", layers + degraded_dot(geo))
    generate_menu(directory, points, glyph)
    subprocess.run(["magick", str(ROOT / "build/appicon.png"), "-resize", "48x48", "-strip",
                    "-define", "png:exclude-chunks=date,time", "PNG32:" + str(directory / "normal-color.png")],
                   check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--write", action="store_true")
    mode.add_argument("--check", action="store_true")
    parser.add_argument("--glyph-height", type=int, default=GLYPH_HEIGHT, help="模板图标字形高度（像素）")
    parser.add_argument("--stroke-width", type=float, default=STROKE_WIDTH, help="BrandMark 24 单位坐标中的描边宽度")
    args = parser.parse_args()
    names = all_names()
    if not (args.write or args.check):
        for name in names:
            print(f"{'覆盖' if (OUTPUT / name).exists() else '新增'}: {OUTPUT / name}")
        return
    if not shutil.which("magick"):
        raise RuntimeError("生成图标需要 ImageMagick 的 magick 命令；运行 App 不需要它")
    with tempfile.TemporaryDirectory(prefix="tray-icons-") as temp:
        directory = Path(temp)
        generate(directory, args.glyph_height, args.stroke_width)
        if args.check:
            for name in names:
                if not (OUTPUT / name).exists() or (OUTPUT / name).read_bytes() != (directory / name).read_bytes():
                    raise RuntimeError(f"图标不是当前来源的生成结果: {name}")
            print(f"{len(names)} 个图标与生成结果一致")
            return
        for name in names:
            (OUTPUT / name).parent.mkdir(parents=True, exist_ok=True)
            (OUTPUT / name).write_bytes((directory / name).read_bytes())
        print(f"已写入 {len(names)} 个图标")


if __name__ == "__main__":
    main()
