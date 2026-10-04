import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

// One walk through the whole dashboard against a seeded box (e2e/seed.sh),
// served from the binary with its real CSP.
test("login → projects → history → change → undo → health → keys → sign out", async ({ page, baseURL }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    const t = m.text();
    // 4xx answers (428 plans, 409 conflicts) are expected and logged by Chrome.
    if (m.type() === "error" && !t.startsWith("Failed to load resource")) problems.push(t);
  });

  // Login without a code explains how to get one.
  await page.goto("/login");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(/^Sign in (to your box|with a link from your terminal)\.$/);
  await expect(page.getByText("tiffin login")).toBeVisible();

  // A used or made-up code is refused kindly.
  await page.goto("/login#tfl_not-a-real-code");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("That link has been used, or it expired.");
  await expect(page).toHaveURL(/\/login$/); // the code never stays in the address bar

  // A real one signs in and lands on Projects: a card per project.
  await signIn(page, baseURL!);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Projects");
  await expect(page).toHaveTitle(/Projects · Tiffin$/);
  await expect(page.getByRole("list", { name: "Projects" }).getByRole("link", { name: "hello", exact: true })).toBeVisible();
  // Every project's history lives in Settings.
  await page.getByRole("link", { name: "Settings" }).first().click();
  await page.getByRole("link", { name: "History" }).first().click();
  await expect(page).toHaveURL(/\/ledger$/);
  await expect(page.getByRole("heading", { level: 1 })).toContainText(/changes (today |since [\w ]+? )?across two projects/);
  await expect(page).toHaveTitle(/History · Tiffin$/);
  await expect(page.getByRole("heading", { name: /Today/ })).toBeVisible();

  // Filtering by risk.
  await page.getByRole("button", { name: /Irreversible/ }).click();
  await expect(page.getByRole("link", { name: /Drop the uploads bucket/ })).toBeVisible();
  await expect(page.getByRole("link", { name: /Scale the API/ })).toHaveCount(0);
  await page.getByRole("button", { name: /Irreversible/ }).click();

  // Change detail: intent, plan hash, op with reason and diff.
  await page.getByRole("link", { name: /Drop the uploads bucket/ }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Drop the uploads bucket; files moved to the notes project.");
  await expect(page.getByText("Times and numbers")).toBeVisible();
  await expect(page.getByText(/Deletes bucket “uploads” and every file in it/).first()).toBeVisible();
  await expect(page.getByText("What undo can’t restore")).toBeVisible();

  // Undo: review the plan first, then confirm it.
  await page.getByRole("button", { name: "Undo this change" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Review the undo" })).toBeVisible();
  await expect(dialog.getByText("Add the uploads bucket")).toBeVisible();
  await expect(dialog.getByText("The original change deleted data.")).toBeVisible();
  await dialog.getByRole("button", { name: "Confirm undo" }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByText("Undone by")).toBeVisible();
  await expect(page.getByRole("button", { name: "Undo this change" })).toHaveCount(0);

  // Undoing something newer work depends on is refused with a reason (409).
  await page.goto("/ledger");
  await page.getByRole("link", { name: /Set up the hello project/ }).click();
  await page.getByRole("button", { name: "Undo this change" }).click();
  await expect(page.getByRole("dialog").getByText("Something changed since, so undo would overwrite newer work")).toBeVisible();
  await page.getByRole("button", { name: "Close", exact: true }).first().click();

  // An undo that destroys data asks you to type the project name.
  await page.goto("/ledger");
  await page.getByRole("link", { name: /Add a thumbnails bucket/ }).click();
  await page.getByRole("button", { name: "Undo this change" }).click();
  await expect(page.getByRole("heading", { name: "This undo destroys data" })).toBeVisible();
  const destroy = page.getByRole("button", { name: "Destroy and undo" });
  await expect(destroy).toBeDisabled();
  await page.getByLabel("Type notes to confirm").fill("notes");
  await expect(destroy).toBeEnabled();
  await page.keyboard.press("Escape");

  // Health (Settings › Health).
  await page.getByRole("link", { name: "Health" }).first().click();
  await expect(page.getByRole("heading", { level: 1 })).toContainText(/Nothing is wrong|is fine|failing|firing/);
  await expect(page.getByText("Platform state readable.")).toBeVisible();
  await expect(page).toHaveTitle(/Health · Tiffin$/);

  // Command palette navigates.
  await page.keyboard.press("ControlOrMeta+k");
  await page.getByRole("combobox", { name: "Command palette" }).fill("keys for agents");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/settings\/keys$/);

  // API keys: create shows the secret once; revoke asks first.
  await page.getByRole("button", { name: "Create key" }).click();
  await page.getByLabel("Name").fill("smoke-agent");
  await page.getByRole("dialog").getByRole("radio", { name: "Read only" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Create smoke-agent" }).click();
  await expect(page.getByTestId("token-secret")).toHaveText(/^tfn_/);
  await page.getByRole("button", { name: "I’ve stored it" }).click();
  const row = page.getByRole("listitem").filter({ hasText: "Smoke Agent" });
  await expect(row).toContainText("Read only");
  await row.getByRole("button", { name: "Revoke smoke-agent" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Revoke Smoke Agent" }).click();
  await expect(page.getByRole("listitem").filter({ hasText: "Smoke Agent" })).toHaveCount(0);

  // Sign out; afterwards any page sends you back to /login with a kind word.
  await page.getByRole("button", { name: "Account" }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Signed out. See you soon.");
  await page.goto("/status");
  await expect(page).toHaveURL(/\/login\?reason=session/);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Your session has ended.");

  expect(problems, problems.join("\n")).toEqual([]);
});

// Passkeys for sign-in, then a project's overview, its secrets, and people.
test("passkey → project → secrets → people → passkey sign-in", async ({ page, baseURL }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));
  page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && problems.push(m.text()));

  const cdp = await page.context().newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true },
  });

  await signIn(page, baseURL!);

  // Add a passkey.
  await page.goto("/settings/passkeys");
  await page.getByLabel("Passkey name").fill("Test key");
  await page.getByRole("button", { name: "Add a passkey" }).click();
  await expect(page.getByText("Test key")).toBeVisible();

  // A project's overview: what's in it, as tiles.
  await page.goto("/projects/hello");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("hello");
  await expect(page.getByRole("list", { name: "What’s in hello" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Database" })).toBeVisible();

  // Secrets are write-only (Settings › Secrets).
  await page.getByRole("link", { name: "Settings" }).first().click();
  await page.getByRole("link", { name: /^Secrets/ }).click();
  await expect(page.getByText("STRIPE_SECRET_KEY")).toBeVisible();
  await page.getByLabel("Name").fill("smoke_token");
  await expect(page.getByLabel("Name")).toHaveValue("SMOKE_TOKEN");
  await page.getByRole("textbox", { name: "Value" }).fill("s3cret");
  await page.getByRole("button", { name: "Save SMOKE_TOKEN" }).click();
  await expect(page.getByText("SMOKE_TOKEN", { exact: true })).toBeVisible();
  await expect(page.getByText("s3cret")).toHaveCount(0);
  await page.getByRole("button", { name: "Delete SMOKE_TOKEN" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete SMOKE_TOKEN" }).click();
  await expect(page.getByText("SMOKE_TOKEN", { exact: true })).toHaveCount(0);

  // People: invite with a role, get a one-time link.
  await page.goto("/settings/people");
  await page.getByRole("button", { name: "Invite someone" }).click();
  await page.getByLabel("Name").fill("Ada");
  await page.getByRole("radio", { name: /Viewer/ }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Invite Ada" }).click();
  await expect(page.getByText(/\/login#tfl_/)).toBeVisible();
  await page.getByRole("button", { name: "Done" }).click();
  await expect(page.getByText("Ada")).toBeVisible();

  // The passkey added above also signs in: sign out, sign back in with it, land where you were going.
  await page.request.delete("/v1/session");
  await page.goto("/settings/passkeys");
  await expect(page).toHaveURL(/\/login\?reason=session&next=%2Fsettings%2Fpasskeys/);
  await page.getByRole("button", { name: "Sign in with a passkey" }).click();
  await expect(page).toHaveURL(/\/settings\/passkeys$/);
  await expect(page.getByText("Test key")).toBeVisible();

  expect(problems, problems.join("\n")).toEqual([]);
});

// Changes happen when you click: the copies stepper applies at once, the
// toast's Undo puts it back, and only what deletes data asks first.
test("usage → copies apply at once → undo → removing a database asks first", async ({ page, baseURL }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));
  page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && problems.push(m.text()));
  await signIn(page, baseURL!);

  await page.goto("/projects/notes/usage");
  await page.getByRole("button", { name: "Advanced" }).click();
  const search = page.getByRole("spinbutton", { name: "search copies" });
  await expect(search).toHaveAttribute("aria-valuenow", "1");
  await search.focus();
  await page.keyboard.press("ArrowUp");
  await expect(search).toHaveAttribute("aria-valuenow", "2");

  const toast = page.getByRole("status").filter({ hasText: "search runs on 2 copies now." });
  await expect(toast).toBeVisible();
  await expect(search).toHaveAttribute("aria-valuenow", "2");
  await expect(search).not.toHaveAttribute("aria-busy", "true");
  await toast.getByRole("button", { name: "Undo" }).click();
  await expect(page.getByText("Undone. Everything is back as it was.")).toBeVisible();
  await expect(search).toHaveAttribute("aria-valuenow", "1");

  // The project's History says it in plain words; a change and its undo fold away together.
  await page.goto("/projects/notes/history");
  await expect(page.getByRole("link", { name: /Run search on 2 copies instead of 1/ })).toHaveCount(0);
  await page.getByRole("button", { name: /that (was|were) undone/ }).click();
  await expect(page.getByRole("link", { name: "Run search on 2 copies instead of 1." })).toBeVisible();

  // Turning the database off would delete data: the dialog says what, and Cancel leaves it on.
  await page.goto("/projects/notes/settings");
  await page.getByRole("switch", { name: /^Database: on/ }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Remove the database from notes?" })).toBeVisible();
  await expect(dialog.getByText("This can’t be undone.")).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Delete for good" })).toBeDisabled();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("switch", { name: /^Database: on/ })).toHaveAttribute("aria-checked", "true");

  expect(problems, problems.join("\n")).toEqual([]);
});
