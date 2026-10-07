import { mkdirSync } from "node:fs";
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { needsServices, ownerToken, pick, signIn } from "./helpers";

// The KV console against a box with a KV (a dev box, see seed-box.sh):
//   KV=1 E2E_BASE_URL=http://localhost:5471 E2E_OWNER_TOKEN=... bunx playwright test kv
// It writes keys under e2e: in the project (E2E_KV_PROJECT, default shop) and
// deletes them again. SHOTS_DIR gets screenshots at 1440 and 390, light and dark.
test.skip(!process.env.KV, "set KV=1 for a box with a KV");
needsServices(process.env.E2E_KV_PROJECT ?? "shop", ["valkey"]);
test.describe.configure({ mode: "serial" });

const project = process.env.E2E_KV_PROJECT ?? "shop";
const out = process.env.SHOTS_DIR ?? "screenshots/kv";
const kvUrl = (q = "") => `/projects/${project}/data/kv${q}`;

async function cli(request: APIRequestContext, baseURL: string, commands: string) {
  const res = await request.post(`${baseURL}/v1/projects/${project}/kv/command`, {
    headers: { Authorization: `Bearer ${ownerToken()}` },
    data: { commands, write: true },
  });
  expect(res.ok(), await res.text()).toBeTruthy();
  return (await res.json()).results as Array<{ reply: unknown; error?: string }>;
}

async function wipe(request: APIRequestContext, baseURL: string, prefix: string) {
  const h = { Authorization: `Bearer ${ownerToken()}` };
  const first = await request.post(`${baseURL}/v1/projects/${project}/kv/delete-prefix`, { headers: h, data: { prefix } });
  if (first.status() !== 428) return;
  const { confirm } = await first.json();
  await request.post(`${baseURL}/v1/projects/${project}/kv/delete-prefix`, { headers: h, data: { prefix, confirm } });
}

/** Zero serious or critical accessibility problems on what is on screen. */
async function axe(page: Page, what: string) {
  await page.waitForTimeout(400); // let toasts and dialogs finish fading in
  const r = await new AxeBuilder({ page }).analyze();
  const bad = r.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(bad.map((v) => `${what}: ${v.id} ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
}

const toast = (page: Page, text: string | RegExp) => page.getByRole("status").filter({ hasText: text }).first();

async function openKey(page: Page, key: string) {
  await page.goto(kvUrl(`?key=${encodeURIComponent(key)}`));
  await page.getByRole("heading", { name: key, exact: true }).waitFor();
}

test.beforeAll(async ({ request, baseURL }) => {
  await wipe(request, baseURL!, "e2e:");
});
test.afterAll(async ({ request, baseURL }) => {
  await wipe(request, baseURL!, "e2e:");
  await wipe(request, baseURL!, "big:");
});

test.beforeEach(async ({ page, baseURL }) => {
  page.setDefaultTimeout(15_000);
  await page.emulateMedia({ reducedMotion: "reduce" });
  page.on("pageerror", (e) => console.log(`PAGEERROR ${e.message}`));
  await signIn(page, baseURL!);
});

test("make a key of every type with New key", async ({ page }) => {
  await page.goto(kvUrl());
  const make = async (type: string, name: string, fill: () => Promise<void>) => {
    await page.getByRole("button", { name: "New key" }).click();
    const d = page.getByRole("dialog", { name: "New key" });
    await d.getByRole("radio", { name: new RegExp(`^${type}`) }).click();
    await d.getByRole("textbox", { name: "Name", exact: true }).fill(name);
    await fill();
    await d.getByRole("button", { name: "Make key" }).click();
    await expect(page.getByRole("heading", { name, exact: true })).toBeVisible();
  };
  const d = () => page.getByRole("dialog", { name: "New key" });
  await make("Text", "e2e:settings", async () => {
    await d().getByRole("textbox", { name: "Value", exact: true }).fill('{"theme":"dark","beta":true}');
  });
  // JSON opens as a tree; Edit shows the text.
  await expect(page.getByRole("group", { name: "Value of e2e:settings, as a tree" })).toContainText('"theme": "dark"');
  await page.getByRole("radio", { name: "Edit" }).click();
  await expect(page.getByLabel("Value of e2e:settings", { exact: true })).toHaveValue('{"theme":"dark","beta":true}');
  await axe(page, "text key");

  await make("Hash", "e2e:session:u_2041", async () => {
    await d().getByLabel("field 1", { exact: true }).fill("plan");
    await d().getByLabel("value 1", { exact: true }).fill("pro");
    await d().getByRole("button", { name: "Another field" }).click();
    await d().getByLabel("field 2", { exact: true }).fill("theme");
    await d().getByLabel("value 2", { exact: true }).fill("dark");
  });
  await expect(page.getByRole("gridcell", { name: "pro", exact: true })).toBeVisible();

  await make("List", "e2e:recent", async () => {
    await d().getByRole("textbox", { name: "Items, one per line" }).fill("1042\n1041\n1040");
  });
  await expect(page.getByRole("gridcell", { name: "1041", exact: true })).toBeVisible();

  await make("Set", "e2e:tags", async () => {
    await d().getByRole("textbox", { name: "Members, one per line" }).fill("kitchen\nceramic");
  });
  await expect(page.getByRole("gridcell", { name: "ceramic", exact: true })).toBeVisible();

  await make("Sorted set", "e2e:leaderboard", async () => {
    for (const [i, [m, s]] of [["BWL-01", "12"], ["CUP-07", "48"], ["TIF-03", "31"]].entries()) {
      if (i > 0) await d().getByRole("button", { name: "Another member" }).click();
      await d().getByLabel(`member ${i + 1}`, { exact: true }).fill(m);
      await d().getByLabel(`score ${i + 1}`, { exact: true }).fill(s);
    }
  });
  // Highest score first, ranked.
  const first = page.getByRole("row").nth(1);
  await expect(first).toContainText("CUP-07");
  await expect(first).toContainText("48");

  await make("Stream", "e2e:events", async () => {
    await d().getByLabel("field 1", { exact: true }).fill("type");
    await d().getByLabel("value 1", { exact: true }).fill("signup");
  });
  await expect(page.getByRole("gridcell", { name: "type=signup", exact: true })).toBeVisible();

  // An existing name is refused.
  await page.getByRole("button", { name: "New key" }).click();
  await d().getByRole("radio", { name: /^Text/ }).click();
  await d().getByRole("textbox", { name: "Name", exact: true }).fill("e2e:settings");
  await d().getByRole("button", { name: "Make key" }).click();
  await expect(toast(page, "already exists")).toBeVisible();
  await page.keyboard.press("Escape");
});

test("edit values, with Undo", async ({ page }) => {
  // Text: save, then Undo puts it back.
  await openKey(page, "e2e:settings");
  await page.getByRole("radio", { name: "Edit" }).click();
  const box = page.getByLabel("Value of e2e:settings", { exact: true });
  await page.getByRole("button", { name: "Format" }).click();
  await expect(box).toHaveValue(/\n {2}"theme": "dark"/);
  await box.fill('{"theme":"light"}');
  await page.keyboard.press("Meta+Enter");
  await expect(toast(page, "Saved e2e:settings")).toBeVisible();
  await toast(page, "Saved e2e:settings").getByRole("button", { name: "Undo" }).click();
  await expect(toast(page, "Put back as it was")).toBeVisible();
  await expect(box).toHaveValue('{"theme":"dark","beta":true}');

  // Hash: Enter edits a cell, Enter saves; add a field; Delete removes one.
  await openKey(page, "e2e:session:u_2041");
  const plan = page.getByRole("gridcell", { name: "pro", exact: true });
  await plan.click();
  await plan.press("Enter");
  await page.getByLabel("Edit Value", { exact: true }).fill("team");
  await page.getByLabel("Edit Value", { exact: true }).press("Enter");
  await expect(toast(page, "Saved plan on e2e:session:u_2041")).toBeVisible();
  await expect(page.getByRole("gridcell", { name: "team", exact: true })).toBeVisible();
  await page.getByLabel("New field", { exact: true }).fill("lastSeen");
  await page.getByLabel("Its value", { exact: true }).fill("1759741964");
  await page.getByRole("button", { name: "Add field" }).click();
  await expect(page.getByRole("gridcell", { name: /1759741964/ })).toBeVisible();
  await page.getByRole("gridcell", { name: "theme", exact: true }).click();
  await page.keyboard.press("Delete");
  await expect(toast(page, "Deleted theme from e2e:session:u_2041")).toBeVisible();
  await expect(page.getByRole("gridcell", { name: "theme", exact: true })).toHaveCount(0);
  await axe(page, "hash key");

  // Sorted set: a new score reorders it.
  await openKey(page, "e2e:leaderboard");
  const score = page.getByRole("row").filter({ hasText: "BWL-01" }).getByRole("gridcell").nth(2);
  await score.dblclick();
  await page.getByLabel("Edit Score", { exact: true }).fill("99");
  await page.getByLabel("Edit Score", { exact: true }).press("Enter");
  await expect(page.getByRole("row").nth(1)).toContainText("BWL-01");

  // List: add to the end, remove the first.
  await openKey(page, "e2e:recent");
  await page.getByLabel("New item", { exact: true }).fill("1043");
  await page.getByRole("button", { name: "Add to end" }).click();
  await expect(page.getByRole("gridcell", { name: "1043", exact: true })).toBeVisible();
  await page.getByRole("gridcell", { name: "1042", exact: true }).click();
  await page.keyboard.press("Delete");
  await expect(page.getByRole("gridcell", { name: "1042", exact: true })).toHaveCount(0);

  // Stream: add an entry; newest first.
  await openKey(page, "e2e:events");
  await page.getByLabel("Field 1", { exact: true }).fill("type");
  await page.getByLabel("Value 1", { exact: true }).fill("order");
  await page.getByRole("button", { name: "Add entry" }).click();
  await expect(page.getByRole("row").nth(1)).toContainText("type=order");
});

test("expiry: add one, keep forever", async ({ page }) => {
  await openKey(page, "e2e:tags");
  await expect(page.getByText("Kept until deleted", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Add an expiry" }).click();
  await page.getByLabel("Expire in", { exact: true }).fill("2");
  await pick(page, page.getByRole("combobox", { name: "Unit", exact: true }), "hours");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText(/in (1 h 59 min|2 h)/)).toBeVisible();
  await page.getByRole("button", { name: "Keep forever" }).click();
  await expect(page.getByText("Kept until deleted", { exact: true })).toBeVisible();
});

test("rename and delete a key, with Undo", async ({ page }) => {
  await openKey(page, "e2e:tags");
  await page.getByRole("button", { name: "Rename" }).click();
  await page.getByLabel("New name", { exact: true }).fill("e2e:labels");
  await page.getByLabel("New name", { exact: true }).press("Enter");
  await expect(page.getByRole("heading", { name: "e2e:labels", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Delete key" }).click();
  await expect(toast(page, "Deleted e2e:labels")).toBeVisible();
  await toast(page, "Deleted e2e:labels").getByRole("button", { name: "Undo" }).click();
  await openKey(page, "e2e:labels");
});

test("delete a prefix: count first, then Undo", async ({ page, request, baseURL }) => {
  await cli(request, baseURL!, "SET e2e:grp:a 1\nSET e2e:grp:b 2\nSET e2e:grp:c 3");
  await page.goto(kvUrl());
  const tree = page.getByRole("tree", { name: "Keys" });
  await tree.getByRole("treeitem", { name: /^e2e:/ }).click();
  const grp = tree.getByRole("treeitem", { name: /^grp:\s*3/ });
  await grp.focus();
  await page.keyboard.press("Delete");
  const d = page.getByRole("dialog");
  await expect(d.getByRole("heading", { name: "Delete 3 keys starting with e2e:grp:?" })).toBeVisible();
  await axe(page, "prefix confirm");
  await d.getByRole("button", { name: "Delete 3 keys" }).click();
  await expect(toast(page, "Deleted every key starting with e2e:grp:")).toBeVisible();
  await expect(tree.getByRole("treeitem", { name: /^grp:/ })).toHaveCount(0);
  await toast(page, "Deleted every key starting with e2e:grp:").getByRole("button", { name: "Undo" }).click();
  await expect(tree.getByRole("treeitem", { name: /^grp:\s*3/ })).toBeVisible();
});

test("console: read, then write only with Allow changes", async ({ page }) => {
  await page.goto(kvUrl("/console"));
  const cmd = page.getByLabel("Command", { exact: true });
  await cmd.fill('SET e2e:hello "hello world"');
  await cmd.press("Enter");
  await expect(page.getByRole("log")).toContainText("turn on Allow changes");
  await page.getByRole("switch", { name: /Allow changes/ }).click();
  await cmd.fill('SET e2e:hello "hello world"');
  await cmd.press("Enter");
  await expect(page.getByRole("log")).toContainText('"OK"');
  await cmd.fill("GET e2e:hello\nKEYS e2e:h*");
  await cmd.press("Enter");
  await expect(page.getByRole("log")).toContainText('"hello world"');
  await expect(page.getByRole("log")).toContainText('1) "e2e:hello"');
  await cmd.fill("FLUSHALL");
  await cmd.press("Enter");
  await expect(page.getByRole("log")).toContainText("admin only");
  await axe(page, "console");
});

test("50,000 keys scroll smoothly", async ({ page, request, baseURL }) => {
  test.setTimeout(180_000);
  await cli(request, baseURL!, `EVAL "for i=1,50000 do redis.call('SET', KEYS[1]..i, i) end" 1 big:`);
  await page.goto(kvUrl());
  const tree = page.getByRole("tree", { name: "Keys" });
  const t0 = Date.now();
  await tree.getByRole("treeitem", { name: /^big:\s*50,000/ }).click();
  await tree.getByRole("treeitem", { name: /^1\b/ }).first().waitFor();
  const opened = Date.now() - t0;
  // Scroll down through the group: the list keeps only what is on screen.
  const stats = await page.evaluate(async () => {
    const el = document.querySelector<HTMLElement>('[role="tree"]')!;
    const gaps: number[] = [];
    let last = performance.now();
    for (let i = 0; i < 90; i++) {
      el.scrollTop += 400;
      await new Promise((r) => requestAnimationFrame(r));
      const now = performance.now();
      gaps.push(now - last);
      last = now;
    }
    gaps.sort((a, b) => a - b);
    return { rows: el.querySelectorAll('[role="treeitem"]').length, p95: gaps[Math.floor(gaps.length * 0.95)] };
  });
  console.log(`50k keys: group opened in ${opened} ms, ${stats.rows} rows in the DOM, p95 frame ${stats.p95.toFixed(1)} ms`);
  expect(stats.rows).toBeLessThan(120);
  expect(stats.p95).toBeLessThan(50);
});

for (const theme of ["light", "dark"] as const) {
  for (const w of [1440, 390]) {
    test(`screens ${theme} ${w}`, async ({ page }) => {
      test.setTimeout(120_000);
      mkdirSync(out, { recursive: true });
      await page.setViewportSize(w === 390 ? { width: 390, height: 844 } : { width: 1440, height: 900 });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      const shot = async (name: string, full = true) => {
        await page.waitForTimeout(500);
        await page.screenshot({ path: `${out}/kv-${name}-${theme}-${w}.png`, fullPage: full });
        const wide = await page.evaluate(() => document.documentElement.scrollWidth);
        expect(wide, `${name} scrolls sideways`).toBeLessThanOrEqual(w + 1);
      };
      await page.goto(kvUrl());
      await page.getByRole("tree", { name: "Keys" }).getByRole("treeitem").first().waitFor();
      await shot("keys");
      await openKey(page, "e2e:session:u_2041");
      await shot("hash");
      await axe(page, `hash ${theme} ${w}`);
      await openKey(page, "e2e:leaderboard");
      await shot("zset");
      await openKey(page, "e2e:settings");
      await shot("text");
      await page.goto(kvUrl());
      await page.getByRole("button", { name: "New key" }).click();
      await page.getByRole("dialog").getByRole("radio", { name: /^Hash/ }).click();
      await shot("new", false);
      await axe(page, `new key ${theme} ${w}`);
      await page.keyboard.press("Escape");
      await page.goto(kvUrl("/console"));
      await page.getByLabel("Command", { exact: true }).fill("HGETALL e2e:session:u_2041");
      await page.getByLabel("Command", { exact: true }).press("Enter");
      await page.getByRole("log").getByText("1) ").first().waitFor();
      await shot("console");
      await axe(page, `console ${theme} ${w}`);
    });
  }
}
