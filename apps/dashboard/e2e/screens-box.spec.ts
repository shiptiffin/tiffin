import { mkdirSync } from "node:fs";
import { test, type Page } from "@playwright/test";
import { signIn } from "./helpers";

// Visual review of the module pages against a seeded dev box (e2e/seed-box.sh):
//   SCREENS=1 E2E_BASE_URL=http://localhost:5391 E2E_OWNER_TOKEN=... bunx playwright test screens-box
test.skip(!process.env.SCREENS || !process.env.E2E_OWNER_TOKEN, "set SCREENS=1 and E2E_OWNER_TOKEN for a seeded box");

const out = process.env.SHOTS_DIR ?? "screenshots/fusion";
const only = process.env.SHOTS?.split(",");

async function shot(page: Page, name: string, fullPage = true) {
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
}

// Every module page, by URL, waiting for its title. Detail views (one job, one
// user) are reached from their lists by the area specs (screens-jobs, -data…).
const h1 = (p: Page) => p.getByRole("heading", { level: 1 }).first().waitFor();
const pages: Array<{ name: string; url: string; wait?: (p: Page) => Promise<unknown>; act?: (p: Page) => Promise<unknown>; full?: boolean }> = [
  { name: "ledger", url: "/ledger" },
  { name: "overview", url: "/projects/shop" },
  { name: "status", url: "/status" },
  { name: "tokens", url: "/tokens" },
  { name: "people", url: "/settings/people" },
  { name: "passkeys", url: "/settings/passkeys" },
  { name: "settings", url: "/settings" },
  { name: "storage", url: "/projects/shop/storage" },
  { name: "bucket", url: "/projects/shop/storage/assets?prefix=images%2F", full: false },
  { name: "inbox", url: "/projects/shop/email", full: false },
  { name: "email-settings", url: "/projects/shop/email/settings" },
  { name: "data", url: "/projects/shop/data" },
  { name: "table", url: "/projects/shop/data/tables/orders", full: false },
  { name: "sql", url: "/projects/shop/data/sql" },
  { name: "branches", url: "/projects/shop/data/branches" },
  { name: "kv", url: "/projects/shop/data/kv" },
  { name: "secrets", url: "/projects/shop/secrets" },
  { name: "metrics", url: "/metrics" },
  { name: "logs", url: "/logs?project=shop", full: false },
  { name: "errors", url: "/errors" },
  { name: "alerts", url: "/alerts" },
  { name: "backups", url: "/backups" },
  { name: "queues", url: "/projects/shop/queues" },
  { name: "jobs", url: "/projects/shop/queues/jobs", full: false },
  { name: "workflows", url: "/projects/shop/workflows" },
  { name: "analytics", url: "/projects/shop/analytics?period=24h" },
  { name: "protect", url: "/protect" },
  { name: "approvals", url: "/approvals" },
  { name: "apps", url: "/projects/shop/apps" },
  { name: "app", url: "/projects/shop/apps/web" },
  { name: "users", url: "/projects/shop/users" },
  { name: "orgs", url: "/projects/shop/orgs" },
  { name: "new", url: "/new" },
  { name: "kit", url: "/_kit" },
];

for (const theme of ["dark", "light"] as const) {
  for (const size of [
    { name: "1440", width: 1440, height: 900 },
    { name: "390", width: 390, height: 844 },
  ]) {
    test(`module screens ${theme} ${size.name}`, async ({ page, baseURL }) => {
      test.setTimeout(240_000);
      mkdirSync(out, { recursive: true });
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      page.setDefaultTimeout(20_000);
      page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && console.log(`CONSOLE ${m.text()}`));
      page.on("pageerror", (e) => console.log(`PAGEERROR ${e.message}`));
      await signIn(page, baseURL!);
      for (const pg of pages) {
        if (only && !only.includes(pg.name)) continue;
        await page.goto(pg.url);
        await (pg.wait ?? h1)(page);
        if (pg.act) await pg.act(page);
        await shot(page, `m-${pg.name}-${theme}-${size.name}`, pg.full ?? true);
        const wide = await page.evaluate(() => document.documentElement.scrollWidth);
        if (wide > size.width + 1) console.log(`OVERFLOW ${pg.name} ${size.name}: ${wide}px`);
      }
    });
  }
}
