import { mkdirSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { ownerToken, signIn } from "./helpers";

// Visual review of the Ledger (list, a receipt, approvals and the permit)
// against a seeded dev box, through a Vite dev server:
//   SCREENS=1 E2E_BASE_URL=http://localhost:5401 E2E_OWNER_TOKEN=... bunx playwright test screens-ledger
// It reads only, except that it types into the permit's guard (never signs).
// The signed permit is drawn from a stubbed answer so no plan is approved.
test.skip(!process.env.SCREENS || !process.env.E2E_OWNER_TOKEN, "set SCREENS=1 and E2E_OWNER_TOKEN for a seeded box");

const out = process.env.SHOTS_DIR ?? "screenshots/fusion";
const only = process.env.SHOTS?.split(",");

type Apr = { id: string; status: string; plan: { risk: string }; createdAt: string; planHash: string };
type Chg = { id: string; undoneBy?: string; actor: { kind: string }; plan: { risk: string } };

async function shot(page: Page, name: string, fullPage = true) {
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage });
  const wide = await page.evaluate(() => document.documentElement.scrollWidth);
  const vw = page.viewportSize()?.width ?? 0;
  if (wide > vw + 1) console.log(`OVERFLOW ${name}: ${wide}px`);
}

for (const theme of ["light", "dark"] as const) {
  for (const size of [
    { name: "1440", width: 1440, height: 900 },
    { name: "390", width: 390, height: 844 },
  ]) {
    test(`ledger screens ${theme} ${size.name}`, async ({ page, baseURL }) => {
      test.setTimeout(240_000);
      mkdirSync(out, { recursive: true });
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      page.setDefaultTimeout(20_000);
      page.on("console", (m) => m.type() === "error" && !m.text().startsWith("Failed to load resource") && console.log(`CONSOLE ${m.text()}`));
      page.on("pageerror", (e) => console.log(`PAGEERROR ${e.message}`));
      await signIn(page, baseURL!);

      const auth = { Authorization: `Bearer ${ownerToken()}` };
      const approvals = (await (await page.request.get(`${baseURL}/v1/approvals`, { headers: auth })).json()) as Apr[];
      const changes = (await (await page.request.get(`${baseURL}/v1/changes?limit=200`, { headers: auth })).json()) as Chg[];
      const pending = approvals.find((a) => a.status === "pending" && a.plan.risk === "irreversible");
      const rejected = approvals.find((a) => a.status === "rejected");
      const undone = changes.find((c) => c.undoneBy && c.actor.kind === "agent") ?? changes.find((c) => c.undoneBy);
      const irr = changes.find((c) => c.plan.risk === "irreversible");
      const go = (n: string) => !only || only.includes(n);
      const tag = `${theme}-${size.name}`;

      if (go("ledger")) {
        await page.goto("/ledger");
        await page.getByRole("heading", { level: 1 }).waitFor();
        await shot(page, `ledger-${tag}`);
        if (size.name === "1440") {
          await page.keyboard.press("j");
          await page.keyboard.press("j");
          await page.keyboard.press("j");
          await expect(page.locator("[data-entry][data-sel]")).toHaveCount(1);
          await shot(page, `ledger-keys-${tag}`, false);
        }
        await page.goto("/ledger?who=agents");
        await page.getByRole("heading", { level: 1 }).waitFor();
        await shot(page, `ledger-agents-${tag}`, false);
      }
      if (go("entry") && undone) {
        await page.goto(`/changes/${undone.id}`);
        await page.getByRole("heading", { level: 1 }).waitFor();
        await page.getByText("Times and numbers").waitFor();
        await shot(page, `entry-${tag}`);
      }
      if (go("entry") && irr) {
        await page.goto(`/changes/${irr.id}`);
        await page.getByText("Times and numbers").waitFor();
        await shot(page, `entry-irreversible-${tag}`);
        if (size.name === "1440" && theme === "light") {
          await page.emulateMedia({ media: "print" });
          await shot(page, `entry-print-${tag}`);
          await page.emulateMedia({ media: "screen" });
        }
      }
      if (go("approvals")) {
        await page.goto("/approvals");
        await page.getByRole("heading", { level: 1 }).waitFor();
        await shot(page, `approvals-${tag}`);
      }
      if (go("permit") && pending) {
        await page.goto(`/approvals/${pending.id}`);
        await page.getByRole("heading", { level: 1 }).waitFor();
        await shot(page, `approval-${tag}`);
        const project = await page.getByLabel(/^Type .* to arm$/).getAttribute("placeholder");
        await page.getByLabel(/^Type .* to arm$/).fill(project ?? "");
        await expect(page.getByRole("button", { name: "Sign with passkey" })).toBeEnabled();
        await shot(page, `approval-armed-${tag}`);
        await page.getByRole("button", { name: "Decline…" }).click();
        await page.getByLabel(/Tell .* why/).waitFor();
        await shot(page, `approval-declining-${tag}`, false);

        // The signed state, drawn from a stubbed answer: nothing is approved.
        const real = await (await page.request.get(`${baseURL}/v1/approvals/${pending.id}`, { headers: auth })).json();
        const decidedAt = new Date(Date.now() - 2 * 60_000).toISOString();
        await page.route(`**/v1/approvals/${pending.id}`, (r) =>
          r.fulfill({ json: { ...real, status: "approved", decidedAt, decidedBy: "tok_01M41HAR6FFX9J7QVC8F3V6MZA" } }),
        );
        await page.goto(`/approvals/${pending.id}`);
        await page.getByText("Signed, not applied yet").waitFor();
        await shot(page, `approval-signed-${tag}`);
        await page.unroute(`**/v1/approvals/${pending.id}`);
      }
      if (go("permit") && rejected) {
        await page.goto(`/approvals/${rejected.id}`);
        await page.getByRole("heading", { level: 1 }).waitFor();
        await shot(page, `approval-declined-${tag}`);
      }
    });
  }
}
