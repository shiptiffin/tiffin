import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

test("a week-long sign-in survives reopening the browser and can still be revoked", async ({ page, browser, baseURL }) => {
  await signIn(page, baseURL!);
  const state = await page.context().storageState();
  const cookie = state.cookies.find((c) => c.name === "__Host-tiffin_session");
  expect(cookie).toBeDefined();
  expect(cookie).toMatchObject({ httpOnly: true, secure: true, sameSite: "Strict", path: "/" });
  const remaining = cookie!.expires - Date.now() / 1000;
  expect(remaining).toBeGreaterThan(7 * 86400 - 60);
  expect(remaining).toBeLessThanOrEqual(7 * 86400);

  // A fresh browser context restores only persisted browser state.
  const reopened = await browser.newContext({ baseURL, storageState: state });
  try {
    const tab = await reopened.newPage();
    await tab.goto("/settings/sign-ins");
    await expect(tab.getByText("Each sign-in lasts 7 days.")).toBeVisible();
    const who = await reopened.request.get("/v1/whoami");
    expect(who.ok()).toBe(true);
    const person = await who.json();
    expect(Math.abs(new Date(person.expiresAt).getTime() / 1000 - cookie!.expires)).toBeLessThan(1);

    // Revocation takes effect even in a browser still holding the old cookie.
    expect((await page.request.delete("/v1/session")).ok()).toBe(true);
    expect((await reopened.request.get("/v1/whoami")).status()).toBe(401);
    await tab.goto("/status");
    await expect(tab).toHaveURL(/\/login\?reason=session/);
  } finally {
    await reopened.close();
  }
});

test("a passkey signs in for seven days through the real browser flow", async ({ page, baseURL }) => {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true },
  });
  await signIn(page, baseURL!);
  // First-time setup is offered before the dashboard's account menu.
  await expect(page).toHaveURL(/\/welcome\/passkey/);
  await page.getByRole("button", { name: "Add Touch ID", exact: true }).click();
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await page.waitForURL((u) => u.pathname === "/");
  expect((await page.request.delete("/v1/session")).ok()).toBe(true);
  await page.goto("/login");
  await page.getByRole("button", { name: "Sign in with Touch ID", exact: true }).click();
  await page.waitForURL((u) => u.pathname === "/");
  const cookie = (await page.context().cookies()).find((c) => c.name === "__Host-tiffin_session");
  expect(cookie!.expires - Date.now() / 1000).toBeGreaterThan(7 * 86400 - 60);
  const sessions = await page.request.get("/v1/sessions");
  expect((await sessions.json()).find((s: { current: boolean }) => s.current).method).toBe("passkey");
  expect((await page.request.delete("/v1/session")).ok()).toBe(true);
});
