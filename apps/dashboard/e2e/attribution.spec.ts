import AxeBuilder from "@axe-core/playwright";
import { createHash } from "node:crypto";
import { mkdirSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { privacyText, type Cookie } from "../src/lib/privacy-text";
import { ownerToken, signIn } from "./helpers";

// Analytics › Attribution against the seeded box (e2e/serve.sh), on "hello":
// turn marketing attribution on through its dialog (the box asking "Confirm
// it's you" first, answered by a stubbed passkey), flip its switches, set the
// first-visit cookie for EU visitors with consent, then turn it off through
// the irreversible plan. It leaves hello as it found it (attribution off).
//   bunx playwright test attribution
//   E2E_BASE_URL=http://localhost:5391 E2E_OWNER_TOKEN=... bunx playwright test attribution
// SHOTS=1 also saves screenshots (1440 and 390, light and dark) to SHOTS_DIR.

const out = process.env.SHOTS_DIR ?? "screenshots/attribution";
const url = "/projects/hello/analytics?tab=attribution";

async function attribution(page: Page, baseURL: string) {
  const res = await page.request.get(`${baseURL}/v1/projects/hello/manifest`, { headers: { Authorization: `Bearer ${ownerToken()}` } });
  return ((await res.json()).manifest.services?.analytics?.attribution ?? null) as Record<string, unknown> | null;
}

async function noSeriousA11y(page: Page, what: string) {
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity));
  const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
  const bad = r.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(bad.map((v) => `${what}: ${v.id} ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
}

/** The box asks for a recent strong sign-in once; a stubbed passkey answers. */
async function askOnceThenPasskey(page: Page) {
  let asked = 0;
  await page.route("**/v1/apply", async (r) => {
    if (asked++ > 0) return r.fallback();
    await r.fulfill({
      status: 403,
      contentType: "application/problem+json",
      json: { status: 403, code: "reauth_required", title: "Forbidden", detail: "confirm it's you first: turning on marketing attribution needs a recent sign-in" },
    });
  });
  await page.route("**/v1/passkeys", (r) => r.fulfill({ json: [{ id: "pk1", name: "This Mac", createdAt: new Date().toISOString() }] }));
  await page.route("**/v1/session/confirm/options", (r) => r.fulfill({ json: { publicKey: { challenge: "AAAA", allowCredentials: [] } } }));
  await page.route("**/v1/session/confirm", (r) => r.fulfill({ json: { confirmedUntil: new Date(Date.now() + 600_000).toISOString() } }));
  await page.addInitScript(() => {
    const buf = () => new Uint8Array([1, 2, 3]).buffer;
    if (!navigator.credentials) return; // about:blank isn't a secure context
    navigator.credentials.get = async () =>
      ({ id: "pk1", rawId: buf(), type: "public-key", authenticatorAttachment: "platform", getClientExtensionResults: () => ({}), response: { clientDataJSON: buf(), authenticatorData: buf(), signature: buf(), userHandle: buf() } }) as unknown as Credential;
  });
  return () => asked;
}

test.describe.configure({ mode: "serial" });

test("attribution: turn on as a person, switches, the cookie, turn off", async ({ page, context, baseURL }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(e.message));
  const asked = await askOnceThenPasskey(page);
  await signIn(page, baseURL!);
  expect(await attribution(page, baseURL!)).toBeNull();

  // Basic analytics says campaign tags need attribution, and links to it.
  await page.goto("/projects/hello/analytics?period=30d");
  await page.getByRole("tab", { name: "UTM source" }).click();
  await page.getByRole("button", { name: "Turn on attribution" }).click();
  await expect(page).toHaveURL(/tab=attribution/);

  // Off: one sentence and Turn on. The dialog says what it means, with the privacy text to add.
  await expect(page.getByText("See which sources and campaigns bring sign-ups and paying customers.")).toBeVisible();
  await page.getByRole("button", { name: "Turn on attribution" }).click();
  const dialog = page.getByRole("dialog", { name: "Turn on marketing attribution?" });
  await expect(dialog.getByText("That is personal data about your customers.")).toBeVisible();
  await expect(dialog.getByText("Visitors whose browser sends it are left out.")).toBeVisible();
  await expect(dialog.getByRole("heading", { name: "Where our customers come from" })).toBeVisible();
  await dialog.getByRole("radio", { name: "Markdown" }).click();
  await expect(dialog.getByText("### Where our customers come from")).toBeVisible();
  await dialog.getByRole("button", { name: "Copy text" }).click();
  await dialog.getByRole("button", { name: "Turn on", exact: true }).click();

  // The box asks first; a passkey confirms, and the same change goes through.
  const sudo = page.getByRole("dialog", { name: "Confirm it’s you" });
  await sudo.getByRole("button", { name: /^Confirm with/ }).click();
  await expect(page.getByText("Marketing attribution is on.")).toBeVisible();
  await expect(page.getByText("Turned on marketing attribution for hello.")).toBeVisible();
  expect(asked()).toBe(2);
  expect(await attribution(page, baseURL!)).toEqual({ campaigns: true, signups: true, firstVisitCookie: "off" });

  // The box's own text matches the preview that was copied: no "changed" notice.
  await expect(page.getByText("You copied this version.")).toBeVisible();

  // A switch changes at once.
  await page.getByRole("switch", { name: "Sign-up sources: on" }).click();
  await expect(page.getByRole("switch", { name: "Sign-up sources: off" })).toBeVisible();
  await expect.poll(async () => (await attribution(page, baseURL!))?.signups).toBe(false);
  await page.getByRole("switch", { name: "Sign-up sources: off" }).click();
  await expect.poll(async () => (await attribution(page, baseURL!))?.signups).toBe(true);

  // The first-visit cookie for EU visitors with consent: its own confirmation, with the prompt to add.
  await page.getByRole("radio", { name: "Ask EU visitors" }).click();
  const cookie = page.getByRole("dialog", { name: "Ask EU visitors for the first-visit cookie?" });
  await expect(cookie.getByText('<script defer src="/_tiffin/consent.js"></script>')).toBeVisible();
  await cookie.getByRole("button", { name: "Ask EU visitors" }).click();
  await expect(cookie).toBeHidden();
  await expect.poll(async () => (await attribution(page, baseURL!))?.firstVisitCookie).toBe("ask-eu");
  await expect(page.getByRole("button", { name: "Copy consent prompt" })).toBeVisible();
  // The privacy text now lists the cookie, and it isn't the copy any more.
  await expect(page.getByText("Your privacy text changed.")).toBeVisible();
  await expect(page.getByRole("region", { name: "Privacy text" }).getByText("tiffin_first_visit")).toBeVisible();
  await page.getByRole("button", { name: "Copy new text" }).click();
  await expect(page.getByText("You copied this version.")).toBeVisible();

  // Fewer visitors: no confirmation.
  await page.getByRole("radio", { name: "Off", exact: true }).click();
  await expect.poll(async () => (await attribution(page, baseURL!))?.firstVisitCookie).toBe("off");

  // Turning it off deletes what it saved: the plan says so, and can't be undone.
  await page.getByRole("button", { name: "Turn off", exact: true }).click();
  const tray = page.getByRole("alertdialog", { name: "Turn off marketing attribution for hello?" });
  await expect(tray.getByText(/deletes what it linked to accounts: sign-up sources, checkout returns, first visits and payments/)).toBeVisible();
  await tray.getByRole("button", { name: "Delete for good" }).click();
  await expect(page.getByRole("button", { name: "Turn on attribution" })).toBeVisible();
  expect(await attribution(page, baseURL!)).toBeNull();
  expect(problems).toEqual([]);
});

test("attribution: an API key's refusal says who can turn it on", async ({ page, baseURL }) => {
  await page.route("**/v1/apply", (r) =>
    r.fulfill({
      status: 403,
      contentType: "application/problem+json",
      json: {
        status: 403,
        code: "forbidden",
        title: "Forbidden",
        detail: "an API key can't turn on marketing attribution: it links visits to people and your privacy policy has to say so",
        hint: "a person turns it on in the dashboard (the project's Analytics page, Attribution), or with the owner token",
      },
    }),
  );
  await signIn(page, baseURL!);
  await page.goto(url);
  await page.getByRole("button", { name: "Turn on attribution" }).click();
  await page.getByRole("button", { name: "Turn on", exact: true }).click();
  await expect(page.getByText("Couldn’t turn it on")).toBeVisible();
  await expect(page.getByText(/a person turns it on in the dashboard/i)).toBeVisible();
  expect(await attribution(page, baseURL!)).toBeNull();
});

test("attribution: no serious accessibility problems, screenshots", async ({ page, baseURL }) => {
  test.setTimeout(180_000);
  if (process.env.SHOTS) mkdirSync(out, { recursive: true });
  const shot = async (name: string, width: number, scheme: string, full = true) => {
    if (!process.env.SHOTS) return;
    await page.waitForTimeout(400);
    await page.screenshot({ path: `${out}/${name}-${width}-${scheme}.png`, fullPage: full });
  };
  // The on state, without changing the box: the manifest and the privacy text answer as if attribution were on.
  const on = async (cookie: Cookie) => {
    await page.route("**/v1/projects/hello/manifest", async (r) => {
      const res = await r.fetch();
      const body = await res.json();
      body.manifest.services.analytics = { ...body.manifest.services.analytics, attribution: { campaigns: true, signups: true, firstVisitCookie: cookie } };
      await r.fulfill({ response: res, json: body });
    });
    const s = { on: true, campaigns: true, signups: true, cookie };
    const markdown = privacyText(s);
    const settings = { attribution: true, campaigns: true, signups: true, firstVisitCookie: cookie };
    const hash = createHash("sha256").update(markdown).digest("hex").slice(0, 12);
    await page.route("**/v1/analytics/privacy?*", (r) => r.fulfill({ json: { project: "hello", settings, revenue: false, version: "2026-10-25", hash, markdown } }));
  };
  const off = async () => {
    await page.unroute("**/v1/projects/hello/manifest");
    await page.unroute("**/v1/analytics/privacy?*");
  };
  await signIn(page, baseURL!);
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: width > 400 ? 900 : 844 });
      await off();
      await page.goto(url);
      await page.getByRole("button", { name: "Turn on attribution" }).waitFor();
      await noSeriousA11y(page, `off ${scheme} ${width}`);
      await shot("off", width, scheme);
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);

      await page.getByRole("button", { name: "Turn on attribution" }).click();
      await page.getByRole("dialog", { name: "Turn on marketing attribution?" }).waitFor();
      await noSeriousA11y(page, `turn on ${scheme} ${width}`);
      await shot("turn-on", width, scheme, false);
      await page.keyboard.press("Escape");

      await on("ask-eu");
      await page.goto(url);
      await page.getByText("Marketing attribution is on.").waitFor();
      await page.getByRole("region", { name: "Privacy text" }).getByText("tiffin_first_visit").waitFor();
      await noSeriousA11y(page, `on ${scheme} ${width}`);
      await shot("on", width, scheme);
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);

      await off();
      await on("off");
      await page.goto(url);
      await page.getByRole("radio", { name: "Outside the EU" }).click();
      await page.getByRole("dialog", { name: "Set the first-visit cookie?" }).waitFor();
      await noSeriousA11y(page, `cookie ${scheme} ${width}`);
      await shot("cookie", width, scheme, false);
      await page.keyboard.press("Escape");
      await off();
    }
  }
});
