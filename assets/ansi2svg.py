#!/usr/bin/env python3
"""Render captured ANSI terminal output as an SVG terminal window."""
import html
import re
import sys

BG = "#161b22"
CHROME = "#21262d"
BORDER = "#30363d"
FG = "#e6edf3"
DIM = "#848d97"
BASIC = {31: "#f47067", 32: "#8ddb8c", 33: "#e0b959", 35: "#dcbdfb", 36: "#76cae8"}

FONT = "ui-monospace, 'SF Mono', SFMono-Regular, Menlo, Consolas, 'Liberation Mono', monospace"
FS = 14
CW = FS * 0.602
LH = 21
PAD = 18
BAR = 34

ESC = re.compile(r"\x1b\[([0-9;]*)m")


def xterm256(n):
    """xterm-256 index -> hex color."""
    base = [
        "#000000", "#cd0000", "#00cd00", "#cdcd00", "#0000ee", "#cd00cd",
        "#00cdcd", "#e5e5e5", "#7f7f7f", "#ff0000", "#00ff00", "#ffff00",
        "#5c5cff", "#ff00ff", "#00ffff", "#ffffff",
    ]
    if n < 16:
        return base[n]
    if n < 232:
        n -= 16
        lv = [0, 95, 135, 175, 215, 255]
        r, g, b = lv[n // 36], lv[n // 6 % 6], lv[n % 6]
    else:
        r = g = b = 8 + 10 * (n - 232)
    return f"#{r:02x}{g:02x}{b:02x}"


def parse(line):
    """Split one line into (text, bold, dim, italic, colorhex) runs."""
    runs, pos = [], 0
    boldf = dimf = italf = False
    color = None
    for m in ESC.finditer(line):
        if m.start() > pos:
            runs.append((line[pos:m.start()], boldf, dimf, italf, color))
        codes = [int(c) for c in (m.group(1) or "0").split(";") if c != ""] or [0]
        i = 0
        while i < len(codes):
            c = codes[i]
            if c == 0:
                boldf = dimf = italf = False
                color = None
            elif c == 1:
                boldf = True
            elif c == 2:
                dimf = True
            elif c == 3:
                italf = True
            elif c in BASIC:
                color = BASIC[c]
            elif c == 38 and i + 2 < len(codes) and codes[i + 1] == 5:
                color = xterm256(codes[i + 2])
                i += 2
            i += 1
        pos = m.end()
    if pos < len(line):
        runs.append((line[pos:], boldf, dimf, italf, color))
    return runs


def main():
    raw = open(sys.argv[1], encoding="utf-8").read()
    lines = raw.replace("\r\n", "\n").replace("\r", "\n").split("\n")
    while lines and not lines[0].strip():
        lines.pop(0)
    while lines and not lines[-1].strip():
        lines.pop()

    cols = max(len(ESC.sub("", l)) for l in lines)
    width = round(PAD * 2 + cols * CW)
    height = BAR + PAD * 2 + len(lines) * LH

    out = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" '
        f'viewBox="0 0 {width} {height}" font-family="{FONT}" font-size="{FS}">',
        f'<rect width="{width}" height="{height}" rx="10" fill="{BG}" stroke="{BORDER}"/>',
        f'<path d="M0 {BAR}h{width}" stroke="{BORDER}"/>',
        f'<rect width="{width}" height="{BAR}" rx="10" fill="{CHROME}"/>',
        f'<rect y="{BAR - 10}" width="{width}" height="10" fill="{CHROME}"/>',
        f'<circle cx="22" cy="{BAR / 2}" r="6" fill="#f47067"/>',
        f'<circle cx="42" cy="{BAR / 2}" r="6" fill="#e0b959"/>',
        f'<circle cx="62" cy="{BAR / 2}" r="6" fill="#8ddb8c"/>',
        f'<text x="{width / 2}" y="{BAR / 2 + 4}" text-anchor="middle" fill="{DIM}" '
        f'font-size="12">quickap</text>',
    ]

    y = BAR + PAD + FS
    for line in lines:
        spans = []
        for text, boldf, dimf, italf, color in parse(line):
            if not text:
                continue
            attrs = ""
            if color and dimf:
                attrs = f' fill="{color}" opacity="0.62"'
            elif color:
                attrs = f' fill="{color}"'
            elif dimf:
                attrs = f' fill="{DIM}"'
            else:
                attrs = f' fill="{FG}"'
            if boldf:
                attrs += ' font-weight="bold"'
            if italf:
                attrs += ' font-style="italic"'
            spans.append(f"<tspan{attrs}>{html.escape(text)}</tspan>")
        if spans:
            out.append(f'<text x="{PAD}" y="{y}" xml:space="preserve">{"".join(spans)}</text>')
        y += LH

    out.append("</svg>")
    print("\n".join(out))


if __name__ == "__main__":
    main()
