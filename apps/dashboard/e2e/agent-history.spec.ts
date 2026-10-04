import { expect, test } from "@playwright/test";
import { seedAgents, signIn } from "./helpers";

// An agent's change is a change like any other: claude-code (an API key)
// sets a setting in notes through the API, the person sees it in the
// project's History under the agent's name, and undoes it from there.
// Runs on the throwaway box Playwright starts (e2e/serve.sh).
test.skip(!!process.env.E2E_BASE_URL, "needs the throwaway box (its seeded agent keys)");

test("an agent changes something; it shows in History by name; the person undoes it", async ({ page, baseURL }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));
  page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && problems.push(m.text()));

  const agent = { Authorization: `Bearer ${seedAgents().CLAUDE}`, "X-Tiffin-Session": "s-51c0d2" };
  const m = (await (await page.request.get(`${baseURL}/v1/projects/notes/manifest`, { headers: agent })).json()).manifest;
  m.env = { ...(m.env ?? {}), PREVIEW_SIZE: "640" };
  const plan = await (await page.request.post(`${baseURL}/v1/plan`, { headers: agent, data: { manifest: m } })).json();
  const intent = "Make link previews 640 px wide";
  const applied = await page.request.post(`${baseURL}/v1/apply`, { headers: agent, data: { manifest: m, confirm: plan.hash, intent } });
  expect(applied.status()).toBe(200);

  await signIn(page, baseURL!);
  await page.goto("/projects/notes/history");
  const row = page.getByRole("listitem").filter({ hasText: intent });
  await expect(row).toContainText("Claude Code");
  await row.getByRole("button", { name: "Undo" }).click();
  await expect(page.getByText("Undone. Everything is back as it was.")).toBeVisible();
  await expect(page.getByRole("listitem").filter({ hasText: intent })).toHaveCount(0);

  expect(problems, problems.join("\n")).toEqual([]);
});
