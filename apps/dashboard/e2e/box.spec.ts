import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// The module pages against a real, seeded dev box (tiffin up + e2e/seed-box*.sh):
//   E2E_BOX=1 E2E_BASE_URL=https://dashboard.tiffin.localhost:18448 E2E_OWNER_TOKEN=... bunx playwright test box
// It changes things on that box (uploads, under-attack mode), so never point it at a box you care about.
test.skip(!process.env.E2E_BOX || !process.env.E2E_OWNER_TOKEN, "set E2E_BOX=1 and E2E_OWNER_TOKEN for a seeded dev box");

test("modules: storage, data, email, queues, workflows, users, analytics, protection", async ({ page, baseURL }) => {
  test.setTimeout(180_000);
  page.setDefaultTimeout(20_000);
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    const t = m.text();
    // Remote images in captured mail are blocked on purpose (CSP).
    if (m.type() === "error" && !t.startsWith("Failed to load resource") && !t.includes("tracker.example")) problems.push(t);
  });

  await signIn(page, baseURL!);
  await page.goto("/ledger");
  await expect(page.getByRole("heading", { level: 1 })).toContainText("changes across");

  // Storage: upload into a folder, preview it, delete it.
  await page.goto("/projects/shop/storage/uploads?prefix=notes%2F");
  await page.locator("input[type=file]").setInputFiles({ name: "hello-from-e2e.txt", mimeType: "text/plain", buffer: Buffer.from("Hello from the dashboard e2e") });
  await page.getByRole("button", { name: /^hello-from-e2e.txt / }).click();
  await expect(page.getByText("Hello from the dashboard e2e")).toBeVisible();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete file" }).click();
  await expect(page.getByRole("button", { name: /^hello-from-e2e.txt / })).toHaveCount(0);

  // Data: a read-only query.
  await page.goto("/projects/shop/data/sql");
  await page.getByLabel("SQL").fill("SELECT count(*) AS orders FROM orders");
  await page.getByRole("button", { name: "Run", exact: true }).click();
  await expect(page.getByRole("cell", { name: "1180" })).toBeVisible();

  // Email: the dev inbox, a message and its links.
  await page.goto("/projects/shop/email");
  await expect(page.getByText("Nothing leaves this box.")).toBeVisible();
  await page.getByRole("button", { name: /Your sign-in link/ }).click();
  await page.getByRole("button", { name: /Links/ }).click();
  await expect(page.locator("code", { hasText: "auth/verify?token=" })).toBeVisible();

  // Queues and a workflow run.
  await page.goto("/projects/shop/queues");
  await expect(page.getByRole("link", { name: "emails: its jobs" })).toBeVisible();
  await page.goto("/projects/shop/workflows?state=completed");
  await page.getByText("fulfil-order").first().click();
  await expect(page.getByRole("heading", { name: "Steps" })).toBeVisible();

  // The app's own users.
  await page.goto("/projects/shop/users");
  await page.getByLabel("Search users").fill("ada");
  await expect(page.getByText("ada.lovelace@example.com")).toBeVisible();

  // Analytics.
  await page.goto("/projects/shop/analytics?period=24h");
  await expect(page.getByText("IP Geolocation by DB-IP").first()).toBeVisible();

  // Protection: the alarm state reaches every page, then goes away.
  await page.goto("/protect");
  await page.getByRole("button", { name: "Lift the guard" }).click();
  await page.getByRole("button", { name: "Turn on for an hour" }).click();
  await expect(page.getByText("Under-attack mode is on.")).toBeVisible();
  await page.goto("/");
  await expect(page.getByText("Under-attack mode is on.")).toBeVisible();
  await expect(page).toHaveTitle(/^Under attack · /);
  await page.goto("/protect");
  await page.getByRole("button", { name: "Turn it off now" }).click();
  await expect(page.getByText(/^Normal\./)).toBeVisible();

  expect(problems, problems.join("\n")).toEqual([]);
});
