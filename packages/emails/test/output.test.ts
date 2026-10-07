// The generated app emails (packages/auth-engine/src/templates.gen.ts): every
// placeholder is filled, values are escaped for where they land, and the
// committed output matches the React sources.
import { describe, expect, test } from "bun:test";
import { readdirSync } from "node:fs";
import { join } from "node:path";
import * as gen from "../../auth-engine/src/templates.gen";
import { accentText } from "../src/accent";

const dir = join(import.meta.dir, "..", "src/emails");
const specs = await Promise.all(
  readdirSync(dir)
    .filter((f) => f.endsWith(".tsx"))
    .map(async (f) => (await import(join(dir, f))).spec),
);
const app = specs.filter((s) => s.family === "app");
const fn = (id: string) => gen.appEmails[id as keyof typeof gen.appEmails] as (v: Record<string, unknown>) => gen.Email;

describe("every app email", () => {
  test("has a generated function", () => {
    expect(Object.keys(gen.appEmails).sort()).toEqual(app.map((s) => s.id).sort());
  });
  for (const s of app) {
    test(`${s.id}: fills every placeholder and stays small`, () => {
      const e = fn(s.id)(s.preview);
      for (const part of [e.subject, e.html, e.text]) {
        expect(part.length).toBeGreaterThan(0);
        expect(part).not.toContain("%%");
        expect(part).not.toContain("{{");
        expect(part).not.toContain("undefined");
      }
      expect(e.subject).not.toMatch(/[\r\n]/);
      expect(e.text).not.toMatch(/\n\n\n/);
      expect(Buffer.byteLength(e.html)).toBeLessThan(30 * 1024);
      expect(e.html).toContain('<html dir="ltr" lang="en">');
      expect(e.html).toContain("<!--[if mso]>");
      expect(e.html).not.toMatch(/<img(?![^>]*alt=)/);
      expect(e.html).not.toMatch(/ShipTiffin|Tiffin/); // the app's brand only
    });
    test(`${s.id}: escapes hostile values`, () => {
      const evil = `<script>alert("x")</script>&'`;
      const v: Record<string, unknown> = { ...s.preview };
      for (const [k, spec] of Object.entries(s.vars as Record<string, { kind: string }>)) if (spec.kind === "text") v[k] = evil;
      const e = fn(s.id)(v);
      expect(e.html).not.toContain("<script>");
      expect(e.html).toContain("&lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt;&amp;&#39;");
      expect(e.text).toContain(evil); // plain text is not HTML
      expect(e.subject).not.toMatch(/[\r\n]/);
    });
  }
});

describe("links and colours", () => {
  const base = app.find((s) => s.id === "magic-link")!.preview;
  test("refuses javascript: and data: links", () => {
    expect(() => gen.magicLink({ ...base, url: "javascript:alert(1)" })).toThrow(/refusing link/);
    expect(() => gen.magicLink({ ...base, url: " data:text/html,x" })).toThrow(/refusing link/);
    expect(() => gen.magicLink({ ...base, logoUrl: "javascript:x" })).toThrow(/refusing link/);
  });
  test("escapes quotes inside a link", () => {
    const e = gen.magicLink({ ...base, url: `https://a.example/?q="><script>` });
    expect(e.html).toContain('href="https://a.example/?q=&quot;&gt;&lt;script&gt;"');
  });
  test("refuses a colour that could break out of the style", () => {
    expect(() => gen.magicLink({ ...base, accent: "red;background:url(x)" })).toThrow(/refusing colour/);
    expect(gen.magicLink({ ...base, accent: "#AbCdEf" }).html).toContain("background-color:#AbCdEf");
  });
  test("leaves out the logo and site when empty", () => {
    const e = gen.magicLink({ ...base, logoUrl: "", site: "", siteLabel: "" });
    expect(e.html).not.toContain("<img");
    expect(e.html).not.toContain("larder.app\"");
    const withAll = gen.magicLink({ ...base, logoUrl: "https://larder.app/logo.png" });
    expect(withAll.html).toContain('src="https://larder.app/logo.png"');
  });
  test("an unknown otp purpose is refused", () => {
    const otp = app.find((s) => s.id === "otp")!.preview;
    expect(() => gen.otp({ ...otp, purpose: "nope" as never })).toThrow();
    for (const purpose of ["sign-in", "email-verification", "forget-password", "change-email"] as const) {
      expect(gen.otp({ ...otp, purpose }).html).toContain(otp.code);
    }
  });
});

describe("accent text", () => {
  test("picks the readable colour", () => {
    expect(accentText("#1c1917")).toBe("#ffffff");
    expect(accentText("#f2b036")).toBe("#25170c"); // brass: the dashboard's text on brass
    expect(accentText("#2f6b4f")).toBe("#ffffff");
    expect(accentText("#ffe14d")).toBe("#25170c");
  });
});

test("committed output matches the sources (bun run build)", async () => {
  const p = Bun.spawnSync(["bun", "scripts/build.tsx", "--check"], { cwd: join(import.meta.dir, "..") });
  expect(p.stderr.toString()).toBe("");
  expect(p.exitCode).toBe(0);
});
