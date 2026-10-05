import { mkdirSync } from "node:fs";
import { test, type Page } from "@playwright/test";
import { signIn } from "./helpers";

// Visual review of the moments that sell the product: starting a project,
// the project overview, apps and deploys, and signing in. Against a seeded
// dev box through a local Vite:
//   SCREENS=1 E2E_BASE_URL=http://localhost:5402 E2E_OWNER_TOKEN=... bunx playwright test screens-start
// SHOTS=new,login limits it to some pages; THEMES=light and SIZES=1440 narrow it further.
// It never applies anything (the first-run shot fakes an empty box in the browser only).
test.skip(!process.env.SCREENS || !process.env.E2E_OWNER_TOKEN, "set SCREENS=1 and E2E_OWNER_TOKEN for a seeded box");

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

const failedDeploy = process.env.FAILED_DEPLOY ?? "";
const liveDeploy = process.env.LIVE_DEPLOY ?? ""; // project/app/id

type Shot = { name: string; url: string; wait: (p: Page) => Promise<unknown>; act?: (p: Page) => Promise<unknown>; full?: boolean; before?: (p: Page) => Promise<unknown> };
const pages: Shot[] = [
  { name: "new", url: "/new", wait: (p) => p.getByText(/will be created/).waitFor() },
  {
    name: "new-first",
    url: "/new",
    before: (p) => p.route("**/v1/projects", (r) => r.fulfill({ json: [] })),
    wait: (p) => p.getByText("Your tiffin is packed. Nothing in it yet.").waitFor(),
    // The box isn't really empty: pick a starter whose name is free.
    act: async (p) => {
      await p.getByRole("radio", { name: /^Static site/ }).click();
      await p.getByText(/Three things will be created|Two things will be created/).waitFor();
    },
  },
  {
    name: "new-git",
    url: "/new",
    wait: (p) => p.getByText(/will be created/).waitFor(),
    act: async (p) => {
      await p.getByRole("radio", { name: /From a git URL/ }).click();
      await p.getByPlaceholder("https://github.com/owner/repo").fill("https://github.com/vercel/next-learn");
      await p.getByText(/will be created/).waitFor();
    },
  },
  { name: "project", url: "/projects/shop", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
  { name: "secrets", url: "/projects/shop/secrets", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
  { name: "apps", url: "/projects/shop/apps", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
  { name: "app", url: "/projects/shop/apps/web", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor() },
  {
    name: "deploy-tray",
    url: "/projects/shop/apps/web",
    wait: (p) => p.getByRole("heading", { level: 1 }).waitFor(),
    act: async (p) => {
      await p.getByRole("button", { name: /^Deploy/ }).first().click();
      await p.getByRole("dialog").waitFor();
    },
    full: false,
  },
  ...(failedDeploy ? [{ name: "deploy-failed", url: `/projects/shop/apps/web/deploys/${failedDeploy}`, wait: (p: Page) => p.getByRole("heading", { level: 1 }).waitFor() }] : []),
  ...(liveDeploy
    ? [{ name: "deploy", url: `/projects/${liveDeploy.split("/")[0]}/apps/${liveDeploy.split("/")[1]}/deploys/${liveDeploy.split("/")[2]}`, wait: (p: Page) => p.getByRole("heading", { level: 1 }).waitFor() }]
    : []),
  { name: "app-logs", url: "/projects/shop/apps/web/logs", wait: (p) => p.getByRole("heading", { level: 1 }).waitFor(), full: false },
];

for (const theme of themes) {
  for (const size of sizes) {
    test(`start screens ${theme} ${size.name}`, async ({ page, baseURL, browser }) => {
      test.setTimeout(300_000);
      mkdirSync(out, { recursive: true });
      page.setDefaultTimeout(20_000);
      page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && console.log(`CONSOLE ${m.text()}`));
      page.on("pageerror", (e) => console.log(`PAGEERROR ${e.message}`));

      if (!only || only.includes("login")) {
        // Signed out: a fresh context with no session.
        const ctx = await browser.newContext({ viewport: { width: size.width, height: size.height }, colorScheme: theme, reducedMotion: "reduce", ignoreHTTPSErrors: true, baseURL });
        const lp = await ctx.newPage();
        await lp.goto("/login");
        await lp.getByText("tiffin login").first().waitFor();
        await shot(lp, `login-${theme}-${size.name}`, false);
        await lp.goto("/login?reason=signed-out");
        await lp.getByText("tiffin login").first().waitFor();
        await shot(lp, `login-out-${theme}-${size.name}`, false);
        await ctx.close();
      }

      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      await signIn(page, baseURL!);
      await page.evaluate(() => sessionStorage.removeItem("tiffin.staged"));
      for (const pg of pages) {
        if (only && !only.includes(pg.name)) continue;
        if (pg.before) await pg.before(page);
        await page.goto(pg.url);
        await pg.wait(page);
        if (pg.act) await pg.act(page);
        await shot(page, `${pg.name}-${theme}-${size.name}`, pg.full ?? true);
        await page.unrouteAll({ behavior: "ignoreErrors" });
        await page.evaluate(() => sessionStorage.removeItem("tiffin.staged"));
      }
    });
  }
}

// The whole first minute, for real: FLOW=<name> creates a project from the
// Next.js starter through the page and shoots it building and live.
// It changes the box (one new project), so it only runs when asked.
test("start flow", async ({ page, baseURL }) => {
  test.skip(!process.env.FLOW, "set FLOW=<project name> to create a real project");
  test.setTimeout(180_000);
  const name = process.env.FLOW!;
  const theme = (process.env.THEMES?.split(",")[0] ?? "light") as "light" | "dark";
  mkdirSync(out, { recursive: true });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ colorScheme: theme });
  await signIn(page, baseURL!);
  await page.goto("/new");
  await page.getByRole("radio", { name: new RegExp(`^${process.env.STARTER ?? "Next.js app"}`) }).click();
  await page.getByLabel("Project name").fill(name);
  await page.getByRole("radio", { name: process.env.ENAMEL ?? "Plum" }).click();
  await page.getByRole("button", { name: `Create ${name}` }).waitFor();
  await page.getByText(/will be created/).waitFor();
  const t0 = Date.now();
  await page.getByRole("button", { name: `Create ${name}` }).click();
  await page.getByText("Build log", { exact: true }).waitFor();
  await page.waitForTimeout(Number(process.env.BUILD_WAIT ?? 2500));
  await page.screenshot({ path: `${out}/new-building-${theme}-1440.png` });
  await page.getByRole("heading", { name: `${name} is live.` }).waitFor({ timeout: 120_000 });
  console.log(`LIVE after ${((Date.now() - t0) / 1000).toFixed(1)} s`);
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${out}/new-live-${theme}-1440.png`, fullPage: true });
});
