import { mkdirSync } from "node:fs";
import { test, type Page } from "@playwright/test";
import { signIn } from "./helpers";

// Visual review of the Fusion foundation (the Box, the plan tray, /_kit and a
// few older pages inside the new shell) against a seeded dev box:
//   SCREENS=1 E2E_BASE_URL=http://localhost:5391 E2E_OWNER_TOKEN=... bunx playwright test screens-fusion
// It stages a change but never applies it.
test.skip(!process.env.SCREENS || !process.env.E2E_OWNER_TOKEN, "set SCREENS=1 and E2E_OWNER_TOKEN for a seeded box");

const out = process.env.SHOTS_DIR ?? "screenshots/fusion";
const only = process.env.SHOTS?.split(",");

async function shot(page: Page, name: string, fullPage = true) {
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
  const wide = await page.evaluate(() => document.documentElement.scrollWidth);
  const vw = page.viewportSize()?.width ?? 0;
  if (wide > vw + 1) console.log(`OVERFLOW ${name}: ${wide}px`);
}

// The empty box, without emptying the shared dev box: the API answers as a fresh box would.
async function asEmptyBox(p: Page) {
  await p.route("**/v1/projects", (r) => r.fulfill({ json: [] }));
  await p.route("**/v1/changes?*", (r) => r.fulfill({ json: [] }));
  await p.route("**/v1/approvals*", (r) => r.fulfill({ json: [] }));
}

const pages: Array<{
  name: string;
  url: string;
  stub?: (p: Page) => Promise<unknown>;
  wait: (p: Page) => Promise<unknown>;
  act?: (p: Page) => Promise<unknown>;
  full?: boolean;
}> = [
  { name: "box-empty", url: "/", stub: asEmptyBox, wait: (p) => p.getByRole("heading", { name: "Start a project" }).waitFor() },
  { name: "box", url: "/", wait: (p) => p.getByText("In use", { exact: true }).waitFor() },
  {
    name: "tray",
    url: "/",
    wait: (p) => p.getByText("In use", { exact: true }).waitFor(),
    act: async (p) => {
      await p.getByRole("slider", { name: "web instances" }).focus();
      await p.keyboard.press("ArrowRight");
      await p.getByRole("button", { name: "Review" }).click();
      await p.getByText("What will happen, in order").waitFor();
      await p.locator(".diff .ln").first().waitFor();
    },
    full: false,
  },
  {
    name: "tray-irreversible",
    url: "/",
    wait: (p) => p.getByText("In use", { exact: true }).waitFor(),
    act: async (p) => {
      await p.getByRole("region", { name: "Project shop" }).getByRole("switch", { name: /^Analytics: on/ }).click();
      await p.getByRole("button", { name: "Review" }).click();
      await p.getByText("What will happen, in order").waitFor();
      await p.getByLabel(/Type shop to arm/).fill("shop");
      await p.locator(".diff .ln").first().waitFor();
    },
    full: false,
  },
  { name: "kit", url: "/_kit", wait: (p) => p.getByText("Light, the hero").first().waitFor() },
  { name: "ledger", url: "/ledger", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
  { name: "status", url: "/status", wait: (p) => p.getByRole("heading", { name: "Checks" }).waitFor() },
  { name: "storage", url: "/projects/shop/storage", wait: (p) => p.getByRole("heading", { name: "Buckets" }).waitFor() },
  { name: "project", url: "/projects/shop", wait: (p) => p.getByRole("heading", { name: "Apps" }).waitFor() },
  { name: "settings", url: "/settings", wait: (p) => p.getByText("Project colours").waitFor() },
  { name: "approvals", url: "/approvals", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
];

for (const theme of ["light", "dark"] as const) {
  for (const size of [
    { name: "1440", width: 1440, height: 900 },
    { name: "390", width: 390, height: 844 },
  ]) {
    test(`fusion screens ${theme} ${size.name}`, async ({ page, baseURL }) => {
      test.setTimeout(240_000);
      mkdirSync(out, { recursive: true });
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      page.setDefaultTimeout(20_000);
      page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && console.log(`CONSOLE ${m.text()}`));
      page.on("pageerror", (e) => console.log(`PAGEERROR ${e.message}`));
      await signIn(page, baseURL!);
      await page.evaluate(() => sessionStorage.removeItem("tiffin.staged"));
      for (const pg of pages) {
        if (only && !only.includes(pg.name)) continue;
        if (pg.stub) await pg.stub(page);
        await page.goto(pg.url);
        await pg.wait(page);
        if (pg.act) await pg.act(page);
        await shot(page, `${pg.name}-${theme}-${size.name}`, pg.full ?? true);
        await page.evaluate(() => sessionStorage.removeItem("tiffin.staged"));
        if (pg.stub) await page.unrouteAll({ behavior: "ignoreErrors" });
      }
    });
  }
}
