// The email colours that come from the dashboard (brand.ts `fromDashboard`)
// must be exactly what apps/dashboard/src/styles/tokens.css says, converted
// from oklch to hex. Change the dashboard's brass and this fails until the
// emails follow.
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import * as gen from "../../auth-engine/src/templates.gen";
import { accentText } from "../src/accent";
import { brass, dark, fromDashboard, light } from "../src/ui/brand";

const css = readFileSync(join(import.meta.dir, "../../../apps/dashboard/src/styles/tokens.css"), "utf8");
const blocks = {
  light: css.slice(css.indexOf(':root,\n[data-theme="light"]'), css.indexOf("@media (prefers-color-scheme: dark)")),
  dark: css.slice(css.indexOf(':root[data-theme="dark"]'), css.indexOf("/* Type, radii and motion")),
};

/** An opaque oklch() token of a theme, as #rrggbb (CSS Color 4: OKLab to linear sRGB, gamut-clipped). */
function tokenHex(theme: "light" | "dark", name: string): string {
  const m = blocks[theme].match(new RegExp(`\\n\\s*${name}:\\s*oklch\\(([\\d.]+) ([\\d.]+) ([\\d.]+)\\);`));
  if (!m) throw new Error(`${name} is not an opaque oklch() in the ${theme} block of tokens.css`);
  const [L, C, H] = [Number(m[1]), Number(m[2]), (Number(m[3]) * Math.PI) / 180];
  const a = C * Math.cos(H), b = C * Math.sin(H);
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const mm = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  const rgb = [
    4.0767416621 * l - 3.3077115913 * mm + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * mm - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * mm + 1.707614701 * s,
  ];
  return (
    "#" +
    rgb
      .map((x) => {
        x = Math.min(1, Math.max(0, x));
        x = x <= 0.0031308 ? 12.92 * x : 1.055 * x ** (1 / 2.4) - 0.055;
        return Math.round(x * 255).toString(16).padStart(2, "0");
      })
      .join("")
  );
}

describe("email colours match the dashboard's tokens.css", () => {
  for (const theme of ["light", "dark"] as const) {
    const palette = theme === "light" ? light : dark;
    for (const { family, token, css: name } of fromDashboard[theme]) {
      test(`${theme} ${family}.${token} = ${name}`, () => {
        expect(palette[family][token]).toBe(tokenHex(theme, name));
      });
    }
  }
  test("brass is the dashboard's primary button in both themes", () => {
    expect<Record<string, string>>(brass.light).toEqual({ accent: tokenHex("light", "--brass"), onAccent: tokenHex("light", "--on-brass") });
    expect<Record<string, string>>(brass.dark).toEqual({ accent: tokenHex("dark", "--brass"), onAccent: tokenHex("dark", "--on-brass") });
  });
  test("both families default to brass", () => {
    expect(light.box.accent).toBe(brass.light.accent);
    expect(light.app.accent).toBe(brass.light.accent);
    expect(light.app.onAccent).toBe(brass.light.onAccent);
  });
  test("the auth engine's default is the same brass", () => {
    expect(gen.BRASS).toBe(brass.light.accent);
    expect(gen.ON_BRASS).toBe(brass.light.onAccent);
    expect(accentText(gen.BRASS)).toBe(gen.ON_BRASS); // the dashboard's text on brass
  });
});
