import { mkdirSync } from "node:fs";
import { test, type Page } from "@playwright/test";
import { needsServices, signIn } from "./helpers";

// Visual review of the data tiers (Data, table browser, SQL, branches, Key-value,
// Storage, a bucket, the dev inbox, email settings) against a seeded dev box:
//   SCREENS=1 E2E_BASE_URL=http://localhost:5403 E2E_OWNER_TOKEN=... bunx playwright test screens-data
// Read-only: it runs one SELECT and opens things, nothing else.
test.skip(!process.env.SCREENS, "set SCREENS=1 for screenshots");
needsServices("shop", ["postgres", "valkey", "storage"]);

const out = process.env.SHOTS_DIR ?? "screenshots/fusion";
const only = process.env.SHOTS?.split(",");
const themes = (process.env.THEMES?.split(",") ?? ["light", "dark"]) as Array<"light" | "dark">;
const widths = process.env.WIDTHS?.split(",") ?? ["1440", "390"];

async function shot(page: Page, name: string, fullPage = true) {
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
  const wide = await page.evaluate(() => document.documentElement.scrollWidth);
  const vw = page.viewportSize()?.width ?? 0;
  if (wide > vw + 1) console.log(`OVERFLOW ${name}: ${wide}px`);
}

const pages: Array<{ name: string; url: string; wait: (p: Page) => Promise<unknown>; act?: (p: Page) => Promise<unknown>; full?: boolean }> = [
  { name: "data", url: "/projects/shop/data", wait: (p) => p.getByText("Quick actions").first().waitFor() },
  { name: "data-table", url: "/projects/shop/data/tables/orders", wait: (p) => p.getByRole("grid").waitFor() },
  {
    name: "data-table",
    url: "/projects/shop/data/tables/orders",
    wait: (p) => p.getByRole("columnheader", { name: /items/ }).waitFor(),
    full: false,
  },
  {
    name: "data-sql",
    url: "/projects/shop/data/sql",
    wait: (p) => p.getByLabel("SQL").waitFor(),
    act: async (p) => {
      await p
        .getByLabel("SQL")
        .fill(
          "SELECT c.city, count(*) AS orders, sum(o.total_cents) AS revenue_cents\nFROM orders o JOIN customers c ON c.id = o.customer_id\nGROUP BY c.city\nORDER BY revenue_cents DESC;",
        );
      await p.getByRole("button", { name: "Run", exact: true }).click();
      await p
        .getByText(/ in \d+/)
        .first()
        .waitFor();
    },
  },
  {
    name: "data-sql-write",
    url: "/projects/shop/data/sql",
    wait: (p) => p.getByLabel("SQL").waitFor(),
    act: async (p) => {
      await p.getByLabel("SQL").fill("DELETE FROM orders WHERE status = 'refunded';");
      await p.getByRole("switch", { name: "Allow changes" }).click();
    },
    full: false,
  },
  { name: "data-branches", url: "/projects/shop/data/branches", wait: (p) => p.getByRole("heading", { name: "Copies", exact: true }).waitFor() },
  {
    name: "kv",
    url: "/projects/shop/data/kv?key=leaderboard%3Abowls",
    wait: (p) => p.getByRole("heading", { name: "leaderboard:bowls" }).waitFor(),
  },
  { name: "storage", url: "/projects/shop/storage", wait: (p) => p.getByRole("heading", { name: "Buckets" }).waitFor() },
  {
    // Making a bucket public reaches outside the box, so it asks first; the shot is the question (Escape cancels).
    name: "storage-confirm",
    url: "/projects/shop/storage",
    wait: (p) => p.getByRole("heading", { name: "Buckets" }).waitFor(),
    act: async (p) => {
      await p
        .getByRole("radiogroup", { name: /^uploads/ })
        .getByRole("radio", { name: "public" })
        .click();
      await p.getByRole("alertdialog").getByRole("button", { name: "Confirm" }).waitFor();
    },
    full: false,
  },
  {
    name: "storage-bucket",
    url: "/projects/shop/storage/uploads?prefix=avatars%2F&file=avatars%2Fada.png",
    wait: (p) => p.getByRole("img", { name: "ada.png" }).waitFor(),
  },
  {
    name: "storage-text",
    url: "/projects/shop/storage/uploads?prefix=config%2F&file=config%2Fshipping.json",
    wait: (p) => p.getByText("File contents").waitFor(),
  },
  {
    name: "email",
    url: "/projects/shop/email",
    wait: (p) => p.getByText("Nothing leaves this box.").waitFor(),
    act: async (p) => {
      await p.getByRole("button", { name: /Receipt for order/ }).click();
      await p.getByTitle("Message preview").waitFor();
    },
    full: false,
  },
  { name: "email-list", url: "/projects/shop/email", wait: (p) => p.getByRole("button", { name: /Receipt for order/ }).waitFor(), full: false },
  { name: "email-empty", url: "/projects/guestbook/email", wait: (p) => p.getByText("No mail yet.").waitFor() },
  { name: "email-settings", url: "/projects/shop/email/settings", wait: (p) => p.getByRole("heading", { name: /write to/i }).waitFor() },
];

for (const theme of themes) {
  for (const w of widths) {
    const size = w === "390" ? { width: 390, height: 844 } : { width: 1440, height: 900 };
    test(`data screens ${theme} ${w}`, async ({ page, baseURL }) => {
      test.setTimeout(240_000);
      mkdirSync(out, { recursive: true });
      await page.setViewportSize(size);
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      page.setDefaultTimeout(20_000);
      page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && console.log(`CONSOLE ${m.text()}`));
      page.on("pageerror", (e) => console.log(`PAGEERROR ${e.message}`));
      await signIn(page, baseURL!);
      await page.evaluate(() => sessionStorage.removeItem("tiffin.staged"));
      for (const pg of pages) {
        if (only && !only.includes(pg.name)) continue;
        await page.goto(pg.url);
        try {
          await pg.wait(page);
          if (pg.act) await pg.act(page);
        } catch (e) {
          console.log(`WAITFAIL ${pg.name}: ${(e as Error).message.split("\n")[0]}`);
        }
        await shot(page, `${pg.name}-${theme}-${w}`, pg.full ?? true);
        await page.evaluate(() => sessionStorage.removeItem("tiffin.staged"));
      }
    });
  }
}
