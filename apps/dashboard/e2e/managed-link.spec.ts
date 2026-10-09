import { mkdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { signIn } from "./helpers";

// A box ShipTiffin manages links back to the customer's account: an item in
// the account menu and a Plan row in Settings › General. A local box is not
// managed, so /v1/status answers as a managed one would (a fixture; nothing
// on the box changes). SCREENS=1 also saves screenshots to SHOTS_DIR.

const ACCOUNT = "https://shiptiffin.com/account";
const PAUSED = "Automatic updates are paused: this box's ShipTiffin subscription is not active. Your apps keep running. Renew at shiptiffin.com/account.";
const out = process.env.SHOTS_DIR ?? join(homedir(), "Desktop/shiptiffin-readme-preview");

async function asManaged(page: Page, paused = false) {
  await page.route("**/v1/status", async (r) => {
    const res = await r.fetch();
    const json = await res.json();
    await r.fulfill({ response: res, json: { ...json, managed: { account: ACCOUNT, ...(paused ? { paused: PAUSED } : {}) } } });
  });
}

test.beforeEach(async ({ page }) => {
  page.setDefaultTimeout(15_000);
  await page.setViewportSize({ width: 1440, height: 900 });
});

test("a self-hosted box has no ShipTiffin account link", async ({ page, baseURL }) => {
  await signIn(page, baseURL!);
  await page.getByRole("button", { name: "Account" }).click();
  await expect(page.getByRole("menuitem", { name: /Sign out/ })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: /ShipTiffin account/ })).toHaveCount(0);
  await page.keyboard.press("Escape");
  await page.goto("/settings");
  await expect(page.getByText("Tiffin started")).toBeVisible();
  await expect(page.getByText("Managed by ShipTiffin")).toHaveCount(0);
});

for (const theme of ["light", "dark"] as const) {
  test(`a managed box links to its ShipTiffin account (${theme})`, async ({ page, baseURL }) => {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
    await asManaged(page);
    await signIn(page, baseURL!);

    await page.getByRole("button", { name: "Account" }).click();
    const item = page.getByRole("menuitem", { name: /ShipTiffin account/ });
    await expect(item).toHaveAttribute("href", ACCOUNT);
    await expect(item).toHaveAttribute("target", "_blank");
    await expect(item).toHaveAttribute("rel", /noopener/);
    if (process.env.SCREENS) {
      mkdirSync(out, { recursive: true });
      await page.waitForTimeout(400);
      await page.screenshot({ path: `${out}/managed-link-menu-${theme}.png` });
    }
    await page.keyboard.press("Escape");

    await page.goto("/settings");
    const plan = page.getByRole("link", { name: /Manage subscription/ });
    await expect(plan).toHaveAttribute("href", ACCOUNT);
    await expect(page.getByText("Managed by ShipTiffin")).toBeVisible();
    if (process.env.SCREENS) {
      await page.waitForTimeout(400);
      await page.screenshot({ path: `${out}/managed-link-plan-${theme}.png`, clip: { x: 0, y: 0, width: 1440, height: 900 } });
    }
  });
}

test("a lapsed subscription says updates are paused, linking the account", async ({ page, baseURL }) => {
  await page.emulateMedia({ colorScheme: "light", reducedMotion: "reduce" });
  await asManaged(page, true);
  await signIn(page, baseURL!);
  await page.goto("/settings");
  await expect(page.getByRole("status").filter({ hasText: "Automatic updates are paused" })).toBeVisible();
  await expect(page.getByRole("link", { name: /^shiptiffin\.com\/account/ })).toHaveAttribute("href", ACCOUNT);
  if (process.env.SCREENS) {
    mkdirSync(out, { recursive: true });
    await page.waitForTimeout(400);
    await page.screenshot({ path: `${out}/managed-link-plan-paused-light.png`, clip: { x: 0, y: 0, width: 1440, height: 900 } });
  }
});
