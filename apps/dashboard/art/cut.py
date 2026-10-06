"""Stage 1: key out the paper, repaint the mascot's ochre band as plain steel, and write
full-resolution RGBA cut-outs plus band masks (the band's coverage, for CSS tinting).

    ART_SRC=<folder with picks/ and raw/> python cut.py <outdir> [name ...]

Needs numpy, scipy, pillow, opencv-python-headless. See ../src/assets/illustrations/README.md.
"""
import sys, json, os
import numpy as np
import cv2
from PIL import Image
from scipy import ndimage as ndi

ART = os.environ['ART_SRC']
OUT = sys.argv[1]
os.makedirs(OUT, exist_ok=True)

# name: (source PNG under ART_SRC, band, keep)
#   band: None (leave the colours alone), 'largest' (the biggest ochre region is the band), or an
#         (x0, y0, x1, y1) box to look in (scenes, where the cart's wood is ochre too)
#   keep: centroids of enclosed paper-coloured pockets that are part of the drawing (a paper tag, rice),
#         not background showing through (inside a handle, between spokes); 'all' keeps every one
JOBS = {
    'mascot-base': ('picks/mascot-base.png', 'largest', []),
    'mascot-live': ('picks/A-live.png', 'largest', []),
    'mascot-idle': ('picks/A-sleepy.png', 'largest', []),
    'mascot-deploying': ('picks/A-busy.png', 'largest', []),
    'mascot-failed': ('picks/A-failed.png', 'largest', []),
    'mascot-preview': ('picks/A-preview.png', 'largest', []),
    'mascot-night': ('raw/statesC/states-sleepy-2.png', 'largest', []),
    'starter-static': ('picks/static.png', None, 'all'),
    'starter-api': ('picks/api.png', None, 'all'),
    'starter-web': ('picks/web-app.png', None, 'all'),
    'empty-projects': ('picks/E-no-projects.png', (650, 380, 840, 500), []),
    'empty-inbox': ('picks/E-empty-inbox.png', 'largest', []),
    'empty-backups': ('picks/D-backup.png', (1000, 300, 1400, 750), [(379, 368), (296, 647), (553, 657)]),
    'cart': ('picks/B-cart.png', (640, 400, 812, 492), []),
}

T_BG = 16.0       # paper distance that still counts as paper
SPECK = 120         # foreground islands smaller than this (px) are paper grain
HOLE_MEAN = 7.5   # enclosed paper-coloured pockets with mean distance under this are background too
GRAPHITE = np.array([46, 42, 38], np.float32)


def border_median(a):
    b = np.concatenate([a[:8].reshape(-1, 3), a[-8:].reshape(-1, 3), a[:, :8].reshape(-1, 3), a[:, -8:].reshape(-1, 3)])
    return np.median(b, 0).astype(np.float32)


def background(a, B, keep):
    d = np.linalg.norm(a - B, axis=2)
    lab, n = ndi.label(d < T_BG)
    edge = set(np.unique(np.concatenate([lab[0], lab[-1], lab[:, 0], lab[:, -1]]))) - {0}
    idx = np.arange(1, n + 1)
    means = ndi.mean(d, lab, idx)
    coms = ndi.center_of_mass(np.ones_like(d), lab, idx)
    bg_labels = []
    for i in idx:
        if i in edge:
            bg_labels.append(i); continue
        if means[i - 1] >= HOLE_MEAN:
            continue
        if keep == 'all':  # every enclosed pocket is drawing (a window's page, a sheet of paper)
            continue
        cy, cx = coms[i - 1]
        if any(abs(cx - kx) < 40 and abs(cy - ky) < 40 for kx, ky in keep):
            continue
        bg_labels.append(i)
    return np.isin(lab, bg_labels), d


def band_region(a, alpha_fg, spec):
    hsv = cv2.cvtColor(np.clip(a, 0, 255).astype(np.uint8), cv2.COLOR_RGB2HSV).astype(np.float32)
    h, s, v = hsv[..., 0] * 2, hsv[..., 1] / 255, hsv[..., 2] / 255
    ochre = (h > 28) & (h < 52) & (s > 0.45) & (v > 0.55) & alpha_fg
    if spec != 'largest':
        x0, y0, x1, y1 = spec
        lim = np.zeros_like(ochre); lim[y0:y1, x0:x1] = True
        ochre &= lim
    ochre = ndi.binary_opening(ochre, iterations=1)
    lab, n = ndi.label(ochre)
    if n == 0:
        return None
    sizes = ndi.sum(ochre, lab, range(1, n + 1))
    return lab == (1 + int(np.argmax(sizes)))


def run(name, src, band, keep):
    im = Image.open(os.path.join(ART, src)).convert('RGB')
    a = np.asarray(im).astype(np.float32)
    B = border_median(a)
    bg, d = background(a, B, keep)
    fg = ~bg
    # paper grain specks are tiny islands: drop foreground islands under SPECK pixels
    lab, n = ndi.label(fg)
    if n:
        sizes = ndi.sum(fg, lab, range(1, n + 1))
        small = np.isin(lab, 1 + np.nonzero(sizes < SPECK)[0])
        fg &= ~small
        bg = ~fg
        info_small = sorted(sizes[(sizes >= SPECK) & (sizes < 2000)].astype(int).tolist())
    else:
        info_small = []
    info = {'name': name, 'src': src, 'size': im.size, 'islands': info_small}

    # --- band: recolour to steel, and keep its coverage as a mask
    mask = None
    plain = None
    if band:
        R = band_region(a, fg, band)
        T = np.median(a[R], 0)
        # steel: the low-saturation mid-grey fill of the tiers
        hsv = cv2.cvtColor(a.astype(np.uint8), cv2.COLOR_RGB2HSV).astype(np.float32)
        steelpx = fg & (hsv[..., 1] < 30) & (hsv[..., 2] > 110) & (hsv[..., 2] < 175)
        S = np.median(a[steelpx], 0)
        zone = ndi.binary_dilation(R, iterations=10)
        filled = ndi.binary_fill_holes(R)
        zone |= filled
        # coverage by chroma: graphite and steel are near-neutral, so a pixel's warm chroma says how much band ink it holds
        chroma = a.max(2) - a.min(2)
        cT = float(T.max() - T.min())
        m = np.clip((chroma - 10) / (cT - 10), 0, 1) * zone
        m[R] = 1.0
        # recolour (keeps the grain: only the ink is swapped)
        a = a + m[..., None] * (S - T)
        # then drop any leftover hue (the band's pale highlight strip would turn bluish): grey at the same lightness, in steel's tint
        W = np.array([0.299, 0.587, 0.114], np.float32)
        grey = (a @ W)[..., None] + (S - float(S @ W))
        a = a + m[..., None] * (grey - a)
        # mask: coverage, with the paler grain specks let through so a tint keeps the riso texture
        lum = a @ np.array([0.299, 0.587, 0.114], np.float32)
        lumS = float(S @ np.array([0.299, 0.587, 0.114], np.float32))
        speck = np.clip((lum - lumS) / 45.0, 0, 1)
        mask = m * (1 - 0.55 * speck)
        # faceless: inpaint the face (the holes inside the band) with steel, and close the mask over it
        face = filled & ~R
        face = ndi.binary_dilation(face, iterations=3) & ndi.binary_erosion(filled, iterations=1)
        if face.any():
            rgb8 = np.clip(a, 0, 255).astype(np.uint8)
            inp = cv2.inpaint(rgb8, face.astype(np.uint8) * 255, 5, cv2.INPAINT_TELEA).astype(np.float32)
            plain = inp
            plainmask = np.maximum(mask, face.astype(np.float32) * 0.97)
        info.update(T=T.round().tolist(), S=S.round().tolist(), band_px=int(R.sum()))

    # --- alpha matte: core foreground is opaque; a thin band around the paper edge is unmixed against the nearest core colour
    core = ndi.binary_erosion(fg, iterations=2)
    edge = ndi.binary_dilation(bg, iterations=3) & ndi.binary_dilation(fg, iterations=3)

    def matte(rgb):
        _, (iy, ix) = ndi.distance_transform_edt(~core, return_indices=True)
        F = rgb[iy, ix]
        FB = F - B
        al = np.einsum('ijk,ijk->ij', rgb - B, FB) / np.maximum(np.einsum('ijk,ijk->ij', FB, FB), 1.0)
        al = np.clip(al, 0, 1)
        alpha = np.where(fg, 1.0, 0.0)
        alpha = np.where(edge, al, alpha)
        alpha[core] = 1.0
        alpha[alpha < 0.05] = 0
        col = np.where(edge[..., None], F, rgb)  # decontaminate the edge: its colour is the object's, the paper goes to alpha
        return np.dstack([np.clip(col, 0, 255), alpha * 255]).astype(np.uint8)

    out = matte(a)
    Image.fromarray(out, 'RGBA').save(f'{OUT}/{name}.png')
    ys, xs = np.nonzero(out[..., 3] > 8)
    info['bbox'] = [int(xs.min()), int(ys.min()), int(xs.max()) + 1, int(ys.max()) + 1]
    if mask is not None:
        Image.fromarray((np.clip(mask, 0, 1) * 255).astype(np.uint8), 'L').save(f'{OUT}/{name}-band.png')
    if plain is not None and name == 'mascot-base':
        Image.fromarray(matte(plain), 'RGBA').save(f'{OUT}/tin-plain.png')
        Image.fromarray((np.clip(plainmask, 0, 1) * 255).astype(np.uint8), 'L').save(f'{OUT}/tin-plain-band.png')
    print(json.dumps(info))


only = sys.argv[2:] or list(JOBS)
for n in only:
    run(n, *JOBS[n])
