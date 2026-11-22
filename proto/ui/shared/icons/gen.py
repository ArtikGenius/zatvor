#!/usr/bin/env python3
"""Render the small iris (spec mockups, viewBox 24) to PNG with Pillow.

Run from this directory: python3 gen.py. Outputs are committed; CI does not
run this. Lines: the six blades of the iris path, inside a circle r=10.
"""
from PIL import Image, ImageDraw

SEGS = [((12.0, 2.0), (21.2, 15.9)), ((20.7, 7.0), (13.2, 21.9)),
        ((20.7, 17.0), (4.0, 18.0)), ((12.0, 22.0), (2.8, 8.1)),
        ((3.3, 17.0), (10.8, 2.1)), ((3.3, 7.0), (20.0, 6.0))]


def iris(size, color, bg=None, circle_w=1.6, blade_w=1.3, pad=0.0):
    ss = 8  # supersampling
    big = size * ss
    img = Image.new("RGBA", (big, big), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    if bg is not None:
        r = big * 0.22
        d.rounded_rectangle([0, 0, big - 1, big - 1], radius=r, fill=bg)
    k = big * (1 - 2 * pad) / 24.0
    off = big * pad

    def p(x, y):
        return (off + x * k, off + y * k)

    cw = max(1, round(circle_w * k))
    c0, c1 = p(2, 2), p(22, 22)
    d.ellipse([c0[0], c0[1], c1[0], c1[1]], outline=color, width=cw)
    bw = max(1, round(blade_w * k))
    blades = Image.new("RGBA", (big, big), (0, 0, 0, 0))
    bd = ImageDraw.Draw(blades)
    for a, b in SEGS:
        bd.line([p(*a), p(*b)], fill=color, width=bw)
    mask = Image.new("L", (big, big), 0)
    ImageDraw.Draw(mask).ellipse([c0[0], c0[1], c1[0], c1[1]], fill=255)
    img.paste(blades, (0, 0), Image.composite(blades, Image.new("RGBA", (big, big)), mask).split()[3])
    return img.resize((size, size), Image.LANCZOS)


ACCENT = (157, 180, 255, 255)  # #9DB4FF
if __name__ == "__main__":
    # Windows tray (colour, readable on dark and light taskbars at 16/32 px).
    iris(32, ACCENT, circle_w=2.0, blade_w=1.7).save("tray-32.png")
    # macOS menu bar template image: black + alpha, 22 pt @2x.
    iris(44, (0, 0, 0, 255), circle_w=1.8, blade_w=1.5).save("tray-template-44.png")
    # App icon source for `tauri icon` (bundle icons) and Wails.
    iris(1024, ACCENT, bg=(18, 27, 40, 255), pad=0.12).save("app-1024.png")
