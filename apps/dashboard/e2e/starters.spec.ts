import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { pick, signIn } from "./helpers";

// New project's code step: three kinds (Web app, Static site, API), each
// with a quiet framework choice that drives what the project needs. Creates
// nothing on the box.
//   STARTERS_FIXTURE=templates.json   serves GET /v1/templates from a file (a box built before kinds existed)

async function open(page: Page, baseURL: string, query = "") {
  const fixture = process.env.STARTERS_FIXTURE;
  if (fixture) await page.route("**/v1/templates", (r) => r.fulfill({ contentType: "application/json", body: readFileSync(fixture, "utf8") }));
  await signIn(page, baseURL);
  await page.goto(`/new${query}`);
  await expect(page.locator("[data-kind=api]")).toContainText("Hono"); // the starters are in
}

const kind = (page: Page, k: string) => page.locator(`[data-kind=${k}]`);
const database = (page: Page) => page.getByRole("checkbox", { name: /Database/ });

test("a kind's framework is a quiet choice that drives what it needs", async ({ page, baseURL }) => {
  await open(page, baseURL!);
  // Web app is picked, with Next.js, which needs a database.
  await expect(kind(page, "web").getByRole("radio")).toBeChecked();
  await expect(kind(page, "web").getByRole("combobox")).toHaveText("Next.js");
  await expect(kind(page, "static")).toContainText("Astro");
  await expect(kind(page, "api")).toContainText("Hono");
  await expect(database(page)).toBeChecked();
  await expect(database(page)).toBeDisabled();
  await expect(page.getByText("The Next.js starter uses it")).toBeVisible();

  await pick(page, kind(page, "web").getByRole("combobox"), "TanStack Start");
  await expect(page.getByText("The TanStack Start starter uses it")).toBeVisible();
  await expect(page.getByLabel("The plan")).toContainText("TanStack Start");

  // A static site needs nothing; its default is Astro, and Vite + React is the other choice.
  await kind(page, "static").getByRole("radio").click();
  await expect(kind(page, "static").getByRole("combobox")).toHaveText("Astro");
  await expect(database(page)).not.toBeChecked();
  await expect(page.getByLabel("The plan")).toContainText("Astro, built to static files");
  await pick(page, kind(page, "static").getByRole("combobox"), "Vite + React");
  await expect(page.getByLabel("The plan")).toContainText("Vite + React");

  // An API defaults to Hono (with one framework it's a label, not a dropdown). Coming back keeps the web app's pick.
  await kind(page, "api").getByRole("radio").click();
  await expect(kind(page, "api")).toContainText("Hono");
  await kind(page, "web").getByRole("radio").click();
  await expect(kind(page, "web").getByRole("combobox")).toHaveText("TanStack Start");
});

test("a link names a kind or a starter", async ({ page, baseURL }) => {
  for (const [q, k, fw] of [
    ["static", "static", "Astro"],
    ["vite-react", "static", "Vite + React"],
    ["tanstack-start", "web", "TanStack Start"],
    ["nextjs", "web", "Next.js"],
    ["sveltekit", "web", "SvelteKit"],
    ["react-router", "web", "React Router"],
    ["nuxt", "web", "Nuxt"],
  ]) {
    await open(page, baseURL!, `?starter=${q}`);
    await expect(kind(page, k).getByRole("radio")).toBeChecked();
    await expect(kind(page, k).getByRole("combobox")).toHaveText(fw);
  }
});
