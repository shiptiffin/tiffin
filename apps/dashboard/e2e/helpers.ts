import { existsSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test, type APIRequestContext, type Locator, type Page } from "@playwright/test";

export const port = Number(process.env.E2E_PORT ?? 7392);

/** The owner token of the box under test (written by e2e/serve.sh, or given). */
export function ownerToken(): string {
  if (process.env.E2E_OWNER_TOKEN) return process.env.E2E_OWNER_TOKEN;
  const file = process.env.E2E_OWNER_TOKEN_FILE ?? join(boxDir, "box", "owner-token");
  return readFileSync(file, "utf8").trim();
}

export async function loginCode(request: APIRequestContext, baseURL: string): Promise<string> {
  const res = await request.post(`${baseURL}/v1/login-links`, { headers: { Authorization: `Bearer ${ownerToken()}` } });
  if (!res.ok()) throw new Error(`login-links: ${res.status()} ${await res.text()}`);
  return (await res.json()).code;
}

const boxDir = join(tmpdir(), `tiffin-dash-e2e-${port}`);

/** The seeded agents' tokens on the throwaway box (written by e2e/seed.sh): CLAUDE, CODEX. */
export function seedAgents(): Record<string, string> {
  const raw = readFileSync(join(boxDir, "box", "seed-agents.env"), "utf8");
  return Object.fromEntries(raw.split("\n").filter(Boolean).map((l) => l.split(/=(.*)/s).slice(0, 2) as [string, string]));
}

/** Waits until e2e/serve.sh has finished the live seeding. */
async function ready() {
  if (process.env.E2E_BASE_URL) return;
  for (let i = 0; i < 100 && !existsSync(join(boxDir, "ready")); i++) await new Promise((r) => setTimeout(r, 100));
}

/** Signs the page in through the real /login#code flow. */
export async function signIn(page: Page, baseURL: string) {
  await ready();
  const code = await loginCode(page.request, baseURL);
  await page.goto("about:blank");
  await page.goto(`/login#${code}`);
  await page.waitForURL((u) => u.pathname === "/");
}

/**
 * Picks an option in the dashboard's Select: a Radix combobox whose listbox
 * opens in a portal, so the option is looked up on the page, by its exact
 * label (the text people see, not the value).
 */
export async function pick(page: Page, combobox: Locator, option: string) {
  await combobox.click();
  await page.getByRole("listbox").getByRole("option", { name: option, exact: true }).click();
}

/**
 * Skips every test in the file unless `project` exists on the box under test
 * with each of `services` (postgres, valkey, storage...) ready. A Mac's local
 * box (e2e/serve.sh) runs none of them; a dev box seeded by e2e/seed-box.sh
 * runs them all. With no services, the project only has to exist. Call it at
 * the top of a spec, after its opt-in check.
 */
export function needsServices(project: string, services: string[], where = "a dev box seeded by e2e/seed-box.sh") {
  test.beforeAll(async ({ request, baseURL }) => {
    const res = await request.get(`${baseURL}/v1/projects/${project}`, { headers: { Authorization: `Bearer ${ownerToken()}` } });
    const status: Record<string, { state: string }> = res.ok() ? ((await res.json()).status ?? {}) : {};
    const missing = services.filter((s) => status[`service/${s}`]?.state !== "ready");
    const what = res.ok() ? `${project} has no ready ${missing.join(", ")}` : `there is no project ${project}`;
    test.skip(!res.ok() || missing.length > 0, `${what} on ${baseURL}: run this against ${where} (a Mac's local box runs no Postgres, Valkey or storage)`);
  });
}
