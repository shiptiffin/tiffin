import { mkdirSync } from "node:fs";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// Licenses (AGPL-3.0 section 13): the account menu reaches it, it links the
// source of this build and shows the third-party notices the binary carries.
//   SHOTS_DIR=/tmp/shots bunx playwright test licenses   also writes screenshots
test("licenses and source", async ({ page, baseURL }) => {
  await signIn(page, baseURL!);
  await page.getByRole("button", { name: "Account" }).click();
  await page.getByRole("menuitem", { name: "Licenses and source" }).click();
  await page.waitForURL((u) => u.pathname === "/licenses");
  await expect(page.getByRole("heading", { level: 1, name: "Licenses" })).toBeVisible();
  await expect(page.getByText("GNU Affero General Public License v3.0 (AGPL-3.0-only)")).toBeVisible();
  await expect(page.getByRole("link", { name: /^shiptiffin\/tiffin/ })).toHaveAttribute("href", /^https:\/\/github\.com\/shiptiffin\/tiffin/);
  await expect(page.getByText("THIRD-PARTY SOFTWARE IN TIFFIN")).toBeVisible();

  const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
  expect(r.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);

  const dir = process.env.SHOTS_DIR;
  if (dir) {
    mkdirSync(dir, { recursive: true });
    for (const scheme of ["light", "dark"] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height: 900 });
        await page.screenshot({ path: `${dir}/licenses-${width}-${scheme}.png` });
      }
    }
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/settings");
    await page.getByRole("heading", { name: "Updates" }).scrollIntoViewIfNeeded();
    await page.screenshot({ path: `${dir}/settings-updates.png` });
  }

  const text = await page.request.get("/licenses.txt");
  expect(text.headers()["content-type"]).toContain("text/plain");
  expect(await text.text()).toContain("Source code of this version: https://github.com/shiptiffin/tiffin");
});
