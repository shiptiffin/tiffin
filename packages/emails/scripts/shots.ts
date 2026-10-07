// Renders every email with its sample data and screenshots it: light and
// dark (prefers-color-scheme, as Apple Mail and iOS Mail do), desktop and a
// 390px phone, plus a forced inversion (roughly what Gmail's apps do).
// Heavy (a browser): run it through research/heavy.sh.
//
//   bun scripts/shots.ts [--out DIR] [--only id,id] [--mark]
//
// --mark also draws assets/email-mark.png (the mark on a paper tile, 3x).
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import * as gen from "../../auth-engine/src/templates.gen";

const root = resolve(import.meta.dir, "..", "..", "..");
const args = process.argv.slice(2);
const opt = (n: string) => {
  const i = args.indexOf(n);
  return i >= 0 ? args[i + 1] : undefined;
};
const out = resolve(opt("--out") ?? join(import.meta.dir, "..", ".shots"));
const only = opt("--only")?.split(",");
mkdirSync(out, { recursive: true });

// Playwright comes from the dashboard's dev dependencies; this package doesn't install a browser.
type Route = { fulfill(o: { contentType: string; body: Buffer }): Promise<void>; request(): { url(): string } };
type Page = {
  setContent(html: string): Promise<void>;
  screenshot(o: { path?: string; fullPage?: boolean; omitBackground?: boolean; clip?: object }): Promise<Buffer>;
  route(glob: string, f: (r: Route) => unknown): Promise<void>;
  goto(url: string): Promise<unknown>;
  addStyleTag(o: { content: string }): Promise<unknown>;
  close(): Promise<void>;
};
type Browser = {
  newPage(o: object): Promise<Page>;
  newContext(o: object): Promise<{ newPage(): Promise<Page>; close(): Promise<void> }>;
  close(): Promise<void>;
};
const { chromium } = (await import(join(root, "apps/dashboard/node_modules/@playwright/test/index.js"))) as { chromium: { launch(): Promise<Browser> } };

// 1. samples: box mail through the Go templates, app mail through templates.gen.ts
const go = Bun.spawnSync(["go", "test", "-count=1", "-run", "TestWriteSamples", "./internal/mod/email/templates"], {
  cwd: root,
  env: { ...process.env, TIFFIN_EMAIL_OUT: out },
});
if (go.exitCode !== 0) throw new Error(go.stderr.toString() + go.stdout.toString());

const dir = join(import.meta.dir, "..", "src/emails");
for (const f of readdirSync(dir).filter((f) => f.endsWith(".tsx"))) {
  const { spec } = await import(join(dir, f));
  if (spec.family !== "app") continue;
  const render = gen.appEmails[spec.id as keyof typeof gen.appEmails] as (v: Record<string, unknown>) => gen.Email;
  const e = render(spec.preview);
  writeFileSync(join(out, `app-${spec.id}.html`), e.html);
  writeFileSync(join(out, `app-${spec.id}.txt`), `Subject: ${e.subject}\n\n${e.text}`);
}
// The same app with a logo and its own accent.
const ml = (await import(join(dir, "magic-link.tsx"))).spec.preview;
writeFileSync(
  join(out, "app-magic-link-logo.html"),
  gen.magicLink({ ...ml, logoUrl: "https://larder.app/logo.png", accent: "#f2b036", accentText: "#1c1917" }).html,
);

// 2. screenshots
const browser = await chromium.launch();
let mark: Buffer | null = null;
try {
  mark = readFileSync(join(import.meta.dir, "..", "assets", "email-mark.png"));
} catch {}

if (args.includes("--mark")) {
  const svg = readFileSync(join(root, "apps/dashboard/public/favicon.svg"), "utf8").replace(/<g class="d">[\s\S]*<\/g><\/svg>$/, "</svg>");
  const page = await browser.newPage({ viewport: { width: 96, height: 96 }, deviceScaleFactor: 1 });
  await page.setContent(
    `<html><body style="margin:0;background:transparent"><div style="width:96px;height:96px;box-sizing:border-box;border-radius:22px;background:#fefdfa;border:2px solid #e8e2d8;display:flex;align-items:center;justify-content:center">` +
      `<div style="width:70px;height:70px">${svg.replace("<svg ", '<svg width="70" height="70" ')}</div></div></body></html>`,
  );
  const drawn = await page.screenshot({ omitBackground: true, clip: { x: 0, y: 0, width: 96, height: 96 } });
  writeFileSync(join(import.meta.dir, "..", "assets", "email-mark.png"), drawn);
  mark = drawn;
  // A stand-in customer logo for the screenshots.
  await page.setContent(
    `<html><body style="margin:0"><div style="width:96px;height:96px;background:#2f6b4f;display:flex;align-items:center;justify-content:center;font:700 54px/1 Georgia,serif;color:#fff">L</div></body></html>`,
  );
  writeFileSync(join(import.meta.dir, "..", "assets", "sample-logo.png"), await page.screenshot({ clip: { x: 0, y: 0, width: 96, height: 96 } }));
  await page.close();
}
const logo = readFileSync(join(import.meta.dir, "..", "assets", "sample-logo.png"));

const files = readdirSync(out).filter((f) => f.endsWith(".html") && (!only || only.some((o) => f.includes(o))));
const shots: string[] = [];
for (const f of files) {
  for (const [label, width] of [
    ["desktop", 720],
    ["phone", 390],
  ] as const) {
    for (const scheme of ["light", "dark", "inverted"] as const) {
      if (scheme === "inverted" && label === "desktop") continue;
      const ctx = await browser.newContext({ viewport: { width, height: 900 }, deviceScaleFactor: 2, colorScheme: scheme === "dark" ? "dark" : "light" });
      const page = await ctx.newPage();
      await page.route("**/*.png", (r: Route) =>
        r.fulfill({ contentType: "image/png", body: r.request().url().includes("email-mark") && mark ? mark : logo }),
      );
      await page.goto("file://" + join(out, f));
      if (scheme === "inverted") await page.addStyleTag({ content: "html{filter:invert(1) hue-rotate(180deg)} img{filter:invert(1) hue-rotate(180deg)}" });
      const name = `${f.replace(".html", "")}.${label}.${scheme}.png`;
      await page.screenshot({ path: join(out, name), fullPage: true });
      shots.push(name);
      await ctx.close();
    }
  }
}
await browser.close();
console.log(`${shots.length} screenshots in ${out}`);
