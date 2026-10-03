import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// One walk through the whole dashboard against a seeded box (e2e/seed.sh),
// served from the binary with its real CSP.
test("login → activity → change → undo → status → tokens → sign out", async ({ page, baseURL }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    const t = m.text();
    // 4xx answers (428 plans, 409 conflicts) are expected and logged by Chrome.
    if (m.type() === "error" && !t.startsWith("Failed to load resource")) problems.push(t);
  });

  // Login without a code explains how to get one.
  await page.goto("/login");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Sign in with a link from your terminal.");
  await expect(page.getByText("tiffin login-link")).toBeVisible();

  // A used or made-up code is refused kindly.
  await page.goto("/login#tfl_not-a-real-code");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("That link has been used, or it expired.");
  await expect(page).toHaveURL(/\/login$/); // the code never stays in the address bar

  // A real one signs in and lands on Activity.
  await signIn(page, baseURL!);
  await expect(page.getByRole("heading", { level: 1 })).toContainText("changes across two projects");
  await expect(page).toHaveTitle("Activity · Tiffin");
  await expect(page.getByRole("heading", { name: /Today/ })).toBeVisible();

  // Filtering by risk.
  await page.getByRole("button", { name: /Irreversible/ }).click();
  await expect(page.getByRole("link", { name: /Drop the uploads bucket/ })).toBeVisible();
  await expect(page.getByRole("link", { name: /Scale the API/ })).toHaveCount(0);
  await page.getByRole("button", { name: /Irreversible/ }).click();

  // Change detail: intent, plan hash, op with reason and diff.
  await page.getByRole("link", { name: /Drop the uploads bucket/ }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Drop the uploads bucket; files moved to the notes project");
  await expect(page.getByText("Plan hash")).toBeVisible();
  await expect(page.getByText('Deletes bucket "uploads" and every file in it')).toBeVisible();
  await expect(page.getByText("This change destroyed data.")).toBeVisible();

  // Undo: review the plan first, then confirm it.
  await page.getByRole("button", { name: "Undo this change" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Review the undo" })).toBeVisible();
  await expect(dialog.getByText("bucket/uploads")).toBeVisible();
  await expect(dialog.getByText("The original change deleted data.")).toBeVisible();
  await dialog.getByRole("button", { name: "Confirm undo" }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByText("Undone by")).toBeVisible();
  await expect(page.getByRole("button", { name: "Undo this change" })).toHaveCount(0);

  // Undoing something newer work depends on is refused with a reason (409).
  await page.goto("/");
  await page.getByRole("link", { name: /Set up the hello project/ }).click();
  await page.getByRole("button", { name: "Undo this change" }).click();
  await expect(page.getByRole("dialog").getByText("Something changed since, so undo would overwrite newer work")).toBeVisible();
  await page.getByRole("button", { name: "Close", exact: true }).first().click();

  // An undo that destroys data asks you to type the project name.
  await page.goto("/");
  await page.getByRole("link", { name: /Add a thumbnails bucket/ }).click();
  await page.getByRole("button", { name: "Undo this change" }).click();
  await expect(page.getByRole("heading", { name: "This undo destroys data" })).toBeVisible();
  const destroy = page.getByRole("button", { name: "Destroy and undo" });
  await expect(destroy).toBeDisabled();
  await page.getByLabel("Type notes to confirm").fill("notes");
  await expect(destroy).toBeEnabled();
  await page.keyboard.press("Escape");

  // Status.
  await page.getByRole("link", { name: "Status" }).first().click();
  await expect(page.getByRole("heading", { level: 1 })).toContainText("All good");
  await expect(page.getByText("platform state readable")).toBeVisible();
  await expect(page).toHaveTitle("Status · Tiffin");

  // Command palette navigates.
  await page.keyboard.press("ControlOrMeta+k");
  await page.keyboard.type("tokens");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/tokens$/);

  // Tokens: create shows the secret once; revoke asks first.
  await page.getByRole("button", { name: "Create token" }).click();
  await page.getByLabel("Name").fill("smoke-agent");
  await page.getByRole("dialog").getByRole("button", { name: "Create token" }).click();
  await expect(page.getByTestId("token-secret")).toHaveText(/^tfn_/);
  await page.getByRole("button", { name: "I've stored it" }).click();
  const row = page.getByRole("listitem").filter({ hasText: "smoke-agent" });
  await expect(row).toBeVisible();
  await row.getByRole("button", { name: "Revoke smoke-agent" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Revoke token" }).click();
  await expect(page.getByRole("listitem").filter({ hasText: "smoke-agent" })).toHaveCount(0);

  // Sign out; afterwards any page sends you back to /login with a kind word.
  await page.getByRole("button", { name: /You/ }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Signed out. See you soon.");
  await page.goto("/status");
  await expect(page).toHaveURL(/\/login\?reason=session/);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Your session has ended.");

  expect(problems, problems.join("\n")).toEqual([]);
});
