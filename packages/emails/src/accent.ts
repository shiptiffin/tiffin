// The button colour of app emails: the dashboard's brass unless the app sets
// its own (auth.emailAccent), with readable text on it. The auth engine does
// the same with the constants templates.gen.ts carries (BRASS, ON_BRASS).
import { brass } from "./ui/brand";

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

/** The dark text choice: the dashboard's text on brass. */
export const DARK_TEXT = brass.light.onAccent;

/** White or near-black (the dashboard's text on brass), whichever reads better on the accent. */
export function accentText(accent: string): "#ffffff" | typeof DARK_TEXT {
  const l = luminance(accent);
  return contrast(l, 1) >= contrast(l, luminance(DARK_TEXT)) ? "#ffffff" : DARK_TEXT;
}

/** The accent, if it is a #rrggbb colour; else the dashboard's brass. */
export function accentOr(accent: string | undefined, fallback: string = brass.light.accent): string {
  return accent && /^#[0-9a-f]{6}$/i.test(accent) ? accent : fallback;
}
