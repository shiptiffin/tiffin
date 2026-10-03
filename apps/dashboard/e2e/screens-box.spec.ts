import { mkdirSync } from "node:fs";
import { test, type Page } from "@playwright/test";
import { signIn } from "./helpers";

// Visual review of the module pages against a seeded dev box (e2e/seed-box.sh):
//   SCREENS=1 E2E_BASE_URL=http://localhost:5391 E2E_OWNER_TOKEN=... bunx playwright test screens-box
test.skip(!process.env.SCREENS || !process.env.E2E_OWNER_TOKEN, "set SCREENS=1 and E2E_OWNER_TOKEN for a seeded box");

const out = "screenshots";
const only = process.env.SHOTS?.split(",");

async function shot(page: Page, name: string, fullPage = true) {
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
}

const pages: Array<{ name: string; url: string; wait?: (p: Page) => Promise<unknown>; act?: (p: Page) => Promise<unknown>; full?: boolean }> = [
  { name: "storage", url: "/projects/shop/storage", wait: (p) => p.getByRole("heading", { name: "Buckets" }).waitFor() },
  {
    name: "bucket-image",
    url: "/projects/shop/storage/assets?prefix=images%2F&file=images%2Fsunset.png",
    wait: (p) => p.getByRole("img", { name: "sunset.png" }).waitFor(),
    full: false,
  },
  {
    name: "bucket-files",
    url: "/projects/shop/storage/uploads?prefix=config%2F&file=config%2Fshipping.json",
    wait: (p) => p.getByText('"freeOver"').first().waitFor(),
    full: false,
  },
  {
    name: "inbox",
    url: "/projects/shop/email",
    act: async (p) => {
      await p.getByRole("button", { name: /Receipt for order/ }).click();
      await p.getByTitle("Message preview").waitFor();
    },
    full: false,
  },
  { name: "email-settings", url: "/projects/shop/email/settings", wait: (p) => p.getByText("bounced@example.com").waitFor() },
  { name: "data", url: "/projects/shop/data", wait: (p) => p.getByText("orders", { exact: true }).waitFor() },
  { name: "table", url: "/projects/shop/data/tables/orders", wait: (p) => p.getByRole("table").waitFor(), full: false },
  {
    name: "sql",
    url: "/projects/shop/data/sql",
    act: async (p) => {
      await p
        .getByLabel("SQL")
        .fill(
          "SELECT c.city, count(*) AS orders, round(sum(o.total_cents) / 100.0, 2) AS revenue\nFROM orders o JOIN customers c ON c.id = o.customer_id\nGROUP BY 1 ORDER BY revenue DESC;",
        );
      await p.getByRole("button", { name: "Run", exact: true }).click();
      await p.getByRole("table").waitFor();
    },
  },
  {
    name: "branches",
    url: "/projects/shop/data/branches",
    wait: (p) =>
      p
        .getByText(/from main/)
        .first()
        .waitFor(),
  },
  { name: "kv", url: "/projects/shop/data/kv?key=leaderboard%3Abowls", wait: (p) => p.getByText("BWL-01").first().waitFor() },
  { name: "metrics", url: "/metrics", wait: (p) => p.getByRole("heading", { name: "Services" }).waitFor() },
  { name: "logs", url: "/logs?project=shop", wait: (p) => p.getByText(/lines/).waitFor(), full: false },
  { name: "errors", url: "/errors", wait: (p) => p.getByText(/TypeError/).waitFor() },
  { name: "issue", url: "/errors", act: async (p) => (await p.getByText(/TypeError/).click(), p.getByText("Latest event").waitFor()) },
  { name: "alerts", url: "/alerts", wait: (p) => p.getByRole("heading", { name: "Rules" }).waitFor() },
  { name: "backups", url: "/backups", wait: (p) => p.getByRole("heading", { name: "History" }).waitFor() },
  {
    name: "restore",
    url: "/backups",
    act: async (p) => {
      await p.getByRole("button", { name: "Restore…" }).first().click();
      await p.getByText(/Everything since/).waitFor();
    },
    full: false,
  },
  { name: "queues", url: "/projects/shop/queues", wait: (p) => p.getByText("thumbnails").first().waitFor() },
  {
    name: "jobs",
    url: "/projects/shop/queues/jobs?queue=webhooks",
    wait: (p) =>
      p
        .getByText(/webhooks/)
        .first()
        .waitFor(),
    full: false,
  },
  {
    name: "job",
    url: "/projects/shop/queues/jobs?state=dead",
    act: async (p) => (
      await p
        .getByText(/410 Gone/)
        .first()
        .click(),
      p.getByRole("heading", { name: "Attempts" }).waitFor()
    ),
  },
  { name: "workflows", url: "/projects/shop/workflows", wait: (p) => p.getByText(/waiting for a person/).waitFor() },
  {
    name: "run",
    url: "/projects/shop/workflows?state=completed",
    act: async (p) => (await p.getByText("fulfil-order").first().click(), p.getByRole("heading", { name: "Steps" }).waitFor()),
  },
  { name: "analytics", url: "/projects/shop/analytics?period=24h", wait: (p) => p.getByText("Top pages").waitFor() },
  { name: "protect", url: "/protect", wait: (p) => p.getByText("Banned right now").waitFor() },
  { name: "approvals", url: "/approvals", wait: (p) => p.getByText(/Workflows waiting/).waitFor() },
];

for (const theme of ["dark", "light"] as const) {
  for (const size of [
    { name: "1440", width: 1440, height: 960 },
    { name: "375", width: 375, height: 812 },
  ]) {
    test(`module screens ${theme} ${size.name}`, async ({ page, baseURL }) => {
      test.setTimeout(240_000);
      mkdirSync(out, { recursive: true });
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      page.setDefaultTimeout(20_000);
      await signIn(page, baseURL!);
      for (const pg of pages) {
        if (only && !only.includes(pg.name)) continue;
        await page.goto(pg.url);
        if (pg.wait) await pg.wait(page);
        if (pg.act) await pg.act(page);
        await shot(page, `m-${pg.name}-${theme}-${size.name}`, pg.full ?? true);
      }
    });
  }
}
