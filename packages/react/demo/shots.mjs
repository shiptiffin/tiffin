// Screenshots of the gallery (demo/server.ts must be running):
//   research/heavy.sh node demo/shots.mjs [outDir]
import { createRequire } from "node:module";
const require = createRequire(import.meta.url + "/../../../../apps/dashboard/");
const { chromium } = require("@playwright/test");
const base = process.env.DEMO_URL ?? "http://localhost:4317";
const out = process.argv[2] ?? "/tmp/tiffin-react-shots";
await import("node:fs").then((fs) => fs.mkdirSync(out, { recursive: true }));
const browser = await chromium.launch();
const shots = [];
for (const theme of ["light", "dark"]) {
  const ctx = await browser.newContext({ viewport: { width: 520, height: 880 }, deviceScaleFactor: 2, colorScheme: theme });
  const page = await ctx.newPage();
  const go = async (only) => {
    await page.goto(`${base}/?only=${only}&theme=${theme}`);
    await page.waitForLoadState("networkidle");
    await page.evaluate(() => document.fonts.ready);
    await page.waitForTimeout(300);
  };
  const shot = async (name) => {
    const f = `${out}/${name}-${theme}.png`;
    await page.screenshot({ path: f });
    shots.push(f);
  };
  await go("signin"); await shot("signin");
  await page.getByLabel("Email").fill("ada@example.com");
  await page.getByRole("button", { name: "Use a one-time code" }).click();
  await page.getByRole("heading", { name: "Enter your code" }).waitFor();
  await page.keyboard.type("4182");
  await shot("signin-code");
  await go("signin");
  await page.getByLabel("Email").fill("ada@example.com");
  await page.getByLabel("Password", { exact: true }).fill("wrong password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("alert").waitFor();
  await shot("signin-error");
  await go("signup"); await shot("signup");
  await go("invite"); await shot("invite");
  await page.getByRole("button", { name: /invite link/ }).click();
  await page.getByText(/accept-invite\?link=/).waitFor();
  await shot("invite-link");
  await go("accept"); await shot("accept");
  await page.setViewportSize({ width: 760, height: 420 });
  await go("bar");
  await page.getByRole("button", { name: /Organization:/ }).click();
  await shot("orgswitcher");
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: /Account:/ }).click();
  await shot("userbutton");
  await ctx.close();
}
const phone = await browser.newContext({ viewport: { width: 375, height: 760 }, deviceScaleFactor: 2 });
const p = await phone.newPage();
await p.goto(`${base}/?only=signin`);
await p.waitForLoadState("networkidle");
await p.screenshot({ path: `${out}/signin-375.png` });
shots.push(`${out}/signin-375.png`);
await browser.close();
console.log(shots.join("\n"));
