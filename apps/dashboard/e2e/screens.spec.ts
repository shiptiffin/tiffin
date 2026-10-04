import { mkdirSync } from "node:fs";
import { test, type Page } from "@playwright/test";
import { loginCode, signIn } from "./helpers";

// Visual review, not a regression test: `SCREENS=1 bun run e2e screens`
// writes every page in dark and light at desktop and phone widths.
test.skip(!process.env.SCREENS, "set SCREENS=1 to capture screenshots");

const out = "screenshots";
const sizes = [
  { name: "1440", width: 1440, height: 960 },
  { name: "375", width: 375, height: 812 },
];

async function shot(page: Page, name: string, full = true) {
  await page.waitForTimeout(700);
  await page.screenshot({ path: `${out}/${name}.png`, fullPage: full });
  const wide = await page.evaluate(() => document.documentElement.scrollWidth);
  const vw = page.viewportSize()?.width ?? 0;
  if (wide > vw + 1) console.log(`OVERFLOW ${name}: ${wide}px`);
}

for (const theme of ["dark", "light"] as const) {
  for (const size of sizes) {
    test(`screens ${theme} ${size.name}`, async ({ page, baseURL }) => {
      mkdirSync(out, { recursive: true });
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      const tag = `${theme}-${size.name}`;

      await page.goto("/login?reason=signed-out");
      await shot(page, `login-${tag}`, false);
      await page.goto(`/login#used-${await loginCode(page.request, baseURL!)}`);
      await page.waitForTimeout(300);

      await signIn(page, baseURL!);
      await page.getByRole("heading", { level: 1 }).first().waitFor();
      await shot(page, `activity-${tag}`);

      const irr = page.locator("a[href^='/changes/']").first();
      await irr.click();
      await page.getByRole("heading", { name: "What changed" }).waitFor();
      await shot(page, `change-${tag}`);

      await page.getByRole("button", { name: "Undo this change" }).click();
      await page.getByRole("dialog").waitFor();
      await page.waitForTimeout(400);
      await shot(page, `undo-${tag}`, false);
      await page.keyboard.press("Escape");

      await page.goto("/");
      await page.getByRole("link", { name: /Add a thumbnails bucket/ }).click();
      await page.getByRole("button", { name: "Undo this change" }).click();
      await page.getByLabel("Type notes to confirm").fill("not");
      await shot(page, `undo-serious-${tag}`, false);
      await page.keyboard.press("Escape");

      await page.goto("/status");
      await page.getByRole("heading", { name: "Checks" }).waitFor();
      await shot(page, `status-${tag}`);

      await page.goto("/settings/keys");
      await page.getByText("Claude Code").first().waitFor();
      await shot(page, `keys-${tag}`);

      await page.goto("/settings/keys?create=true");
      await page.getByRole("dialog").waitFor();
      await page.getByLabel("Name").fill("cursor");
      await shot(page, `key-create-${tag}`, false);
      await page.keyboard.press("Escape");

      if (size.width < 768) {
        await page.getByRole("button", { name: "Open navigation" }).click();
        await shot(page, `nav-${tag}`, false);
        await page.keyboard.press("Escape");
      }

      await page.goto("/projects/hello");
      await page.getByRole("list", { name: "What’s in hello" }).waitFor();
      await shot(page, `project-${tag}`);
      await page.goto("/projects/hello/secrets");
      await page.getByText("STRIPE_SECRET_KEY").waitFor();
      await shot(page, `secrets-${tag}`);

      await page.goto("/settings/people");
      await page.getByText("Maya Okafor").waitFor();
      await shot(page, `people-${tag}`);
      await page.goto("/settings/passkeys");
      await page.getByRole("heading", { name: "Your passkeys" }).waitFor();
      await shot(page, `passkeys-${tag}`);

      await page.goto("/");
      await page.keyboard.press("ControlOrMeta+k");
      await page.getByRole("dialog").waitFor();
      await page.keyboard.type("bucket");
      await shot(page, `palette-${tag}`, false);
    });
  }
}
