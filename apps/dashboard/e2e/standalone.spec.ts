import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// New project › no code, only Database ticked: a project with only a database, which
// opens on its Database page with a sidebar of just that part. It creates a
// project on the throwaway box, so it runs after the specs that count them.
test("No code and just a database makes a database-only project that reads like a console", async ({ page, baseURL }) => {
  page.setDefaultTimeout(20_000);
  await signIn(page, baseURL!);
  await page.goto("/new");
  await page.getByRole("radio", { name: /No code yet/ }).click();
  await expect(page.getByRole("checkbox", { name: /Database/ })).toBeChecked();
  const name = `solo${Date.now() % 100000}`;
  await page.locator("#pname").fill(name);
  const plan = page.getByRole("complementary", { name: "The plan" });
  await expect(plan).toContainText("Two things, ready in seconds.");
  await expect(plan).toContainText("Postgres 18");
  const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"]).analyze();
  expect(r.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => v.id)).toEqual([]);
  await plan.getByRole("button", { name: `Create ${name}` }).click();

  await expect(page).toHaveURL(new RegExp(`/projects/${name}/data$`));
  const nav = page.getByRole("navigation", { name: "Main" });
  await expect(nav.getByRole("link", { name: "Database" })).toBeVisible();
  await expect(nav.getByRole("link", { name: "Overview" })).toHaveCount(0);
  for (const part of ["App", "Apps", "KV", "Files", "Jobs", "Email"]) await expect(nav.getByRole("link", { name: part, exact: true })).toHaveCount(0);

  // Its manifest holds only the database.
  const m = await (await page.request.get(`/v1/projects/${name}/manifest`)).json();
  expect(Object.keys(m.manifest.services ?? {})).toEqual(["postgres"]);
  expect(m.manifest.apps ?? {}).toEqual({});

  // From Projects, it opens straight on its database too.
  await page.goto("/");
  await page.getByRole("link", { name, exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/projects/${name}/data$`));
});
