import os
from PIL import Image, ImageDraw, ImageFilter
import numpy as np

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PKG = os.path.join(ROOT, "packaging", "fnrelay")
UI = os.path.join(PKG, "app", "ui", "images")
os.makedirs(UI, exist_ok=True)

S = 1024


def lerp(a, b, t):
    return tuple(int(a[i] + (b[i] - a[i]) * t) for i in range(3))


def gradient():
    yy, xx = np.mgrid[0:S, 0:S].astype(np.float32)
    t = np.clip((xx / S) * 0.62 + (yy / S) * 0.38, 0, 1)
    stops = [(0.0, (56, 189, 248)), (0.52, (99, 102, 241)), (1.0, (168, 85, 247))]
    arr = np.zeros((S, S, 3), dtype=np.float32)
    for i in range(len(stops) - 1):
        p0, c0 = stops[i]
        p1, c1 = stops[i + 1]
        mask = (t >= p0) & (t <= p1)
        local = (t - p0) / max(1e-6, (p1 - p0))
        for ch in range(3):
            arr[..., ch] = np.where(mask, c0[ch] + (c1[ch] - c0[ch]) * local, arr[..., ch])
    # soft diagonal highlight
    hl = np.clip(1.0 - (np.sqrt((xx - 260) ** 2 + (yy - 200) ** 2) / 780.0), 0, 1) ** 2
    for ch in range(3):
        arr[..., ch] = np.clip(arr[..., ch] + hl * 34, 0, 255)
    rgba = np.zeros((S, S, 4), dtype=np.uint8)
    rgba[..., :3] = arr.astype(np.uint8)
    rgba[..., 3] = 255
    return Image.fromarray(rgba, "RGBA")


def rounded_mask(radius=236):
    m = Image.new("L", (S, S), 0)
    d = ImageDraw.Draw(m)
    d.rounded_rectangle([0, 0, S - 1, S - 1], radius=radius, fill=255)
    return m


def arrows():
    layer = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    white = (255, 255, 255, 250)
    # soft shadow
    shadow = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    sd = ImageDraw.Draw(shadow)
    w = 58
    sd.line([(272, 372), (704, 372)], fill=(6, 18, 38, 110), width=w)
    sd.line([(752, 652), (320, 652)], fill=(6, 18, 38, 110), width=w)
    sd.polygon([(806, 372), (704, 300), (704, 444)], fill=(6, 18, 38, 110))
    sd.polygon([(218, 652), (320, 580), (320, 724)], fill=(6, 18, 38, 110))
    shadow = shadow.filter(ImageFilter.GaussianBlur(16))
    layer = Image.alpha_composite(layer, shadow)
    d = ImageDraw.Draw(layer)

    # top arrow ->
    d.line([(280, 372), (700, 372)], fill=white, width=w)
    d.ellipse([280 - w / 2, 372 - w / 2, 280 + w / 2, 372 + w / 2], fill=white)
    d.polygon([(806, 372), (704, 302), (704, 442)], fill=white)
    # bottom arrow <-
    d.line([(744, 652), (324, 652)], fill=white, width=w)
    d.ellipse([744 - w / 2, 652 - w / 2, 744 + w / 2, 652 + w / 2], fill=white)
    d.polygon([(218, 652), (320, 582), (320, 722)], fill=white)
    # accent dots
    d.ellipse([474, 590, 550, 666], fill=(125, 211, 252, 230))
    return layer


def build():
    img = gradient()
    img.putalpha(rounded_mask())
    img = Image.alpha_composite(img, arrows())
    d = ImageDraw.Draw(img)
    d.rounded_rectangle([10, 10, S - 11, S - 11], radius=228, outline=(255, 255, 255, 46), width=6)
    # 256
    icon256 = img.resize((256, 256), Image.LANCZOS)
    icon256.save(os.path.join(PKG, "ICON_256.PNG"))
    icon256.save(os.path.join(UI, "icon_256.png"))
    # 64
    icon64 = img.resize((64, 64), Image.LANCZOS)
    icon64.save(os.path.join(PKG, "ICON.PNG"))
    icon64.save(os.path.join(UI, "icon_64.png"))
    print("icons written")


if __name__ == "__main__":
    build()
