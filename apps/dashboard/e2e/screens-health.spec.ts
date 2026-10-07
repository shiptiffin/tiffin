import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { test, type Page } from "@playwright/test";
import { needsServices, signIn } from "./helpers";

// Visual review of Health, Access and Settings on the Fusion against a seeded dev box:
//   SCREENS=1 E2E_BASE_URL=http://localhost:5405 E2E_OWNER_TOKEN=... bunx playwright test screens-health
// Narrow it with SHOTS=status,logs THEMES=light SIZES=1440. It reads; it never applies, restores or imports.
test.skip(!process.env.SCREENS, "set SCREENS=1 for screenshots");
needsServices("shop", ["postgres"]);

const out = process.env.SHOTS_DIR ?? "screenshots/fusion";
const only = process.env.SHOTS ? process.env.SHOTS.split(",") : undefined;
const themes = (process.env.THEMES?.split(",") ?? ["light", "dark"]) as Array<"light" | "dark">;
const sizes = [
  { name: "1440", width: 1440, height: 900 },
  { name: "390", width: 390, height: 844 },
].filter((s) => !process.env.SIZES || process.env.SIZES.split(",").includes(s.name));

async function shot(page: Page, name: string, fullPage = true) {
  await page.waitForTimeout(700);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
  const wide = await page.evaluate(() => document.documentElement.scrollWidth);
  const vw = page.viewportSize()?.width ?? 0;
  if (wide > vw + 1) console.log(`OVERFLOW ${name}: ${wide}px`);
}

const h1 = (p: Page) => p.getByRole("heading", { level: 1 }).first().waitFor();

const pages: Array<{ name: string; url: string; wait: (p: Page) => Promise<unknown>; act?: (p: Page) => Promise<unknown>; full?: boolean }> = [
  { name: "health", url: "/status", wait: (p) => p.getByRole("heading", { name: "Checks", exact: true }).waitFor() },
  { name: "metrics", url: "/metrics", wait: (p) => p.getByRole("heading", { name: "Services", exact: true }).waitFor() },
  { name: "logs", url: "/logs?project=shop", wait: (p) => p.locator("[data-log-line]").first().waitFor(), full: false },
  { name: "errors", url: "/errors", wait: h1 },
  {
    name: "issue",
    url: "/errors",
    wait: h1,
    act: async (p) => {
      await p.locator("a[href^='/errors/iss_']", { hasText: "TypeError" }).first().click();
      await p.getByRole("heading", { name: "Stack trace", exact: true }).waitFor();
    },
  },
  { name: "alerts", url: "/alerts", wait: (p) => p.getByRole("heading", { name: "Rules", exact: true }).waitFor() },
  {
    name: "alert-edit",
    url: "/alerts",
    wait: (p) => p.getByRole("heading", { name: "Rules", exact: true }).waitFor(),
    act: (p) => p.getByRole("button", { name: "New rule" }).click(),
    full: false,
  },
  { name: "protect", url: "/protect", wait: (p) => p.getByRole("heading", { name: "Banned right now", exact: true }).waitFor() },
  {
    name: "protect-armed",
    url: "/protect",
    wait: (p) => p.getByRole("heading", { name: "Banned right now", exact: true }).waitFor(),
    act: (p) => p.getByRole("button", { name: "Turn on…" }).click(),
    full: false,
  },
  { name: "errors-empty", url: "/errors?project=notes", wait: (p) => p.getByText("No open errors in notes.").waitFor(), full: false },
  { name: "backups", url: "/backups", wait: (p) => p.getByRole("heading", { name: "History", exact: true }).waitFor() },
  { name: "keys", url: "/settings/keys", wait: (p) => p.getByRole("heading", { name: "API keys" }).waitFor() },
  { name: "key-create", url: "/settings/keys?create=true", wait: (p) => p.getByRole("dialog").getByText("Expires").waitFor(), full: false },
  { name: "people", url: "/settings/people", wait: h1 },
  { name: "passkeys", url: "/settings/passkeys", wait: h1 },
  { name: "settings", url: "/settings", wait: (p) => p.getByText("Look and sound").waitFor() },
];

for (const theme of themes) {
  for (const size of sizes) {
    test(`health screens ${theme} ${size.name}`, async ({ page, baseURL }) => {
      test.setTimeout(300_000);
      mkdirSync(out, { recursive: true });
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      page.setDefaultTimeout(20_000);
      page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && console.log(`CONSOLE ${m.text()}`));
      page.on("pageerror", (e) => console.log(`PAGEERROR ${e.message}`));
      // One sign-in per run, not per test: every sign-in is a browser session the Tokens page lists.
      const state = process.env.SHOTS_STATE;
      if (state && existsSync(state)) await page.context().addCookies(JSON.parse(readFileSync(state, "utf8")));
      else {
        await signIn(page, baseURL!);
        if (state) writeFileSync(state, JSON.stringify(await page.context().cookies()));
      }
      for (const pg of pages) {
        if (only && !only.includes(pg.name)) continue;
        await page.goto(pg.url);
        await pg.wait(page);
        if (pg.act) await pg.act(page);
        await shot(page, `${pg.name}-${theme}-${size.name}`, pg.full ?? true);
      }
    });
  }
}
