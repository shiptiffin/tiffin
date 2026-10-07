// Helpers the auth engine can copy (or import) when filling app emails.

/** WCAG relative luminance of #rrggbb. */
function luminance(hex: string): number {
  const n = parseInt(hex.slice(1), 16);
  const ch = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((c) => {
    const x = c / 255;
    return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * ch[0]! + 0.7152 * ch[1]! + 0.0722 * ch[2]!;
}

const contrast = (a: number, b: number) => (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);

/** White or near-black, whichever reads better on the accent. */
export function accentText(accent: string): "#ffffff" | "#1c1917" {
  const l = luminance(accent);
  return contrast(l, 1) >= contrast(l, luminance("#1c1917")) ? "#ffffff" : "#1c1917";
}

/** The accent, if it is a #rrggbb colour; else the neutral default. */
export function accentOr(accent: string | undefined, fallback = "#1c1917"): string {
  return accent && /^#[0-9a-f]{6}$/i.test(accent) ? accent : fallback;
}
