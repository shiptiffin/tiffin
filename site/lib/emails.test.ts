// Every email the website sends (emails/samples.ts): it renders to HTML and
// plain text with every value filled in, escapes what it shows, keeps to the
// mail-client rules (600px, Outlook table, alt text, absolute https pictures),
// and its colours are the site's (app/globals.css), the same as the box's
// own emails (packages/emails).
import { describe, expect, test } from "bun:test";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { dark as boxDark, light as boxLight } from "../../packages/emails/src/ui/brand";
import { mails } from "./cloud/emails";
import { samples } from "../emails/samples";
import { ASSETS, dark, light } from "../emails/theme";

describe("every email", () => {
  for (const s of samples) {
    test(`${s.id}: renders HTML and text, every value filled in`, async () => {
      const m = await s.build();
      expect(m.html).toBeDefined();
      for (const part of [m.subject, m.text, m.html!]) {
        expect(part.length).toBeGreaterThan(0);
        expect(part).not.toMatch(/undefined|NaN|\[object Object\]|%%|Invalid Date/);
      }
      expect(m.subject).not.toMatch(/[\r\n]/);
      expect(m.text).not.toMatch(/\n\n\n/);
      expect(m.text).toContain("hello@shiptiffin.com");
      expect(m.text).not.toMatch(/<\/?(table|td|div|span|a|p)\b/);
      const h = m.html!;
      expect(h).toContain('<html dir="ltr" lang="en">');
      expect(h).toContain('<!--[if mso]><table role="presentation" align="center" width="600"');
      expect(h).toContain("max-width:600px");
      expect(h).toContain("prefers-color-scheme:dark");
      expect(h).toMatch(/<div style="display:none[^"]*" data-skip-in-text="true">\S/); // the preheader
      expect(h).not.toMatch(/<img(?![^>]*alt=)/);
      for (const src of h.matchAll(/<img[^>]*src="([^"]+)"/g)) expect(src[1]).toStartWith(`${ASSETS}/`);
      expect(Buffer.byteLength(h)).toBeLessThan(40 * 1024);
      expect(h).not.toContain("<script");
    });
  }
});

describe("your box is ready", () => {
  test("the button goes through the account, never with the box's sign-in code", async () => {
    const m = await mails.ready({ email: "sam@example.com", name: "shop", id: "box_1" });
    expect(m.subject).toBe("Your box shop is ready");
    for (const part of [m.html!, m.text]) {
      expect(part).toContain("https://shiptiffin.com/api/cloud/boxes/box_1/open");
      expect(part).not.toMatch(/tfl_|\/login#/);
      expect(part).toContain("dashboard.shop.shiptiffin.app");
      expect(part).toContain("https://shiptiffin.com/agent-setup.md");
      expect(part).not.toMatch(/keep (my|your) key/i);
    }
    expect(m.text).toContain("Open your dashboard: https://shiptiffin.com/api/cloud/boxes/box_1/open");
    expect(m.html).toContain(`src="${ASSETS}/box-ready.png"`);
  });
  test("without an id, the button opens the account", async () => {
    const m = await mails.ready({ email: "sam@example.com", name: "shop" });
    expect(m.text).toContain("Open your dashboard: https://shiptiffin.com/account");
  });
});

test("values from outside are escaped in the HTML and left as they are in the text", async () => {
  const evil = `<script>alert("x")</script>&'`;
  const box = { email: "sam@example.com", name: "shop" };
  for (const m of [await mails.attention(box, evil), await mails.killed(box, evil), await mails.setup_failed(box, evil)]) {
    expect(m.html).not.toContain("<script>");
    expect(m.html).toContain("&lt;script&gt;");
    expect(m.text).toContain(evil);
  }
});

test("the pictures and fonts the emails load are in public/email", () => {
  for (const f of ["mark.png", "box-ready.png", "newsreader.woff2", "instrument-sans.woff2"]) {
    expect(existsSync(join(import.meta.dir, "..", "public/email", f))).toBe(true);
  }
});

// ---- colours ---------------------------------------------------------------

const css = readFileSync(join(import.meta.dir, "..", "app/globals.css"), "utf8");
const blocks = {
  light: css.slice(css.indexOf(":root {"), css.indexOf("@media (prefers-color-scheme: dark)")),
  dark: css.slice(css.indexOf("@media (prefers-color-scheme: dark)"), css.indexOf("\n}\n", css.indexOf("@media (prefers-color-scheme: dark)"))),
};

/** An opaque oklch() token, as #rrggbb (CSS Color 4: OKLab to linear sRGB, gamut-clipped). */
function tokenHex(theme: "light" | "dark", name: string): string {
  const m = blocks[theme].match(new RegExp(`\\n\\s*${name}:\\s*oklch\\(([\\d.]+) ([\\d.]+) ([\\d.]+)\\);`));
  if (!m) throw new Error(`${name} is not an opaque oklch() in the ${theme} block of globals.css`);
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
        const v = Math.min(1, Math.max(0, x));
        const g = v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055;
        return Math.round(g * 255).toString(16).padStart(2, "0");
      })
      .join("")
  );
}

const fromSite: Record<"light" | "dark", [keyof typeof light, string][]> = {
  light: [
    ["bg", "--paper"], ["card", "--paper-raised"], ["well", "--paper-sunk"], ["ink", "--ink"], ["ink2", "--ink-2"], ["ink3", "--ink-3"],
    ["accent", "--brass"], ["onAccent", "--on-brass"], ["link", "--brass-ink"], ["danger", "--danger"],
  ],
  // ink-2 and ink-3 stay a little lighter than the site's in dark mode, for small text on the card.
  dark: [["bg", "--paper"], ["card", "--paper-raised"], ["well", "--paper-sunk"], ["ink", "--ink"], ["accent", "--brass"], ["onAccent", "--on-brass"], ["link", "--brass-ink"], ["danger", "--danger"]],
};

describe("email colours are the site's", () => {
  for (const theme of ["light", "dark"] as const) {
    for (const [token, name] of fromSite[theme]) {
      test(`${theme} ${token} = ${name}`, () => {
        expect((theme === "light" ? light : dark)[token]).toBe(tokenHex(theme, name));
      });
    }
  }
  test("the box's own emails use the same palette", () => {
    expect<Record<string, string>>(boxLight.box).toEqual(light);
    expect<Record<string, string>>(boxDark.box).toEqual(dark);
  });
});
