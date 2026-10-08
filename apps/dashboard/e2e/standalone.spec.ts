import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// New project › no code: a project with no apps. There is nothing to pick:
// every project has its Database, KV, Files, Email, Analytics and Jobs. Its
// sidebar shows only what works without an app. It creates a project on the
// throwaway box, so it runs after the specs that count them.
test("No code makes a project with its data parts and nothing to pick", async ({ page, baseURL }) => {
  page.setDefaultTimeout(20_000);
  await signIn(page, baseURL!);
  await page.goto("/new");
  await page.getByRole("radio", { name: /No code yet/ }).click();
  await expect(page.getByRole("checkbox", { name: /Database/ })).toHaveCount(0);
  const name = `solo${Date.now() % 100000}`;
  await page.locator("#pname").fill(name);
  const plan = page.getByRole("complementary", { name: "The plan" });
  await expect(plan).toContainText("ready in seconds");
  await expect(plan).toContainText("Postgres 18");
  const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"]).analyze();
  expect(r.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);
  await plan.getByRole("button", { name: `Create ${name}` }).click();

  // Its manifest has no app; the always-there parts are in it.
  await expect.poll(async () => (await page.request.get(`/v1/projects/${name}/manifest`)).status()).toBe(200);
  const m = await (await page.request.get(`/v1/projects/${name}/manifest`)).json();
  expect(Object.keys(m.manifest.services ?? {}).sort()).toEqual(["analytics", "email", "postgres", "storage", "valkey"]);
  expect(m.manifest.apps ?? {}).toEqual({});

  // It opens on its Overview, with a sidebar of what works without an app.
  await page.goto("/");
  await page.getByRole("link", { name, exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/projects/${name}$`));
  const nav = page.getByRole("navigation", { name: "Main" });
  for (const part of ["Overview", "Database", "KV", "Files", "Jobs", "Activity", "Settings"]) await expect(nav.getByRole("link", { name: part, exact: true })).toBeVisible();
  for (const part of ["Deployments", "Email", "Auth"]) await expect(nav.getByRole("link", { name: part, exact: true })).toHaveCount(0);
});
