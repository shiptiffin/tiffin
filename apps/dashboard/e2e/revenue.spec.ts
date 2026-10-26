import AxeBuilder from "@axe-core/playwright";
import { mkdirSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { revenueReport, stripeStatus } from "./fixtures/revenue";
import { signIn } from "./helpers";

// Analytics › Revenue on "hello" with the revenue API stubbed (a scratch box
// can't reach Stripe; e2e/fixtures/revenue.ts has its answers): connecting
// with a restricted key, the import while it runs, the report under first and
// last touch, Match again, Repair and Disconnect. Nothing on the box changes.
//   bunx playwright test revenue
//   E2E_BASE_URL=http://localhost:5391 E2E_OWNER_TOKEN=... bunx playwright test revenue
// SHOTS=1 also saves screenshots (1440 and 390, light and dark) to SHOTS_DIR.

const out = process.env.SHOTS_DIR ?? "screenshots/revenue";
const url = "/projects/hello/analytics?tab=revenue&period=30d";

/** The manifest answers as if attribution were on. */
async function attributionOn(page: Page) {
  await page.route("**/v1/projects/hello/manifest", async (r) => {
    const res = await r.fetch();
    const body = await res.json();
    body.manifest.services.analytics = { ...body.manifest.services.analytics, attribution: { campaigns: true, signups: true, firstVisitCookie: "off" } };
    await r.fulfill({ response: res, json: body });
  });
}

/** Stub the revenue API; returns what was asked of it. */
async function stubRevenue(page: Page, status: () => ReturnType<typeof stripeStatus>) {
  const calls: string[] = [];
  await page.route("**/v1/analytics/stripe?*", (r) => (calls.push(`${r.request().method()} stripe`), r.fulfill({ json: r.request().method() === "DELETE" ? { project: "hello" } : status() })));
  await page.route("**/v1/analytics/revenue?*", (r) => {
    const touch = new URL(r.request().url()).searchParams.get("touch") === "first" ? "first" : "last";
    calls.push(`revenue ${touch}`);
    return r.fulfill({ json: revenueReport("hello", touch) });
  });
  await page.route("**/v1/analytics/revenue/match", (r) => (calls.push("match"), r.fulfill({ json: { checked: 3, matched: 1 } })));
  await page.route("**/v1/analytics/stripe/repair", (r) => (calls.push("repair"), r.fulfill({ json: stripeStatus("hello") })));
  // Which products count: one account selling two apps' products.
  let choice: { all: boolean; products: string[]; noProduct: boolean } = { all: true, products: [], noProduct: false };
  const products = () => ({
    project: "hello",
    all: choice.all,
    noProduct: choice.noProduct,
    noProductPayments: 2,
    catalog: true,
    products: [
      { id: "prod_reel", name: "Reelbird Pro", active: true, payments: 34, last: "2026-10-20T10:00:00Z", counted: choice.all || choice.products.includes("prod_reel") },
      { id: "prod_dibs", name: "Dibs Plus", active: true, payments: 12, last: "2026-10-18T10:00:00Z", counted: choice.all || choice.products.includes("prod_dibs") },
    ],
  });
  await page.route("**/v1/analytics/stripe/products*", async (r) => {
    if (r.request().method() === "POST") {
      const b = r.request().postDataJSON() as { all?: boolean; products?: string[]; noProduct?: boolean };
      calls.push(`products ${JSON.stringify({ all: b.all, products: b.products, noProduct: b.noProduct })}`);
      choice = { all: b.all === true, products: b.products ?? [], noProduct: b.noProduct === true };
    }
    return r.fulfill({ json: products() });
  });
  return calls;
}

test("revenue: count only some Stripe products, then every payment again", async ({ page, baseURL }) => {
  await attributionOn(page);
  const calls = await stubRevenue(page, () => stripeStatus("hello"));
  await signIn(page, baseURL!);
  await page.goto(url);

  const counts = page.locator("section[aria-labelledby=revenue-counts]");
  await expect(counts).toContainText("every payment in this Stripe account");
  await counts.getByRole("button", { name: "Change" }).click();
  await counts.getByRole("radio", { name: "Only these products" }).click();
  await expect(counts.getByText("34 payments")).toBeVisible();
  if (process.env.SHOTS) await counts.screenshot({ path: `${out}/counts.png` });
  await expect(counts.getByRole("button", { name: "Save" })).toBeDisabled(); // nothing picked yet
  await counts.getByRole("checkbox", { name: /Reelbird Pro/ }).click();
  await counts.getByRole("button", { name: "Save" }).click();
  await expect(counts).toContainText("Counts Reelbird Pro");
  expect(calls).toContain(`products {"products":["prod_reel"],"noProduct":false}`);

  await counts.getByRole("button", { name: "Change" }).click();
  await counts.getByRole("radio", { name: "Every payment in this Stripe account" }).click();
  await counts.getByRole("button", { name: "Save" }).click();
  await expect(counts).toContainText("every payment in this Stripe account");
  expect(calls).toContain(`products {"all":true}`);
});

async function noSeriousA11y(page: Page, what: string) {
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity));
  const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
  const bad = r.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(bad.map((v) => `${what}: ${v.id} ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
}

test("revenue: off without attribution, then connect Stripe with a restricted key", async ({ page, baseURL }) => {
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(e.message));
  await signIn(page, baseURL!);
  await page.goto(url);
  await expect(page.getByText("Revenue needs marketing attribution.")).toBeVisible();

  await attributionOn(page);
  let status = stripeStatus("hello", { connected: false });
  let polls = 0;
  const calls = await stubRevenue(page, () => {
    // The import runs for two reads, then it's done.
    if (status.connected && status.backfill.state === "running" && ++polls > 2) status = stripeStatus("hello");
    return status;
  });
  await page.route("**/v1/analytics/stripe", async (r) => {
    const key = r.request().postDataJSON().key as string;
    if (key.startsWith("rk_live_wrong")) {
      return r.fulfill({
        status: 422,
        contentType: "application/problem+json",
        json: { status: 422, code: "validation", title: "Unprocessable", detail: "Stripe refused the key: the key lacks webhook_endpoint_write", hint: "create a restricted key at https://dashboard.stripe.com/apikeys (Create restricted key) with Webhook Endpoints, Event Destinations: Write" },
      });
    }
    status = stripeStatus("hello", { backfill: { state: "running", payments: 412, refunds: 6, subscriptions: 31, started: new Date().toISOString() } });
    return r.fulfill({ json: status });
  });
  await page.goto(url);
  await expect(page.getByText("Connect Stripe to see which sources bring paying customers.")).toBeVisible();
  // The permissions come from the box, grouped as Stripe's form has them.
  await expect(page.getByText("Balance · Charges and Refunds · Customers · Invoices · Subscriptions · Checkout Sessions")).toBeVisible();
  await expect(page.getByRole("link", { name: "Open API keys" })).toHaveAttribute("href", "https://dashboard.stripe.com/apikeys");

  const key = page.getByLabel("Stripe restricted key");
  const connect = page.getByRole("button", { name: "Connect", exact: true });
  await key.fill("pk_live_123");
  await expect(connect).toBeDisabled(); // a publishable key isn't one
  await key.fill("rk_live_wrong12345678");
  await connect.click();
  await expect(page.getByText("Stripe didn’t take that key")).toBeVisible();
  await expect(page.getByText(/Webhook Endpoints, Event Destinations: Write/).last()).toBeVisible();

  await key.fill("rk_live_51Qgood12345678");
  await connect.click();
  await expect(page.getByText("Importing the last 12 months from Stripe.")).toBeVisible();
  await expect(page.getByText("Importing the last 12 months from Stripe.")).toBeHidden({ timeout: 15_000 });
  await expect(page.getByRole("group", { name: /^Revenue per day/ })).toBeVisible();
  expect(calls.filter((c) => c === "GET stripe").length).toBeGreaterThanOrEqual(3);
  expect(problems).toEqual([]);
});

test("revenue: the report, first and last touch, match again, repair, disconnect", async ({ page, baseURL }) => {
  await attributionOn(page);
  let status = stripeStatus("hello", { urlMismatch: true, wantUrl: "https://hello.shop/_tiffin/stripe", warnings: ["the webhook points at https://hello.example.com/_tiffin/stripe, but the project is now at https://hello.shop/_tiffin/stripe: run stripe repair"] });
  const calls = await stubRevenue(page, () => status);
  await signIn(page, baseURL!);
  await page.goto(url);

  // Six numbers against the days before, in the account's currency.
  const tiles = page.locator("section[aria-labelledby=revenue-trend] dl");
  await expect(tiles.getByText("$12,481")).toBeVisible();
  await expect(tiles.getByText("$3,890")).toBeVisible(); // MRR
  await expect(tiles.getByText("$0.95")).toBeVisible(); // revenue per visitor
  await expect(tiles.getByText(/^1\.17.%$/)).toBeVisible();
  await expect(page.getByRole("group", { name: /^Revenue per day/ })).toBeVisible();

  // Sources: direct by name, under last touch; first touch asks again.
  const sources = page.getByRole("region", { name: "Sources" });
  await expect(sources.getByText("Direct")).toBeVisible();
  await expect(sources.getByText("$4,120")).toBeVisible();
  await page.getByRole("radio", { name: /^First touch/ }).click();
  await expect(page).toHaveURL(/touch=first/);
  await expect(sources.getByText("$3,510")).toBeVisible();
  expect(calls).toContain("revenue first");
  await expect(page.getByText("$280.50 from 4 payments isn’t credited to a visit.")).toBeVisible();
  await expect(page.getByText(/€420\.00 in EUR/)).toBeVisible();

  // Payments not matched, with why; Match again.
  await expect(page.getByText("No account has this customer's email.")).toBeVisible();
  await page.getByRole("button", { name: "Match again" }).click();
  await expect(page.getByText("Matched 1 of 3 payments.")).toBeVisible();
  await expect(page.getByText("metadata: { tiffin_user: user.id }")).toBeVisible();

  // The webhook points at an old address: Repair.
  await expect(page.getByText(/The webhook points at https:\/\/hello\.example\.com/)).toBeVisible();
  status = stripeStatus("hello");
  await page.getByRole("button", { name: "Repair webhook" }).click();
  await expect(page.getByText("The webhook points at the project’s address again.")).toBeVisible();
  expect(calls).toContain("repair");

  // Disconnect asks first.
  await page.getByRole("button", { name: "Disconnect", exact: true }).click();
  const dialog = page.getByRole("alertdialog", { name: "Disconnect Stripe from hello?" });
  await expect(dialog.getByText(/2,304 imported payments will be gone for good/)).toBeVisible();
  status = stripeStatus("hello", { connected: false });
  await dialog.getByRole("button", { name: "Disconnect" }).click();
  await expect(page.getByText("Disconnected Stripe from hello.")).toBeVisible();
  expect(calls).toContain("DELETE stripe");
  await expect(page.getByLabel("Stripe restricted key")).toBeVisible();
});

test("revenue: no serious accessibility problems, screenshots", async ({ page, baseURL }) => {
  test.setTimeout(180_000);
  if (process.env.SHOTS) mkdirSync(out, { recursive: true });
  await attributionOn(page);
  let status = stripeStatus("hello");
  await stubRevenue(page, () => status);
  await signIn(page, baseURL!);
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: width > 400 ? 900 : 844 });
      status = stripeStatus("hello", { connected: false });
      await page.goto(url);
      await page.getByLabel("Stripe restricted key").waitFor();
      await noSeriousA11y(page, `connect ${scheme} ${width}`);
      if (process.env.SHOTS) await page.screenshot({ path: `${out}/connect-${width}-${scheme}.png`, fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);

      status = stripeStatus("hello", { mode: "test", key: "rk_test_…9f3c" });
      await page.goto(url);
      await page.getByRole("group", { name: /^Revenue per day/ }).locator("path").first().waitFor();
      await page.waitForTimeout(500);
      await noSeriousA11y(page, `report ${scheme} ${width}`);
      if (process.env.SHOTS) await page.screenshot({ path: `${out}/report-${width}-${scheme}.png`, fullPage: true });
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width + 1);
    }
  }
});
