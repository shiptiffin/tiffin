import { existsSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { APIRequestContext, Page } from "@playwright/test";

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
