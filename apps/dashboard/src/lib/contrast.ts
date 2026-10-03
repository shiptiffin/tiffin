// WCAG 2 contrast for OKLCH tokens, measured from the live CSS (the /_kit
// page uses it to print the contrast table for both themes).

type RGB = [number, number, number];

function oklchToLinear(L: number, C: number, h: number): RGB {
  const hr = (h * Math.PI) / 180;
  const a = C * Math.cos(hr);
  const b = C * Math.sin(hr);
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  const clamp = (x: number) => Math.min(1, Math.max(0, x));
  return [
    clamp(4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s),
    clamp(-1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s),
    clamp(-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s),
  ];
}
const enc = (x: number) => (x <= 0.0031308 ? 12.92 * x : 1.055 * x ** (1 / 2.4) - 0.055);
const dec = (x: number) => (x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4);

export function parseOklch(v: string): { rgb: RGB; alpha: number } | null {
  const m = v.trim().match(/^oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*([\d.]+))?\s*\)$/);
  if (!m) return null;
  return { rgb: oklchToLinear(+m[1], +m[2], +m[3]), alpha: m[4] ? +m[4] : 1 };
}

const blend = (fg: RGB, a: number, bg: RGB): RGB => fg.map((f, i) => dec(enc(f) * a + enc(bg[i]) * (1 - a))) as RGB;
const lum = (c: RGB) => 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];

/** Contrast ratio of `fg` on `bg`; translucent colours are laid over `ground` first. */
export function contrast(fg: string, bg: string, ground: string): number | null {
  const g = parseOklch(ground);
  const b = parseOklch(bg);
  const f = parseOklch(fg);
  if (!g || !b || !f) return null;
  const bb = b.alpha < 1 ? blend(b.rgb, b.alpha, g.rgb) : b.rgb;
  const ff = f.alpha < 1 ? blend(f.rgb, f.alpha, bb) : f.rgb;
  const [hi, lo] = [lum(ff), lum(bb)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}
