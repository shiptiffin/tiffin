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

  // Login without a code offers Touch ID, and a sign-in link from the terminal behind a small fallback.
  await page.goto("/login");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(/^Sign in (to your box|with a link from your terminal)\.$/);
  const fallback = page.getByRole("button", { name: "or use a sign-in link" });
  if (await fallback.isVisible()) {
    await expect(page.getByRole("button", { name: "Sign in with Touch ID" })).toBeVisible();
    await fallback.click();
  }
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
  // Every project's changes: Activity, in the sidebar.
  await page.getByRole("link", { name: "Activity" }).first().click();
  await expect(page).toHaveURL(/\/ledger$/);
  await expect(page.getByRole("heading", { level: 1 })).toContainText(/changes (today |since [\w ]+? )?across \w+ projects/);
  await expect(page).toHaveTitle(/Activity · Tiffin$/);
  // The newest day leads (the seeded history is minutes old: today, or yesterday just after midnight).
  await expect(page.getByRole("heading", { name: /^(Today|Yesterday)/ }).first()).toBeVisible();

  // Filtering by risk.
  await page.getByRole("button", { name: /Can’t be undone/ }).click();
  await expect(page.getByRole("link", { name: /Drop the uploads bucket/ })).toBeVisible();
  await expect(page.getByRole("link", { name: /Scale the API/ })).toHaveCount(0);
  await page.getByRole("button", { name: /Can’t be undone/ }).click();

  // Change detail: intent, plan hash, op with reason and diff.
  await page.getByRole("link", { name: /Drop the uploads bucket/ }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Drop the uploads bucket; files moved to the notes project.");
  await expect(page.getByText("Times and numbers")).toBeVisible();
  await expect(page.getByText(/Deletes bucket “uploads” and every file in it/).first()).toBeVisible();
  await expect(page.getByText("What undo can’t restore")).toBeVisible();

  // Undo: review the plan first, then confirm it.
  await page.getByRole("button", { name: "Undo this change" }).click();
  const dialog = page.getByRole("alertdialog");
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
  await expect(page.getByRole("alertdialog").getByText("Something changed since, so undo would overwrite newer work")).toBeVisible();
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

  // Health.
  await page.getByRole("link", { name: "Health" }).first().click();
  await expect(page.getByRole("heading", { level: 1 })).toContainText(/Nothing is wrong|is fine|failing|firing/);
  await expect(page.getByText("Platform state readable.")).toBeVisible();
  await expect(page).toHaveTitle(/Health · Tiffin$/);

  // Command palette navigates.
  await page.keyboard.press("ControlOrMeta+k");
  await page.getByRole("combobox", { name: "Command palette" }).fill("api keys");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/settings\/keys$/);

  // API keys: create shows the secret once; revoke asks first. A key that
  // outlives the session needs a sign-in from the last 10 minutes: the
  // owner's own `tiffin login` (signIn above) is one, so no confirm step.
  await page.getByRole("button", { name: "Create key" }).click();
  const create = page.getByRole("dialog");
  await create.getByLabel("Name").fill("smoke-agent");
  await create.getByRole("radio", { name: "Read only" }).click();
  await expect(create.getByRole("radio", { name: "90 days" })).toBeChecked();
  for (const choice of ["30 days", "1 year", "Never"]) await expect(create.getByRole("radio", { name: choice })).not.toBeChecked();
  await create.getByRole("button", { name: "Create smoke-agent" }).click();
  await expect(page.getByTestId("token-secret")).toHaveText(/^tfn_/);
  await page.getByRole("button", { name: "I’ve stored it" }).click();
  const row = page.getByRole("listitem").filter({ hasText: "Smoke Agent" });
  await expect(row).toContainText("Read only");
  await expect(row).toContainText("expires in 3 months");
  await row.getByRole("button", { name: "Revoke smoke-agent" }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Revoke Smoke Agent" }).click();
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

// Touch ID / Face ID sign-in, then a project's overview, its secrets, and people.
test("touch id → project → secrets → people → confirm it's you → touch id sign-in", async ({ page, baseURL, browser }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));
  page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && problems.push(m.text()));

  const cdp = await page.context().newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true },
  });

  await signIn(page, baseURL!);

  // Touch ID / Face ID, from the account menu. Adding a passkey needs a
  // sign-in from the last 10 minutes: the owner's own `tiffin login` is one.
  await page.getByRole("button", { name: "Account" }).click();
  await page.getByRole("menuitem", { name: /Sign in with Touch ID \/ Face ID/ }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Sign in with Touch ID / Face ID");
  await page.getByLabel("Device name").fill("Test key");
  await page.getByRole("button", { name: "Set up this device" }).click();
  await expect(page.getByText("Test key")).toBeVisible();

  // A project's overview: what's in it, as tiles.
  await page.goto("/projects/hello");
  await expect(page.getByRole("heading", { level: 1, name: "hello", exact: true })).toBeVisible(); // the icon's letters are hidden
  await expect(page.getByRole("region", { name: "Production" }).getByRole("link", { name: "web", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Services" }).getByRole("link", { name: /^Database/ })).toBeVisible();

  // Secrets are write-only (the project's Environment Variables page).
  await page.goto("/projects/hello/env");
  await expect(page.getByText("STRIPE_SECRET_KEY")).toBeVisible();
  await page.getByRole("button", { name: "Add variable" }).click();
  const add = page.getByRole("dialog", { name: "Add a variable" });
  await add.getByLabel("Name").fill("smoke_token");
  await expect(add.getByLabel("Name")).toHaveValue("SMOKE_TOKEN");
  await add.getByRole("textbox", { name: "Value" }).fill("s3cret");
  const secret = add.getByRole("switch", { name: /^Secret/ });
  if ((await secret.getAttribute("aria-checked")) !== "true") await secret.click();
  await add.getByRole("button", { name: "Add SMOKE_TOKEN" }).click();
  await expect(add).toBeHidden();
  await expect(page.getByRole("cell", { name: "SMOKE_TOKEN", exact: true })).toBeVisible();
  await expect(page.getByText("s3cret")).toHaveCount(0);
  await page.getByRole("button", { name: "Delete SMOKE_TOKEN" }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: /^Delete/ }).click();
  await expect(page.getByRole("cell", { name: "SMOKE_TOKEN", exact: true })).toHaveCount(0);

  // People: invite with a role, get a one-time link.
  await page.goto("/settings/people");
  await page.getByRole("button", { name: "Invite someone" }).click();
  await page.getByLabel("Name").fill("Ada");
  await page.getByRole("radio", { name: /Viewer/ }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Invite Ada" }).click();
  const invite = page.getByText(/\/login#tfl_/);
  await expect(invite).toBeVisible();
  const code = (await invite.textContent())!.match(/tfl_[a-z0-9]+/)![0];
  await page.getByRole("button", { name: "Done" }).click();
  await expect(page.getByText("Ada")).toBeVisible();

  // Ada signs in with that invite, a link someone else made: setting up a
  // passkey asks her to confirm it's her first, or to sign in again.
  const ada = await browser.newContext({ baseURL });
  const adaPage = await ada.newPage();
  await adaPage.goto(`/login#${code}`);
  await adaPage.waitForURL((u) => u.pathname === "/");
  await adaPage.goto("/settings/passkeys");
  await adaPage.getByRole("button", { name: "Set up this device" }).click();
  const confirm = adaPage.getByRole("dialog");
  await expect(confirm.getByRole("heading", { name: "Confirm it’s you" })).toBeVisible();
  await expect(confirm.getByRole("link", { name: "Sign in again" })).toHaveAttribute("href", /^\/login\?.*reason=confirm/);
  await confirm.getByRole("link", { name: "Sign in again" }).click();
  await expect(adaPage.getByRole("heading", { level: 1 })).toHaveText("Sign in again to confirm it’s you.");
  await ada.close();

  // The device set up above also signs in: sign out, sign back in with it, land where you were going.
  await page.request.delete("/v1/session");
  await page.goto("/settings/passkeys");
  await expect(page).toHaveURL(/\/login\?reason=session&next=%2Fsettings%2Fpasskeys/);
  await page.getByRole("button", { name: "Sign in with Touch ID" }).click();
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
  await page.getByRole("button", { name: "Details" }).click();
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

  // The database is always there; Delete all data says what goes and that it comes back for 7 days, and Cancel keeps it.
  await page.goto("/projects/notes/settings#danger");
  await page.getByRole("button", { name: "Delete data" }).first().click();
  const dialog = page.getByRole("alertdialog");
  await expect(dialog.getByRole("heading", { name: "Delete all data in Database?" })).toBeVisible();
  await expect(dialog.getByText("You can restore it for 7 days. After that it’s gone for good.")).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Delete data" })).toBeDisabled(); // the project's name comes first
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();

  expect(problems, problems.join("\n")).toEqual([]);
});
