import AxeBuilder from "@axe-core/playwright";
import { mkdirSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { usageHistory } from "./fixtures/usage-history";
import { signIn } from "./helpers";

// Analytics and the Usage page's charts against the seeded box (e2e/serve.sh
// seeds eleven weeks of traffic for "hello" with e2e/seed-analytics.ts; the
// usage history is stubbed, a laptop box has no metrics store):
//   bunx playwright test analytics
//   E2E_BASE_URL=http://127.0.0.1:7471 E2E_OWNER_TOKEN=... bunx playwright test analytics   # a running seeded box
// Screenshots (1440 and 390, light and dark) go to SHOTS_DIR. Nothing is changed on the box.

const out = process.env.SHOTS_DIR ?? "screenshots/analytics";

async function noSeriousA11y(page: Page, what: string) {
  // Measure once nothing is fading in: mid-animation colours aren't the page's.
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity));
  const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
  const bad = r.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(bad.map((v) => `${what}: ${v.id} (${v.impact}) ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
}

async function stubHistory(page: Page) {
  await page.route("**/v1/projects/hello/usage/history?*", (r) => {
    const u = new URL(r.request().url());
    return r.fulfill({ json: usageHistory("hello", u.searchParams.get("range") ?? "24h", u.searchParams.get("app") ?? undefined) });
  });
}

test.describe.configure({ mode: "serial" });

test("analytics: period, comparison, filters in the URL, readout by keyboard", async ({ page, baseURL }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(e.message));
  await signIn(page, baseURL!);
  await page.goto("/projects/hello/analytics?period=30d");
  await expect(page.getByRole("button", { name: "Days: Last 30 days, compared with the 30 days before. Change" })).toBeVisible();
  // Each headline number says how it compares with the days before.
  await expect(page.getByRole("radio", { name: /^Visitors [\d,]+ \d+\s?% , against [\d,]+ the 30 days before$/ })).toBeVisible();

  // The headline numbers choose the chart.
  const visitors = Number((await page.getByRole("radio", { name: /^Visitors/ }).innerText()).match(/[\d,]+/)![0].replace(/,/g, ""));
  expect(visitors).toBeGreaterThan(5000);
  await page.getByRole("radio", { name: /^Bounce rate/ }).click();
  await expect(page.getByRole("heading", { name: "Bounce rate per day" })).toBeVisible();
  await expect(page).toHaveURL(/metric=bounce/);

  // The chart reads out by keyboard: End goes to the last day, Left steps back.
  const chart = page.getByRole("group", { name: /^Bounce rate per day/ });
  await chart.focus();
  await page.keyboard.press("End");
  await page.keyboard.press("ArrowLeft");
  await expect(page.locator("[aria-live=polite]").filter({ hasText: /Bounce rate \d+\s?%, The 30 days before \d+\s?%/ })).toHaveCount(1);
  // The table view has every day.
  await page.getByRole("button", { name: "Show as a table" }).click();
  await expect(page.getByRole("table", { name: "Bounce rate per day" }).locator("tbody tr")).toHaveCount(30);
  await page.getByRole("button", { name: "Show as charts" }).click();

  // A country filters the whole page and lands in the URL; the chip takes it off.
  await page.getByRole("button", { name: /^Filter by country Germany/ }).click();
  await expect(page).toHaveURL(/country=DE/);
  await expect(page.getByRole("button", { name: "Remove filter: Country is Germany" })).toBeVisible();
  // The numbers follow the filter once its answer is in.
  await expect
    .poll(async () => Number((await page.getByRole("radio", { name: /^Visitors/ }).innerText()).match(/[\d,]+/)![0].replace(/,/g, "")))
    .toBeLessThan(visitors);
  // Two filters: Germany and visits that started on the blog.
  await page.getByRole("tab", { name: "Entry pages" }).click();
  await page.getByRole("button", { name: /^Filter by entry page \/blog\/shipping-on-a-budget/ }).click();
  await expect(page).toHaveURL(/entry=%2Fblog%2Fshipping-on-a-budget/);
  await page.reload();
  await expect(page.getByRole("button", { name: "Remove filter: Entry page is /blog/shipping-on-a-budget" })).toBeVisible();
  await page.getByRole("button", { name: "Clear all" }).click();
  await expect(page).not.toHaveURL(/country=/);

  // Days of one's own choosing: the last 7, today included.
  await page.getByRole("button", { name: /^Days: / }).click();
  await page.getByRole("menuitem", { name: "Choose days…" }).click();
  const today = new Date().toISOString().slice(0, 10);
  const weekAgo = new Date(Date.now() - 6 * 86_400_000).toISOString().slice(0, 10);
  await page.getByLabel("First day", { exact: true }).fill(weekAgo);
  await page.getByLabel("Last day", { exact: true }).fill(today);
  await page.getByRole("button", { name: "Show these days" }).click();
  await expect(page).toHaveURL(new RegExp(`from=${weekAgo}`));
  await expect(page.getByRole("button", { name: /^Days: .+, compared with the 7 days before\. Change$/ })).toBeVisible();

  // Custom events and page speed, coloured by rating.
  await expect(page.getByText("Signup", { exact: true }).first()).toBeVisible();
  const vitals = page.getByRole("region", { name: "Web Vitals" });
  await expect(vitals.getByRole("radio", { name: /^LCP (Good|Needs work|Poor) / })).toBeVisible();
  await expect(vitals.getByRole("img", { name: "Needs work" }).first()).toBeVisible();
  expect(problems).toEqual([]);
});

test("analytics: 90 days by the hour stays smooth", async ({ page, baseURL }) => {
  await signIn(page, baseURL!);
  const t0 = Date.now();
  await page.goto("/projects/hello/analytics?period=90d&interval=hour");
  const chart = page.getByRole("group", { name: /^Visitors per hour/ });
  await expect(chart).toBeVisible();
  const ready = Date.now() - t0;
  // Move the crosshair across the plot, one move a frame, and time the frames.
  const box = (await chart.boundingBox())!;
  const frames = await page.evaluate(async ({ x, y, w }) => {
    const el = document.querySelector('[role="group"][aria-label^="Visitors per hour"]')!;
    const times: number[] = [];
    let last = performance.now();
    for (let i = 0; i < 90; i++) {
      el.dispatchEvent(new PointerEvent("pointermove", { clientX: x + 50 + (i / 90) * (w - 60), clientY: y + 80, bubbles: true, pointerType: "mouse" }));
      await new Promise((r) => requestAnimationFrame(() => r(null)));
      const now = performance.now();
      times.push(now - last);
      last = now;
    }
    times.sort((a, b) => a - b);
    return { p50: times[45], p95: times[85], max: times[89] };
  }, { x: box.x, y: box.y, w: box.width });
  const n = await page.evaluate(() => (document.querySelector('[role="group"][aria-label^="Visitors per hour"] svg path[stroke-width]')?.getAttribute("d")?.match(/C/g) ?? []).length);
  console.log(`90 days × hourly: ${n} curve segments, page ready in ${ready} ms, crosshair frames p50 ${frames.p50.toFixed(1)} ms, p95 ${frames.p95.toFixed(1)} ms, max ${frames.max.toFixed(1)} ms`);
  expect(n).toBeGreaterThan(2000);
  expect(frames.p95).toBeLessThan(50);
  await expect(page.getByText(/^Mon|^Tue|^Wed|^Thu|^Fri|^Sat|^Sun/).first()).toBeVisible(); // the readout
});

test("usage: charts over time against limits, ranges, tables", async ({ page, baseURL }) => {
  await stubHistory(page);
  await signIn(page, baseURL!);
  await page.goto("/projects/hello/observability?tab=resources");
  await expect(page.getByRole("heading", { name: "Over time" })).toBeVisible();
  for (const name of ["Memory", "CPU"]) await expect(page.getByRole("figure", { name: new RegExp(`^${name}`) })).toBeVisible();
  const asked = page.waitForRequest((r) => r.url().includes("/usage/history") && r.url().includes("range=7d"));
  await page.getByRole("radiogroup", { name: "Time range" }).getByRole("radio", { name: "7 days" }).click();
  await asked;
  await page.getByRole("button", { name: "Show as a table" }).click();
  await expect(page.getByRole("table", { name: /^Memory, the last 7 days/ }).locator("tbody tr")).toHaveCount(168);
  await page.getByRole("button", { name: "Show as charts" }).click();
  await expect(page.getByRole("figure", { name: /^Memory/ })).toBeVisible();
});

test("analytics and usage: no serious accessibility problems, screenshots", async ({ page, baseURL }) => {
  test.setTimeout(180_000);
  mkdirSync(out, { recursive: true });
  await stubHistory(page);
  await signIn(page, baseURL!);
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: width > 400 ? 900 : 844 });
      await page.goto("/projects/hello/analytics?period=30d");
      await page.getByRole("heading", { name: "Visitors per day" }).waitFor();
      await page.getByRole("group", { name: /^Visitors per day/ }).locator("path").first().waitFor();
      await page.waitForTimeout(800); // the map's chunk
      await noSeriousA11y(page, `analytics ${scheme} ${width}`);
      await page.screenshot({ path: `${out}/analytics-${width}-${scheme}.png`, fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);

      await page.goto("/projects/hello/analytics?period=30d&country=US&source=Google");
      await page.getByRole("button", { name: "Remove filter: Country is United States" }).waitFor();
      await page.getByRole("radio", { name: /^Visitors/ }).waitFor();
      await page.waitForTimeout(500);
      await noSeriousA11y(page, `analytics filtered ${scheme} ${width}`);
      if (width === 1440) await page.screenshot({ path: `${out}/analytics-filtered-${width}-${scheme}.png`, fullPage: false });

      await page.goto("/projects/hello/observability?tab=resources");
      await page.getByRole("heading", { name: "Over time" }).scrollIntoViewIfNeeded();
      await page.getByRole("figure", { name: /^Memory/ }).locator("svg").waitFor();
      await page.waitForTimeout(500);
      await noSeriousA11y(page, `usage ${scheme} ${width}`);
      await page.screenshot({ path: `${out}/usage-${width}-${scheme}.png`, fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);
    }
  }
});
