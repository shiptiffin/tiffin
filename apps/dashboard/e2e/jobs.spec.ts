import { mkdirSync } from "node:fs";
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { ownerToken, port, signIn } from "./helpers";

// The Jobs area against a box whose queue really runs (e2e/serve-jobs.sh):
//   E2E_PORT=7393 E2E_SERVE=e2e/serve-jobs.sh bunx playwright test jobs
// A project with no apps (hooks) has schedules and queues that call web
// addresses (e2e/jobs-worker.ts); reports has a worker app with a workflow.
// It makes schedules, sends jobs and starts runs on the throwaway box, checks
// every page with axe (no serious or critical violations), and takes
// screenshots at 1440 and 390 px, light and dark, into SHOTS_DIR.
test.skip(!process.env.E2E_SERVE?.includes("serve-jobs"), "needs E2E_SERVE=e2e/serve-jobs.sh");
test.describe.configure({ mode: "serial" });

const worker = `http://127.0.0.1:${port + 2}`;
const out = process.env.SHOTS_DIR ?? "screenshots/jobs";

async function axe(page: Page, what: string) {
  const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
  const bad = r.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(bad.map((v) => `${what}: ${v.id} (${v.impact}) ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
}

async function calls(page: Page): Promise<Array<{ path: string; signed: boolean; cron?: string }>> {
  return (await page.request.get(`${worker}/_calls`)).json();
}

async function api<T>(page: Page, path: string): Promise<T> {
  const res = await page.request.get(path, { headers: { Authorization: `Bearer ${ownerToken()}` } });
  expect(res.ok()).toBeTruthy();
  return res.json();
}

test.beforeEach(async ({ page, baseURL }) => {
  page.on("pageerror", (e) => console.log(`PAGEERROR ${e.message}`));
  await signIn(page, baseURL!);
});

test("a project with no apps: schedules and queues that call web addresses", async ({ page }) => {
  await page.goto("/projects/hooks");
  await page.getByRole("link", { name: "Jobs", exact: true }).first().click();
  await expect(page).toHaveURL(/\/projects\/hooks\/jobs$/);
  await expect(page.getByRole("heading", { level: 1, name: "Jobs" })).toBeVisible();
  for (const tab of ["Runs", "Schedules", "Queues", "Failed"]) await expect(page.getByRole("navigation", { name: "Jobs" }).getByRole("link", { name: new RegExp(`^${tab}`) })).toBeVisible();
  await expect(page.getByRole("link", { name: /^orders/ }).first()).toBeVisible();
  await axe(page, "runs");

  await page.getByRole("navigation", { name: "Jobs" }).getByRole("link", { name: "Schedules" }).click();
  const digest = page.getByRole("listitem").filter({ hasText: "digest" });
  await expect(digest.getByText("Every weekday at 09:00")).toBeVisible();
  await expect(digest.getByText("Europe/London")).toBeVisible();
  await expect(digest.getByText(/127\.0\.0\.1:\d+\/hooks\/digest/)).toBeVisible();
  await expect(digest.getByText(/^Next /)).toBeVisible();
  await axe(page, "schedules");

  // The schedule's calls arrive signed, and the worker checked them with @shiptiffin/sdk/verify.
  const got = await calls(page);
  expect(got.filter((c) => c.cron === "digest").every((c) => c.signed)).toBeTruthy();
  expect(got.some((c) => c.path === "/hooks/orders" && c.signed)).toBeTruthy();
});

test("create, edit, pause and delete a schedule", async ({ page }) => {
  await page.goto("/projects/hooks/jobs/schedules");
  await page.getByRole("button", { name: "New schedule" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "New schedule" })).toBeVisible();
  await dialog.getByLabel("Name").fill("nightly-sync");
  // A typo in a custom schedule is caught by the box's own preview.
  await dialog.getByRole("radio", { name: "Custom" }).click();
  await dialog.getByLabel("Cron expression").fill("61 * * * *");
  await expect(dialog.getByRole("alert").filter({ hasText: "is not a schedule" })).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Create" })).toBeDisabled();
  await dialog.getByRole("radio", { name: "Every day at…" }).click();
  await dialog.locator('input[type="time"]').fill("02:30");
  await dialog.getByLabel("Time zone").fill("Asia/Tokyo");
  await expect(dialog.getByText("30 2 * * *")).toBeVisible();
  await expect(dialog.locator("ol li")).toHaveCount(5);
  await expect(dialog.locator("ol li").first()).toContainText("02:30");
  // No apps: it calls a web address.
  await expect(dialog.getByRole("radio", { name: "An app route" })).toBeDisabled();
  await dialog.getByLabel("Web address to call").fill("not a url");
  await expect(dialog.getByText(/A full address/)).toBeVisible();
  await dialog.getByLabel("Web address to call").fill(`${worker}/hooks/sync`);
  await axe(page, "schedule form");
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("Added the nightly-sync schedule")).toBeVisible();
  const row = page.getByRole("listitem").filter({ hasText: "nightly-sync" });
  await expect(row.getByText("Every night at 02:30")).toBeVisible();
  await expect(row.getByText("Asia/Tokyo")).toBeVisible();

  // Edit: every five minutes.
  await row.getByRole("button", { name: "Edit" }).click();
  await expect(dialog.getByRole("heading", { name: "Edit nightly-sync" })).toBeVisible();
  await dialog.getByRole("radio", { name: "Every N minutes" }).click();
  await dialog.getByRole("combobox").first().selectOption("5");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Changed the nightly-sync schedule")).toBeVisible();
  await expect(row.getByText("Every 5 minutes")).toBeVisible();

  // Pause, and the box keeps it paused.
  await row.getByRole("switch", { name: /nightly-sync runs on schedule/ }).click();
  await expect(row.getByText("Paused").first()).toBeVisible();
  await expect.poll(async () => (await api<Array<{ name: string; paused: boolean }>>(page, "/v1/projects/hooks/queue/crons")).find((c) => c.name === "nightly-sync")?.paused).toBe(true);
  // Run once now still works while paused.
  await row.getByRole("button", { name: "Run now" }).click();
  await expect(page.getByText("Started nightly-sync now.")).toBeVisible();
  await expect.poll(async () => (await calls(page)).filter((c) => c.path === "/hooks/sync" && c.signed).length).toBeGreaterThan(0);
  await row.getByRole("switch", { name: /nightly-sync runs on schedule/ }).click();
  await expect(row.getByText(/^Next /)).toBeVisible();

  // Delete, from the form.
  await row.getByRole("button", { name: "Edit" }).click();
  await dialog.getByRole("button", { name: "Delete schedule" }).click();
  await expect(page.getByText("Removed the nightly-sync schedule")).toBeVisible();
  await expect(page.getByRole("listitem").filter({ hasText: "nightly-sync" })).toHaveCount(0);
});

test("a private address is refused", async ({ page }) => {
  await page.goto("/projects/hooks/jobs/schedules?do=schedule");
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("inside");
  await dialog.getByLabel("Web address to call").fill("http://10.1.2.3/admin");
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText(/Couldn’t add the inside schedule/)).toBeVisible();
  await expect(page.getByText(/a private address/).first()).toBeVisible();
});

test("send a test job and watch it", async ({ page }) => {
  await page.goto("/projects/hooks/jobs/queues");
  await expect(page.getByText("orders", { exact: true }).first()).toBeVisible();
  await axe(page, "queues");
  await page.keyboard.press("t");
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Send a test job" })).toBeVisible();
  await dialog.getByLabel("Queue").selectOption("reports");
  await dialog.getByLabel("Payload (JSON)").fill('{ "pages": }');
  await expect(dialog.getByText(/That isn’t JSON yet \(line 1, column/)).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Send to reports" })).toBeDisabled();
  await dialog.getByLabel("Payload (JSON)").fill('{ "pages": 3 }');
  await dialog.getByRole("radio", { name: "High" }).click();
  await axe(page, "send form");
  await dialog.getByRole("button", { name: "Send to reports" }).click();
  await expect(page).toHaveURL(/\/projects\/hooks\/jobs\/job_\d+$/);
  // Progress and output arrive over the live stream as the job reports them.
  await expect(page.getByText("Rendering pages")).toBeVisible();
  await expect(page.getByRole("progressbar", { name: "Progress" })).toBeVisible();
  await expect(page.getByText("rendered page 2 of 3")).toBeVisible();
  await expect(page.getByText(/^Done in/)).toBeVisible({ timeout: 20_000 });
  await expect(page.getByRole("progressbar", { name: "Progress" })).toHaveAttribute("aria-valuenow", "100");
  await axe(page, "job");
});

test("start a workflow run and watch its steps live", async ({ page }) => {
  const streams: string[] = [];
  const polls: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes("/queue/live/run_")) streams.push(r.url());
    if (/\/workflows\/runs\/run_/.test(r.url())) polls.push(r.url());
  });
  await page.goto("/projects/reports/jobs");
  await page.getByRole("button", { name: "More ways to add a job" }).click();
  await page.getByRole("menuitem", { name: /Start a workflow run/ }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Workflow").fill("monthly-report");
  await dialog.getByLabel("Input (JSON)").fill('{ "month": "October" }');
  await axe(page, "start form");
  await dialog.getByRole("button", { name: "Start the run" }).click();
  await expect(page).toHaveURL(/\/projects\/reports\/jobs\/run_/);
  await expect(page.getByRole("heading", { level: 1, name: "monthly-report" })).toBeVisible();
  const steps = page.getByRole("figure", { name: /its steps over time/ });
  await expect(steps.getByText("load-orders")).toBeVisible();
  await expect(steps.getByText("wait 5 seconds")).toBeVisible();
  await expect(steps.getByText("sleeping")).toBeVisible();
  await expect(page.getByText("Rendering pages")).toBeVisible({ timeout: 20_000 });
  await expect(page.getByText("page 2 of 6")).toBeVisible();
  await expect(page.getByText(/^Done in/)).toBeVisible({ timeout: 30_000 });
  await expect(steps.getByText("email-owners")).toBeVisible();
  expect(streams.length).toBeGreaterThan(0);
  // While the stream is open the page reads the run only when it changes, never on a timer.
  expect(polls.length).toBeLessThan(25);
  await axe(page, "run");
});

test("failed jobs wait in Failed with Retry", async ({ page }) => {
  await page.goto("/projects/hooks/jobs/failed");
  await expect(page.getByText(/failed for good/)).toBeVisible();
  const group = page.getByRole("heading", { name: /warehouse/ });
  await expect(group).toBeVisible();
  await expect(page.getByText("HTTP 503: the warehouse API is down").first()).toBeVisible();
  await axe(page, "failed");
});

// ------------------------------------------------------------------ screenshots

const shots: Array<{ name: string; url: string; wait: (p: Page) => Promise<unknown>; act?: (p: Page) => Promise<unknown> }> = [
  { name: "runs", url: "/projects/hooks/jobs", wait: (p) => p.getByText("Latest runs").waitFor() },
  {
    name: "runs-selected",
    url: "/projects/reports/jobs",
    wait: (p) => p.getByText("Latest runs").waitFor(),
    act: async (p) => {
      await p.getByRole("link", { name: /monthly-report/ }).first().click();
      await p.getByRole("figure").first().waitFor();
    },
  },
  {
    name: "run",
    url: "/projects/reports/jobs",
    wait: (p) => p.getByText("Latest runs").waitFor(),
    act: async (p) => {
      await p.getByRole("link", { name: /monthly-report/ }).first().click();
      await p.getByRole("link", { name: /Every step, its history and input/ }).click();
      await p.getByRole("heading", { name: "Steps" }).waitFor();
    },
  },
  { name: "schedules", url: "/projects/hooks/jobs/schedules", wait: (p) => p.getByText("Every weekday at 09:00").waitFor() },
  { name: "schedule-form", url: "/projects/hooks/jobs/schedules?do=schedule", wait: (p) => p.getByRole("dialog").locator("ol li").nth(4).waitFor() },
  { name: "queues", url: "/projects/hooks/jobs/queues", wait: (p) => p.getByText("order.created").first().waitFor() },
  { name: "send-job", url: "/projects/hooks/jobs/queues?do=send", wait: (p) => p.getByRole("dialog").getByLabel("Payload (JSON)").waitFor() },
  { name: "failed", url: "/projects/hooks/jobs/failed", wait: (p) => p.getByText(/failed for good/).waitFor() },
];

for (const theme of ["light", "dark"] as const) {
  for (const size of [
    { name: "1440", width: 1440, height: 900 },
    { name: "390", width: 390, height: 844 },
  ]) {
    test(`jobs screens ${theme} ${size.name}`, async ({ page }) => {
      test.setTimeout(240_000);
      mkdirSync(out, { recursive: true });
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      for (const s of shots) {
        await page.goto(s.url);
        await s.wait(page);
        if (s.act) await s.act(page);
        await page.waitForTimeout(600);
        await page.screenshot({ path: `${out}/${s.name}-${theme}-${size.name}.png`, fullPage: true });
        const wide = await page.evaluate(() => document.documentElement.scrollWidth);
        expect(wide, `${s.name} scrolls sideways at ${size.name}`).toBeLessThanOrEqual(size.width);
        await axe(page, `${s.name} ${theme} ${size.name}`);
      }
    });
  }
}
