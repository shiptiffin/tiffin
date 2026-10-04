import { mkdirSync } from "node:fs";
import { test, type Page } from "@playwright/test";
import { signIn } from "./helpers";

// Visual review of Connect GitHub (Settings › Git), Import from GitHub (New
// project) and an app deploying from GitHub, against a box the GitHub e2e
// (e2e/github_test.go) left connected to its fake GitHub:
//   SCREENS=1 E2E_BASE_URL=https://dashboard.tiffin.localhost:8473 E2E_OWNER_TOKEN=... bunx playwright test screens-github
// It changes nothing: the not-connected states are the API's answers replayed.
test.skip(!process.env.SCREENS || !process.env.E2E_OWNER_TOKEN, "set SCREENS=1 and E2E_OWNER_TOKEN for a box connected to GitHub");

const out = process.env.SHOTS_DIR ?? "screenshots/github";

async function shot(page: Page, name: string, fullPage = true) {
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
  const wide = await page.evaluate(() => document.documentElement.scrollWidth);
  const vw = page.viewportSize()?.width ?? 0;
  if (wide > vw + 1) console.log(`OVERFLOW ${name}: ${wide}px`);
}

async function notConnected(p: Page, reachable = true) {
  await p.route("**/v1/github", (r) =>
    r.fulfill({
      json: {
        connected: false,
        shared: false,
        installations: [],
        webhookUrl: "https://dashboard.example.com/v1/github/webhook",
        reachable,
        reachableHint: reachable ? undefined : "GitHub can't reach dashboard.tiffin.localhost:8473 from the internet, so pushes would never arrive. Give the box a public domain first (Settings › Your box › Moving it).",
        githubUrl: "https://github.com",
        canManage: true,
        events: [],
      },
    }),
  );
}

for (const [label, size, scheme] of [
  ["desktop", { width: 1440, height: 1000 }, "light"],
  ["phone", { width: 390, height: 844 }, "light"],
  ["desktop-dark", { width: 1440, height: 1000 }, "dark"],
] as const) {
  test(`github screens ${label}`, async ({ page, baseURL }) => {
    mkdirSync(out, { recursive: true });
    await page.setViewportSize(size);
    await page.emulateMedia({ colorScheme: scheme });
    await signIn(page, baseURL!);

    // Settings › Git, connected (the e2e's state).
    await page.goto("/settings/git");
    await page.getByText("Connected to GitHub.").waitFor();
    await shot(page, `${label}-git-connected`);

    // New project › Import from GitHub: the picker, then a repository's setup.
    await page.goto("/new?starter=github");
    await page.getByRole("option").first().waitFor();
    await shot(page, `${label}-import-picker`);
    await page.getByRole("option").first().click();
    await page.getByText("Production branch").waitFor();
    await page.getByLabel("Built as").waitFor();
    await page.waitForTimeout(1200); // the plan on the right
    await shot(page, `${label}-import-setup`);

    // The app deploying from GitHub.
    await page.goto("/projects/ghshop/apps/site");
    await page.getByText("From GitHub").waitFor();
    await shot(page, `${label}-app`);
    await page.getByRole("link", { name: /Live|Replaced/ }).first().click();
    await page.getByText("Build log").first().waitFor();
    await shot(page, `${label}-deploy`);

    // Not connected yet, and a box GitHub can't reach.
    const p2 = await page.context().newPage();
    await p2.setViewportSize(size);
    await p2.emulateMedia({ colorScheme: scheme });
    await notConnected(p2);
    await p2.goto("/settings/git");
    await p2.getByText("Not connected yet.").waitFor();
    await shot(p2, `${label}-git-not-connected`);
    await p2.goto("/new?starter=github");
    await p2.getByText("Connect GitHub first.").waitFor();
    await shot(p2, `${label}-import-connect-first`);
    await p2.unrouteAll();
    await notConnected(p2, false);
    await p2.goto("/settings/git");
    await p2.getByText("Not connected yet.").waitFor();
    await shot(p2, `${label}-git-unreachable`, false);
    await p2.close();
  });
}
