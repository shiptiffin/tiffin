import { mkdirSync } from "node:fs";
import { test, type Page } from "@playwright/test";
import { signIn } from "./helpers";

// Visual review of the jobs, people and audience pages (Queues, Jobs, a job,
// Workflows, a run, Users, a user, Organizations, an organization, Analytics)
// against the seeded dev box:
//   SCREENS=1 E2E_BASE_URL=http://localhost:5404 E2E_OWNER_TOKEN=... bunx playwright test screens-jobs
// SHOTS=queues,run limits it to some pages. Nothing is changed on the box.
test.skip(!process.env.SCREENS || !process.env.E2E_OWNER_TOKEN, "set SCREENS=1 and E2E_OWNER_TOKEN for a seeded box");

const out = process.env.SHOTS_DIR ?? "screenshots/fusion";
const only = process.env.SHOTS?.split(",");

async function shot(page: Page, name: string, fullPage = true) {
  await page.waitForTimeout(700);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
  const wide = await page.evaluate(() => document.documentElement.scrollWidth);
  const vw = page.viewportSize()?.width ?? 0;
  if (wide > vw + 1) console.log(`OVERFLOW ${name}: ${wide}px`);
}

const h1 = (p: Page) => p.getByRole("heading", { level: 1 }).first().waitFor();

const pages: Array<{ name: string; url: string | ((p: Page) => Promise<string>); wait: (p: Page) => Promise<unknown>; act?: (p: Page) => Promise<unknown>; full?: boolean }> = [
  { name: "queues", url: "/projects/shop/queues", wait: (p) => p.getByRole("heading", { name: "Schedules" }).waitFor() },
  {
    name: "queues-staged",
    url: "/projects/shop/queues",
    wait: (p) => p.getByRole("heading", { name: "Schedules" }).waitFor(),
    act: async (p) => {
      await p.getByRole("slider", { name: "thumbnails: jobs at once" }).focus();
      await p.keyboard.press("ArrowRight");
      await p.getByRole("button", { name: "Review" }).click();
      await p.getByText("What will happen, in order").waitFor();
      await p.locator(".diff .ln").first().waitFor();
    },
    full: false,
  },
  { name: "jobs", url: "/projects/shop/queues/jobs", wait: (p) => p.locator("main ol li a").first().waitFor() },
  { name: "job-dead", url: "/projects/shop/queues/jobs/job_87", wait: (p) => p.getByRole("heading", { name: "Tries" }).waitFor() },
  { name: "job-done", url: "/projects/shop/queues/jobs/job_41", wait: (p) => p.getByRole("heading", { name: "Tries" }).waitFor() },
  { name: "workflows", url: "/projects/shop/workflows", wait: (p) => p.getByRole("heading", { name: "Runs" }).waitFor() },
  { name: "run-waiting", url: "/projects/shop/workflows/run_01M41HBY11EJZM7NVG7DKPWKS3", wait: (p) => p.getByRole("heading", { name: "Steps" }).waitFor() },
  { name: "run-failed", url: "/projects/shop/workflows/run_01M41HBY2B35PQTTBK65PJ8T3C", wait: (p) => p.getByRole("heading", { name: "Steps" }).waitFor() },
  { name: "run-done", url: "/projects/shop/workflows/run_01M41HBY0MC99YBKTRQJYHVJCW", wait: (p) => p.getByRole("heading", { name: "Steps" }).waitFor() },
  { name: "users", url: "/projects/shop/users", wait: (p) => p.getByText("ada.lovelace@example.com").waitFor() },
  { name: "user", url: "/projects/shop/users/usr_demo_1", wait: h1 },
  { name: "user-suspended", url: "/projects/shop/users/usr_demo_19", wait: h1 },
  { name: "orgs", url: "/projects/shop/orgs", wait: (p) => p.getByText("Analytical Engines").waitFor() },
  { name: "org", url: "/projects/shop/orgs/org_demo_1", wait: h1 },
  { name: "analytics", url: "/projects/shop/analytics", wait: (p) => p.getByText("IP Geolocation by DB-IP").first().waitFor() },
  {
    name: "analytics-hover",
    url: "/projects/shop/analytics?period=24h",
    wait: (p) => p.getByText("IP Geolocation by DB-IP").first().waitFor(),
    act: async (p) => {
      const fig = p.locator("figure").first();
      await fig.scrollIntoViewIfNeeded();
      const box = (await fig.boundingBox())!;
      await p.mouse.move(box.x + box.width * 0.62, box.y + 120);
      await p.getByRole("status").filter({ hasText: "Page views" }).waitFor();
    },
    full: false,
  },
  { name: "queues-empty", url: "/projects/notes/queues", wait: (p) => p.getByRole("heading", { name: "Schedules" }).waitFor() },
  { name: "workflows-empty", url: "/projects/notes/workflows", wait: (p) => p.getByRole("heading", { name: "Runs" }).waitFor() },
  { name: "analytics-24h", url: "/projects/shop/analytics?period=24h", wait: (p) => p.getByText("IP Geolocation by DB-IP").first().waitFor() },
];

for (const theme of ["light", "dark"] as const) {
  for (const size of [
    { name: "1440", width: 1440, height: 900 },
    { name: "390", width: 390, height: 844 },
  ]) {
    test(`jobs screens ${theme} ${size.name}`, async ({ page, baseURL }) => {
      test.setTimeout(300_000);
      mkdirSync(out, { recursive: true });
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      page.setDefaultTimeout(20_000);
      page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && console.log(`CONSOLE ${m.text()}`));
      page.on("pageerror", (e) => console.log(`PAGEERROR ${e.message}`));
      await signIn(page, baseURL!);
      for (const pg of pages) {
        if (only && !only.includes(pg.name)) continue;
        if (only && process.env.THEMES && !process.env.THEMES.split(",").includes(`${theme}-${size.name}`)) continue;
        await page.goto(typeof pg.url === "string" ? pg.url : await pg.url(page));
        await pg.wait(page);
        if (pg.act) await pg.act(page);
        await shot(page, `${pg.name}-${theme}-${size.name}`, pg.full ?? true);
        await page.evaluate(() => sessionStorage.removeItem("tiffin.staged"));
      }
    });
  }
}
