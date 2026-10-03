import { mkdirSync } from "node:fs";
import { expect, test } from "@playwright/test";
import { ownerToken, seedAgents, signIn } from "./helpers";

// The whole signing loop, with a virtual authenticator standing in for Touch
// ID: an agent asks to make a bucket public (outbound), the owner signs with a
// passkey, the agent applies with the approval, the receipt shows the Seal,
// and the owner undoes it. It removes the passkey it added.
//
// By default it runs on the throwaway box Playwright starts (e2e/serve.sh),
// which is deleted afterwards, so repeated runs leave nothing behind:
//   LEDGER_SIGN=1 bunx playwright test ledger-sign
// It can also run on a dev box (passkeys are bound to the box's origin, so
// point E2E_BASE_URL at the box). The Ledger is append-only, so each run there
// adds the change and its undo for good; LEDGER_VITE (optional) serves the
// dashboard's code from a Vite dev server under that origin:
//   LEDGER_SIGN=1 E2E_BASE_URL=https://dashboard.tiffin.localhost:8470 LEDGER_VITE=http://localhost:5401 \
//     E2E_OWNER_TOKEN=... E2E_AGENT_TOKEN=... bunx playwright test ledger-sign
const external = !!process.env.E2E_BASE_URL;
test.skip(!process.env.LEDGER_SIGN || (external && (!process.env.E2E_OWNER_TOKEN || !process.env.E2E_AGENT_TOKEN)), "set LEDGER_SIGN=1 (and on a dev box E2E_OWNER_TOKEN, E2E_AGENT_TOKEN)");

const out = process.env.SHOTS_DIR ?? "screenshots/fusion";

test("an agent asks, a person signs with a passkey, the agent applies, the person undoes", async ({ page, baseURL }) => {
  test.setTimeout(180_000);
  mkdirSync(out, { recursive: true });
  page.setDefaultTimeout(20_000);
  await page.setViewportSize({ width: 1440, height: 1000 });
  const problems: string[] = [];
  page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));

  const owner = { Authorization: `Bearer ${ownerToken()}` };
  // On the throwaway box, codex (notes only, no apply:outbound) asks to share the private thumbnails bucket.
  const agentToken = external ? process.env.E2E_AGENT_TOKEN : seedAgents().CODEX;
  const agent = { Authorization: `Bearer ${agentToken}`, "X-Tiffin-Session": "s-51c0d2" };
  const project = process.env.LEDGER_PROJECT ?? (external ? "shop" : "notes");
  const bucket = process.env.LEDGER_BUCKET ?? (external ? "uploads" : "thumbnails");

  const vite = process.env.LEDGER_VITE;
  if (vite) {
    const box = new URL(baseURL!);
    await page.route(
      (u) => u.host === box.host && !/^\/(v1|mcp)(\/|$)/.test(u.pathname),
      async (route) => {
        const u = new URL(route.request().url());
        const res = await route.fetch({ url: `${vite}${u.pathname}${u.search}` });
        await route.fulfill({ response: res });
      },
    );
  }

  const cdp = await page.context().newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true },
  });
  await signIn(page, baseURL!);

  // The agent's request: make the uploads bucket public (outbound, needs a person).
  const m = (await (await page.request.get(`${baseURL}/v1/projects/${project}/manifest`, { headers: owner })).json()).manifest;
  m.services.storage.buckets[bucket] = { ...m.services.storage.buckets[bucket], public: true };
  const plan = await (await page.request.post(`${baseURL}/v1/plan`, { headers: agent, data: { manifest: m } })).json();
  const intent = external
    ? "Make the uploads bucket public. Product photos load from it on the storefront, and private links expire after an hour."
    : "Make the thumbnails bucket public. Link previews load from it, and private links expire after an hour.";
  const ask = await page.request.post(`${baseURL}/v1/apply`, { headers: agent, data: { manifest: m, confirm: plan.hash, intent } });
  expect(ask.status()).toBe(403);
  const approvalId = (await ask.json()).approval.id as string;

  let passkeyId: string | undefined;
  try {
    // A passkey to sign with.
    await page.goto("/settings/passkeys");
    await page.getByLabel("Passkey name").fill("MacBook Air");
    await page.getByRole("button", { name: "Add a passkey" }).click();
    await expect(page.getByText("MacBook Air").first()).toBeVisible();
    const keys = (await (await page.request.get(`${baseURL}/v1/passkeys`, { headers: owner })).json()) as Array<{ id: string; name: string }>;
    passkeyId = keys.find((k) => k.name === "MacBook Air")?.id;

    // Sign.
    await page.goto(`/approvals/${approvalId}`);
    await expect(page.getByText(/Signing lets .+ make something visible outside the box/)).toBeVisible();
    await page.getByRole("button", { name: "Sign with passkey" }).click();
    await expect(page.getByText("Signed, not applied yet").first()).toBeVisible();
    await expect(page.getByRole("img", { name: /Seal: signed by/ })).toBeVisible();
    await page.waitForTimeout(250);
    await page.screenshot({ path: `${out}/approval-sealing-light-1440.png` });
    await page.waitForTimeout(700);
    await page.screenshot({ path: `${out}/approval-signed-real-light-1440.png`, fullPage: true });

    // The agent applies exactly that plan, once.
    const applied = await page.request.post(`${baseURL}/v1/apply`, { headers: agent, data: { manifest: m, confirm: plan.hash, intent, approval: approvalId } });
    expect(applied.status()).toBe(200);
    const changeId = (await applied.json()).change.id as string;
    await page.reload();
    await expect(page.getByText("Signed and applied").first()).toBeVisible();
    await expect(page.getByRole("link", { name: "Open the Ledger entry" })).toBeVisible();
    await page.screenshot({ path: `${out}/approval-applied-light-1440.png`, fullPage: true });

    // The Ledger shows the signature; the receipt carries the Seal.
    await page.goto("/ledger");
    await expect(page.getByText(/signed by .* · passkey/).first()).toBeVisible();
    await page.screenshot({ path: `${out}/ledger-signed-light-1440.png` });
    await page.goto(`/changes/${changeId}`);
    await expect(page.getByText("Signed and applied")).toBeVisible();
    await expect(page.getByRole("img", { name: /Seal: signed by/ })).toBeVisible();
    await page.screenshot({ path: `${out}/entry-signed-light-1440.png`, fullPage: true });
    await page.emulateMedia({ media: "print" });
    await page.screenshot({ path: `${out}/entry-signed-print-light-1440.png`, fullPage: true });
    await page.emulateMedia({ media: "screen" });

    // Put it back.
    await page.getByRole("button", { name: "Undo this change" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("heading", { name: "Review the undo" })).toBeVisible();
    await dialog.getByRole("button", { name: "Confirm undo" }).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByText(/Undone by/)).toBeVisible();
  } finally {
    if (passkeyId) await page.request.delete(`${baseURL}/v1/passkeys/${passkeyId}`, { headers: owner });
  }
  expect(problems, problems.join("\n")).toEqual([]);
});
