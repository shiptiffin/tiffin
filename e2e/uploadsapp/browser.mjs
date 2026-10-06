// Drives the uploads page in a real headless Chrome (Playwright), from the
// Mac against the box's app origin. Usage: node browser.mjs <app url>.
// Prints one JSON line with what each upload did.
import { createRequire } from "node:module";
import { existsSync } from "node:fs";

const [appURL, from] = process.argv.slice(2);
const { chromium } = createRequire(from)("@playwright/test");
const chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH ?? (existsSync(chrome) ? chrome : undefined) });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await ctx.newPage();
const out = {};
page.on("console", (m) => process.stderr.write(`[page] ${m.text()}\n`));
await page.goto(appURL);

// Each upload runs in the page: the bytes go from Chrome to s3.<domain>.
const run = (route, size, type) =>
  page.evaluate(
    async ({ route, size, type }) => {
      const bytes = new Uint8Array(size);
      for (let i = 0; i < size; i += 4096) bytes[i] = (i / 4096) % 251;
      const file = new File([bytes], `e2e-${size}.bin`, { type });
      const progress = [];
      const t0 = performance.now();
      try {
        const done = await window.tiffin.uploadFile(file, route, { onProgress: (p) => progress.push(Math.round(p.percent * 10) / 10) });
        return { ok: true, done, progress: progress.length, last: progress.at(-1), monotonic: progress.every((p, i) => i === 0 || p >= progress[i - 1]), ms: Math.round(performance.now() - t0) };
      } catch (e) {
        return { ok: false, code: e.code, status: e.status, message: String(e.message ?? e) };
      }
    },
    { route, size, type },
  );

out.single = await run("/api/upload/media", 5 << 20, "image/png");
out.multipart = await run("/api/upload/media", 200 << 20, "video/mp4");
out.tooBig = await run("/api/upload/small", 2 << 20, "application/octet-stream");
console.log(JSON.stringify(out));
await browser.close();
