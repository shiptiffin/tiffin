import { mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { signIn } from "./helpers";

// The Link preview tab on a deploy, its fact, and the app page's fact. A
// laptop box runs no apps, so hello/web's deploys and their link previews
// are stubbed:
//   bunx playwright test link-preview
// SHOTS=1 also saves screenshots (1024 and 390, light and dark) to SHOTS_DIR.

const out = process.env.SHOTS_DIR ?? "screenshots/link-preview";
const LIVE = "dep_01M5XWP7Q5YJ5Y8D43MFT30PJA";
const OLD = "dep_01M5XW00000000000000000000";
const sunset = readFileSync(join(import.meta.dirname, "fixtures", "sunset.png"));

const deploy = (id: string, status: string, extra: object = {}) => ({
  id,
  project: "hello",
  app: "web",
  status,
  source: "git",
  framework: "next",
  repo: "https://github.com/acme/hello",
  commit: "4f2a9c1d0b",
  message: "Share image for the launch",
  createdAt: id === LIVE ? "2026-10-26T09:00:00Z" : "2026-10-25T09:00:00Z",
  liveAt: id === LIVE ? "2026-10-26T09:01:10Z" : "2026-10-25T09:01:00Z",
  buildSeconds: 52,
  durationSeconds: 70,
  appUrl: "https://hello.example.app",
  url: `https://d-${id.slice(-8).toLowerCase()}--hello.example.app`,
  retention: "kept",
  ...extra,
});

const summary = {
  status: "warn",
  checkedAt: new Date(Date.now() - 4 * 60_000).toISOString(),
  title: "Hello – sign-up sheets that fill themselves",
  image: "https://hello.example.app/opengraph-image.png (1200×630)",
  warn: 1,
  problems: ["X card: There is no twitter:card, so X shows the image small, beside the title."],
  changes: ["Title: Hello → Hello – sign-up sheets that fill themselves", "X card removed (was summary_large_image)"],
  worse: true,
};

const report = (large: boolean) => ({
  deploy: LIVE,
  checkedAt: summary.checkedAt,
  url: "https://hello.example.app/",
  status: large ? "ok" : "warn",
  title: summary.title,
  description: "Make a sheet, share the link, and people pick a slot. No ads, no accounts for the people signing up.",
  siteName: "Hello",
  pageTitle: "Hello – sign-up sheets",
  metaDescription: "Make a sheet, share the link, and people pick a slot. No ads, no accounts for the people signing up.",
  twitterCard: large ? "summary_large_image" : undefined,
  canonical: "https://hello.example.app/",
  icon: "https://hello.example.app/icon.png",
  image: { url: "https://hello.example.app/opengraph-image.png", status: 200, mime: "image/png", bytes: 182_000, width: 1200, height: 630, hash: "a1b2", thumb: true },
  checks: [
    { id: "title", label: "Title", status: "ok", detail: `“${summary.title}”` },
    { id: "description", label: "Description", status: "ok", detail: "104 characters." },
    { id: "image", label: "Share image", status: "ok", detail: "https://hello.example.app/opengraph-image.png" },
    { id: "image-size", label: "Image size", status: "ok", detail: "1200×630, 177 KB" },
    large
      ? { id: "twitter-card", label: "X card", status: "ok", detail: "summary_large_image: the image shows full width." }
      : {
          id: "twitter-card",
          label: "X card",
          status: "warn",
          detail: "There is no twitter:card, so X shows the image small, beside the title.",
          fix: "Add twitter:card summary_large_image (Next.js: twitter.card in metadata).",
        },
    { id: "canonical", label: "Canonical address", status: "ok", detail: "https://hello.example.app/" },
    { id: "robots", label: "Search engines", status: "ok", detail: "Allowed to list the page." },
    { id: "icon", label: "Icon", status: "ok", detail: "https://hello.example.app/icon.png" },
  ],
  tags: [
    { name: "title", value: "Hello – sign-up sheets" },
    { name: "description", value: "Make a sheet, share the link, and people pick a slot." },
    { name: "og:title", value: summary.title },
    { name: "og:image", value: "https://hello.example.app/opengraph-image.png" },
    { name: "og:image:width", value: "1200" },
    { name: "og:site_name", value: "Hello" },
    { name: "link canonical", value: "https://hello.example.app/" },
  ],
  compared: { deploy: OLD, version: 1 },
  changes: [
    { field: "Title", from: "Hello", to: summary.title },
    { field: "X card", from: "summary_large_image", worse: true },
  ],
  thumbUrl: `/v1/projects/hello/apps/web/deploys/${LIVE}/link-preview/image?v=a1b2`,
  canCheck: true,
});

async function stub(page: Page) {
  const state = { checks: 0 };
  const live = deploy(LIVE, "live", { linkPreview: summary });
  const old = deploy(OLD, "superseded", { linkPreview: { ...summary, status: "ok", problems: [], changes: [], worse: false } });
  await page.route("**/v1/projects/hello/apps/web/deploys?*", (r) => r.fulfill({ json: { deploys: [live, old] } }));
  await page.route("**/v1/projects/hello/apps/web/deploys", (r) => r.fulfill({ json: { deploys: [live, old] } }));
  await page.route(`**/v1/projects/hello/apps/web/deploys/${LIVE}`, (r) => r.fulfill({ json: live }));
  await page.route(`**/v1/projects/hello/apps/web/deploys/${LIVE}?*`, (r) => r.fulfill({ json: live }));
  await page.route(`**/v1/projects/hello/apps/web/deploys/${LIVE}/build-log*`, (r) =>
    r.fulfill({ json: { deploy: LIVE, status: "live", text: "==> built in 52.0s\n==> live in 70.0s total\n", offset: 40, done: true } }),
  );
  await page.route(`**/v1/projects/hello/apps/web/deploys/${LIVE}/link-preview/image*`, (r) => r.fulfill({ body: sunset, contentType: "image/png" }));
  await page.route(`**/v1/projects/hello/apps/web/deploys/${LIVE}/link-preview*`, (r) => {
    if (r.request().method() === "POST") state.checks++;
    return r.fulfill({ json: report(state.checks > 0) });
  });
  await page.route("**/v1/projects/hello/apps/web/runtime", (r) =>
    r.fulfill({
      json: {
        project: "hello",
        app: "web",
        framework: "next",
        role: "web",
        previews: [],
        production: { project: "hello", app: "web", url: "https://hello.example.app", live: LIVE, instances: [], updatedAt: "2026-10-26T09:01:10Z" },
      },
    }),
  );
  return state;
}

async function shoot(page: Page, name: string) {
  if (!process.env.SHOTS) return;
  mkdirSync(out, { recursive: true });
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.waitForTimeout(150);
    await page.screenshot({ path: `${out}/${name}-${page.viewportSize()!.width}-${scheme}.png`, fullPage: true });
  }
  await page.emulateMedia({ colorScheme: "light" });
}

test.describe.configure({ mode: "serial" });

test("link preview: the deploy's fact, the tab, the cards, check again", async ({ page, baseURL }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(e.message));
  const state = await stub(page);
  await page.setViewportSize({ width: 1024, height: 768 });
  await signIn(page, baseURL!);
  await page.goto(`/projects/hello/apps/web/deploys/${LIVE}`);
  // The fact opens the tab.
  await page.getByRole("button", { name: "1 warning" }).click();
  await expect(page).toHaveURL(/tab=preview/);
  const tab = page.getByRole("tabpanel");
  await expect(tab.getByText("What the box checked")).toBeVisible();
  await expect(tab.getByText("There is no twitter:card, so X shows the image small, beside the title.")).toBeVisible();
  await expect(tab.getByText("Changed since v1")).toBeVisible();
  await expect(tab.getByText("X card removed (was summary_large_image)")).toBeVisible();
  // X without a large card: the small image beside the title.
  await expect(tab.getByText("hello.example.app").first()).toBeVisible();
  await shoot(page, "tab-x-small");
  for (const s of ["LinkedIn", "Slack", "Google"]) {
    await tab.getByRole("radio", { name: s }).click();
    await shoot(page, `tab-${s.toLowerCase()}`);
  }
  await expect(tab.getByRole("region", { name: "How a shared link looks" }).getByText("Hello – sign-up sheets", { exact: true })).toBeVisible(); // Google uses <title>
  // The tags, folded.
  await tab.getByText(/The page’s tags \(7\)/).click();
  await expect(tab.getByRole("rowheader", { name: "og:image:width" })).toBeVisible();
  // Check again: now with a large card, and fine.
  await tab.getByRole("button", { name: "Check again" }).click();
  await expect(tab.getByText("summary_large_image: the image shows full width.")).toBeVisible();
  expect(state.checks).toBe(1);
  await tab.getByRole("radio", { name: "X" }).click();
  await expect(tab.getByText("From hello.example.app")).toBeVisible();
  await shoot(page, "tab-x-large");
  // A phone.
  await page.setViewportSize({ width: 390, height: 844 });
  await shoot(page, "tab-phone");
  expect(problems).toEqual([]);
});

test("link preview: the app's production fact and the worse mark on the row", async ({ page, baseURL }) => {
  await stub(page);
  await page.setViewportSize({ width: 1024, height: 768 });
  await signIn(page, baseURL!);
  await page.goto("/projects/hello/apps/web");
  const facts = page.getByRole("region", { name: "Production" });
  await expect(facts.getByRole("link", { name: "1 warning" })).toBeVisible();
  await expect(page.getByRole("link", { name: /^Link preview got worse: Title/ })).toBeVisible();
  await shoot(page, "app");
  await facts.getByRole("link", { name: "1 warning" }).click();
  await expect(page).toHaveURL(new RegExp(`deploys/${LIVE}\\?tab=preview`));
  await expect(page.getByRole("tab", { name: /Link preview/, selected: true })).toBeVisible();
});
