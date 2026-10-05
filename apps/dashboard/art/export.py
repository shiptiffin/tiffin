"""Stage 2: frame, scale and export the cut-outs as WebP (and the social card as PNG).

    python export.py <cutdir> <outdir>      (needs pillow and cwebp)

Then copy <outdir>/* into ../src/assets/illustrations/.
"""
import sys, os, subprocess

from PIL import Image

CUT, OUT = sys.argv[1], sys.argv[2]
os.makedirs(OUT, exist_ok=True)


def scale_rgba(im, size):
    # premultiplied resize, so transparent pixels' colour can't bleed into the edge
    return im.convert('RGBa').resize(size, Image.LANCZOS).convert('RGBA')


def window(im, cx, cy, side):
    x0, y0 = int(cx - side / 2), int(cy - side / 2)
    canvas = Image.new('RGBA', (side, side), (0, 0, 0, 0))
    canvas.alpha_composite(im.crop((max(x0, 0), max(y0, 0), min(x0 + side, im.width), min(y0 + side, im.height))), (max(-x0, 0), max(-y0, 0)))
    return canvas


def fit(im, w, h, fill):
    """Trim to the alpha bounds and centre in w x h with the object at most `fill` of either side."""
    im = im.crop(im.getbbox())
    k = min(w * fill / im.width, h * fill / im.height)
    im = scale_rgba(im, (round(im.width * k), round(im.height * k)))
    c = Image.new('RGBA', (w, h), (0, 0, 0, 0))
    c.alpha_composite(im, ((w - im.width) // 2, (h - im.height) // 2))
    return c


def webp(im, name, q=84, aq=90):
    p = f'{OUT}/{name}.png'
    im.save(p)
    subprocess.run(['cwebp', '-quiet', '-q', str(q), '-alpha_q', str(aq), '-m', '6', '-sharp_yuv', p, '-o', f'{OUT}/{name}.webp'], check=True)
    os.remove(p)


def mask_webp(m, name):
    # an alpha-only image: black ink, the band's coverage in alpha (CSS mask-image reads alpha)
    rgba = Image.new('RGBA', m.size, (0, 0, 0, 0)); rgba.putalpha(m)
    p = f'{OUT}/{name}.png'; rgba.save(p)
    subprocess.run(['cwebp', '-quiet', '-q', '70', '-alpha_q', '70', '-m', '6', '-exact', p, '-o', f'{OUT}/{name}.webp'], check=True)
    os.remove(p)


# --- the mascot: every state shares one registration (same window on the same 1254 canvas), so states cross-fade in place
MASCOT_SIDE, MASCOT_PX = 1100, 800
MCX, MCY = 627, 590
for name in ['mascot-base', 'mascot-live', 'mascot-idle', 'mascot-deploying', 'mascot-failed', 'mascot-preview', 'mascot-night', 'tin-plain']:
    im = Image.open(f'{CUT}/{name}.png')
    w = scale_rgba(window(im, MCX, MCY, MASCOT_SIDE), (MASCOT_PX, MASCOT_PX))
    webp(w, name)
    band = f'{CUT}/{name}-band.png'
    if os.path.exists(band) and name != 'mascot-night':
        m = Image.open(band).convert('L')
        mm = Image.new('RGBA', m.size, (0, 0, 0, 0)); mm.putalpha(m)
        mm = scale_rgba(window(mm, MCX, MCY, MASCOT_SIDE), (MASCOT_PX // 2, MASCOT_PX // 2))  # masks at half size: the band's edge sits under an outline
        mask_webp(mm.split()[3], name + '-band')

# --- starters: 640x400 (shown at 320x200)
for name in ['starter-static', 'starter-api', 'starter-next']:
    webp(fit(Image.open(f'{CUT}/{name}.png'), 640, 400, 0.86), name)

# --- empty states
webp(fit(Image.open(f'{CUT}/empty-projects.png'), 640, 320, 0.9), 'empty-projects')
webp(fit(Image.open(f'{CUT}/empty-backups.png'), 480, 280, 0.88), 'empty-backups')
webp(fit(Image.open(f'{CUT}/empty-inbox.png'), 400, 400, 0.8), 'empty-inbox')

# --- social card: 1200x630 paper with grain, the cart on the right ~45 %, the left empty for a headline
card = Image.new('RGBA', (1200, 630), (243, 237, 225, 255))  # the art's paper, flat (grain stays in the drawing; keeps the PNG small)
cart = Image.open(f'{CUT}/cart.png'); cart = cart.crop(cart.getbbox())
k = 600 / cart.width
cart = scale_rgba(cart, (600, round(cart.height * k)))
card.alpha_composite(cart, (1200 - 600 - 56, (630 - cart.height) // 2 + 12))
card.convert('RGB').quantize(colors=96, method=Image.Quantize.MEDIANCUT, dither=Image.Dither.FLOYDSTEINBERG).save(f'{OUT}/og-card.png', optimize=True)
