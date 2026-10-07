import { mkdirSync } from "node:fs";
import { test, type Page } from "@playwright/test";
import { needsServices, signIn } from "./helpers";

// Visual review of the simple dashboard (Projects home, a project's pages,
// the confirm dialog, Settings) against a seeded dev box:
//   SCREENS=1 E2E_BASE_URL=http://localhost:5391 E2E_OWNER_TOKEN=... bunx playwright test screens-simple
// It opens the confirm dialog for turning a service off and cancels it; it changes nothing.
test.skip(!process.env.SCREENS, "set SCREENS=1 for screenshots");
needsServices("shop", ["postgres"]);

const out = process.env.SHOTS_DIR ?? "screenshots/simple";
const only = process.env.SHOTS?.split(",");

async function shot(page: Page, name: string, fullPage = true) {
  await page.waitForTimeout(700);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
  const wide = await page.evaluate(() => document.documentElement.scrollWidth);
  const vw = page.viewportSize()?.width ?? 0;
  if (wide > vw + 1) console.log(`OVERFLOW ${name}: ${wide}px`);
}

// The empty box, without emptying the shared dev box: the API answers as a fresh box would.
async function asEmptyBox(p: Page) {
  await p.route("**/v1/projects", (r) => r.fulfill({ json: [] }));
  await p.route("**/v1/changes?*", (r) => r.fulfill({ json: [] }));
}

// The Usage tab with the resources backend (in progress): /usage answers as it will, with a 25% limit.
async function withUsage(p: Page) {
  const MB = 1048576;
  await p.route("**/v1/projects/shop/usage", (r) =>
    r.fulfill({
      json: {
        project: "shop",
        budget: { auto: false, maxSharePercent: 25 },
        limitSource: "project",
        memory: { usedBytes: 460 * MB, cacheBytes: 40 * MB, limitBytes: 976 * MB, headroomBytes: 556 * MB, pressure: "none" },
        cpu: { percent: 6, limitCpus: 0.5 },
        disk: { databaseBytes: 9 * MB, filesBytes: 0.02 * MB, kvBytes: 0.003 * MB, totalBytes: 9.1 * MB },
        apps: [],
        services: { postgres: { memoryBytes: 180 * MB }, valkey: { memoryBytes: 12 * MB }, auth: { memoryBytes: 60 * MB } },
        sharePercent: 25,
        database: { cpuPercent: 38, limitCpus: 0.5, connections: 6, connectionLimit: 25, queryTimeLimitSeconds: 30, queriesStoppedToday: 2 },
        cache: { usedBytes: 12 * MB, limitBytes: 64 * MB, enforced: true, writesRefused: false },
        builds: { limitCpus: 0.5, slowDownBytes: 1024 * MB },
        limitEvents: [{ at: new Date().toISOString(), kind: "memory", message: "shop used all of the 976 MB of memory it may use, so an app was stopped and restarted. Give it a bigger limit if it needs more." }],
      },
    }),
  );
}

const pages: Array<{
  name: string;
  url: string;
  stub?: (p: Page) => Promise<unknown>;
  wait: (p: Page) => Promise<unknown>;
  act?: (p: Page) => Promise<unknown>;
  full?: boolean;
}> = [
  { name: "home", url: "/", wait: (p) => p.getByText(/^Your box is/).waitFor() },
  { name: "box-usage", url: "/usage", wait: (p) => p.getByRole("heading", { name: "By project" }).waitFor() },
  {
    name: "account",
    url: "/",
    wait: (p) => p.getByText(/^Your box is/).waitFor(),
    act: async (p) => {
      const nav = p.getByRole("button", { name: "Open navigation" });
      if (await nav.isVisible()) await nav.click();
      await p.getByRole("button", { name: "Account" }).last().click();
      await p.getByRole("menuitem", { name: /Touch ID/ }).waitFor();
    },
    full: false,
  },
  {
    name: "sidebar-project",
    url: "/projects/shop",
    wait: (p) => p.getByRole("heading", { name: "Database" }).waitFor(),
    act: async (p) => {
      const nav = p.getByRole("button", { name: "Open navigation" });
      if (await nav.isVisible()) await nav.click();
    },
    full: false,
  },
  { name: "passkeys", url: "/settings/passkeys", wait: (p) => p.getByRole("heading", { name: "Sign in with Touch ID / Face ID" }).waitFor() },
  { name: "home-empty", url: "/", stub: asEmptyBox, wait: (p) => p.getByText("Your tiffin is packed. Nothing in it yet.").waitFor() },
  { name: "project", url: "/projects/shop", wait: (p) => p.getByRole("heading", { name: "Database" }).waitFor() },
  {
    name: "project-add",
    url: "/projects/notes",
    wait: (p) => p.getByRole("heading", { name: "Files" }).waitFor(),
    act: (p) => p.getByRole("button", { name: /^Add/ }).last().click(),
    full: false,
  },
  { name: "observability", url: "/projects/shop/observability", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
  { name: "observability-limit", url: "/projects/shop/observability", stub: withUsage, wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
  { name: "history", url: "/projects/shop/history", wait: (p) => p.getByRole("heading", { name: "History" }).waitFor() },
  { name: "project-settings", url: "/projects/shop/settings", wait: (p) => p.getByRole("heading", { name: "Built-in parts" }).waitFor() },
  {
    name: "confirm",
    url: "/projects/shop/settings",
    wait: (p) => p.getByRole("heading", { name: "Built-in parts" }).waitFor(),
    act: async (p) => {
      await p.getByRole("switch", { name: /^Analytics: on/ }).click();
      await p.getByRole("dialog").getByText("This can’t be undone.").waitFor();
    },
    full: false,
  },
  { name: "new", url: "/new", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
  { name: "app", url: "/projects/shop/apps/web", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
  { name: "data", url: "/projects/shop/data", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
  { name: "settings", url: "/settings", wait: (p) => p.getByRole("heading", { name: "Settings" }).waitFor() },
  { name: "machine", url: "/settings/box", wait: (p) => p.getByText("In use", { exact: true }).waitFor() },
  { name: "keys", url: "/settings/keys", wait: (p) => p.getByRole("heading", { name: "API keys" }).waitFor() },
  {
    name: "key-create",
    url: "/settings/keys?create=true",
    wait: (p) => p.getByRole("dialog").getByText("Expires").waitFor(),
    full: false,
  },
  { name: "ledger", url: "/ledger", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
];

for (const theme of ["light", "dark"] as const) {
  for (const size of [
    { name: "1440", width: 1440, height: 900 },
    { name: "390", width: 390, height: 844 },
  ]) {
    test(`simple screens ${theme} ${size.name}`, async ({ page, baseURL }) => {
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
        if (pg.stub) await pg.stub(page);
        await page.goto(pg.url);
        await pg.wait(page);
        if (pg.act) await pg.act(page);
        await shot(page, `${pg.name}-${theme}-${size.name}`, pg.full ?? true);
        await page.keyboard.press("Escape");
        if (pg.stub) await page.unrouteAll({ behavior: "ignoreErrors" });
      }
      // Signed out: the login page, Touch ID first and the sign-in link as the fallback.
      if (!only || only.includes("login")) {
        await page.context().clearCookies();
        await page.goto("/login");
        await page.getByRole("heading", { level: 1 }).waitFor();
        await shot(page, `login-${theme}-${size.name}`, false);
      }
    });
  }
}
