import AxeBuilder from "@axe-core/playwright";
import { mkdirSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { indexNow, searchReport } from "./fixtures/search-report";
import { signIn } from "./helpers";

// Analytics › Search and Settings › Search engines against the seeded box. A
// laptop box has no findable address and no Google connection, so the
// search report and IndexNow are stubbed (e2e/fixtures/search-report.ts):
//   bunx playwright test search
// SHOTS=1 also saves screenshots (1440 and 390, light and dark) to SHOTS_DIR.
// Nothing on the box changes.

const out = process.env.SHOTS_DIR ?? "screenshots/search";

async function stub(page: Page, state: "verified" | "not_connected" | "none" = "verified") {
  const asked: string[] = [];
  await page.route("**/v1/projects/hello/search/report*", (r) => {
    const u = new URL(r.request().url());
    asked.push(u.search);
    const days = { "7d": 7, "90d": 90, "12mo": 365 }[u.searchParams.get("period") ?? ""] ?? 30;
    return r.fulfill({ json: searchReport("hello", { state, days }) });
  });
  await page.route("**/v1/search/indexnow", (r) => r.fulfill({ json: indexNow() }));
  return asked;
}

async function noSeriousA11y(page: Page, what: string) {
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity));
  const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
  const bad = r.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(
    bad.map(
      (v) =>
        `${what}: ${v.id} (${v.impact}) ${v.nodes.map((n) => `${n.target.join(" ")} ${n.html.slice(0, 160)} ${n.failureSummary ?? ""}`).join(", ")}`,
    ),
  ).toEqual([]);
}

test.describe.configure({ mode: "serial" });

test("search: the tab, the numbers, the period in the URL", async ({ page, baseURL }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(e.message));
  const asked = await stub(page);
  await signIn(page, baseURL!);
  await page.goto("/projects/hello/analytics");
  await page.getByRole("navigation", { name: "Analytics" }).getByRole("button", { name: "Search" }).click();
  await expect(page).toHaveURL(/tab=search/);
  // 30 days by default, ending on Google's last day.
  await expect(page.getByRole("button", { name: "Days: Last 30 days, compared with the 30 days before. Change" })).toBeVisible();
  await expect(page.getByRole("radio", { name: /^Clicks [\d,]+/ })).toBeVisible();
  await page.getByRole("radio", { name: /^Average position/ }).click();
  await expect(page.getByText("Lower is better: 1 is the top of the page.")).toBeVisible();
  await expect(page.getByRole("region", { name: "Searches", exact: true }).getByText("self hosted vercel alternative")).toBeVisible();
  await page.getByRole("button", { name: "Show all 12" }).click();
  await expect(page.getByText("better auth self hosted")).toBeVisible();
  // What moved against the days before: searches, then pages.
  const moved = page.getByRole("region", { name: /^What moved/ });
  await expect(moved).toContainText("+8 clicks");
  await expect(moved).toContainText("−4 clicks");
  await moved.getByRole("radio", { name: "Pages" }).click();
  await expect(moved).toContainText("/blog/launch");
  await expect(moved).toContainText("No longer shows");
  // Found: Google's reasons in plain words; crawlers; AI apart.
  const found = page.getByRole("region", { name: "Found" });
  await expect(found).toContainText("12 of 36 pages checked");
  await expect(found).toContainText("Google knows it but hasn’t read it yet");
  await expect(page.getByRole("region", { name: "Search engine crawlers" })).toContainText("Googlebot");
  await expect(page.getByRole("region", { name: "Search engine crawlers" })).not.toContainText("ClaudeBot");
  await expect(page.getByRole("region", { name: "AI bots" })).toContainText("ClaudeBot");
  // The period lands in the URL; 30 days, the default, stays out of it.
  await page.getByRole("button", { name: /^Days:/ }).click();
  await page.getByRole("menuitemradio", { name: "Last 90 days" }).click();
  await expect(page).toHaveURL(/period=90d/);
  expect(asked.at(-1)).toContain("period=90d");
  await page.getByRole("button", { name: /^Days:/ }).click();
  await expect(page.getByRole("menuitemradio", { name: "Today" })).toHaveCount(0);
  await page.getByRole("menuitemradio", { name: "Last 30 days" }).click();
  await expect(page).not.toHaveURL(/period=/);
  expect(problems).toEqual([]);
});

test("search: without Google, and with nothing findable", async ({ page, baseURL }) => {
  await stub(page, "not_connected");
  await signIn(page, baseURL!);
  await page.goto("/projects/hello/analytics?tab=search");
  await expect(page.getByText("See what people search on Google to find hello")).toBeVisible();
  await expect(page.getByRole("link", { name: "Connect Google" })).toHaveAttribute("href", "/settings/search");
  await page.unrouteAll();
  await stub(page, "none");
  await page.reload();
  await expect(page.getByRole("heading", { name: "Not in search results yet" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Add a domain" })).toBeVisible();
});

test("search: no serious accessibility problems, screenshots", async ({ page, baseURL }) => {
  test.setTimeout(180_000);
  if (process.env.SHOTS) mkdirSync(out, { recursive: true });
  await stub(page);
  await signIn(page, baseURL!);
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: width > 400 ? 900 : 844 });
      await page.goto("/projects/hello/analytics?tab=search");
      await expect(page.getByRole("radio", { name: /^Clicks/ })).toBeVisible();
      await noSeriousA11y(page, `search ${width} ${scheme}`);
      if (process.env.SHOTS) await page.screenshot({ path: `${out}/search-${width}-${scheme}.png`, fullPage: true });
      await page.goto("/settings/search");
      await expect(page.getByRole("region", { name: "IndexNow" })).toContainText("shiptiffin.com");
      await noSeriousA11y(page, `settings ${width} ${scheme}`);
      if (process.env.SHOTS) await page.screenshot({ path: `${out}/settings-${width}-${scheme}.png`, fullPage: true });
    }
  }
});
