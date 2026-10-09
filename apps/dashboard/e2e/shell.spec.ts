import { mkdirSync } from "node:fs";
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { signIn } from "./helpers";

// The app shell: the switcher keeps your section, ⌘K reaches every page and
// action, `?` lists the shortcuts, the Usage roll-up and the Connect dialog.
// Changes nothing on the box (standalone.spec.ts creates a project).
//   SCREENS=1 SHOTS_DIR=/tmp/shots bunx playwright test shell   also writes 1440/390 light/dark screenshots

const MB = 1048576;

/** Zero serious or critical axe violations on what is on screen now. */
async function axe(page: Page, what: string) {
  // Measure once nothing is fading in: mid-animation colours aren't the page's.
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity));
  const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
  const bad = r.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(bad.map((v) => `${what}: ${v.id} ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
}

/** The box measured, as a running box answers (a laptop dev server can't measure). */
async function measured(page: Page) {
  const project = (name: string, mem: number, cpu: number) => ({
    project: name,
    memoryBytes: mem * MB,
    cacheBytes: 0,
    cpuPercent: cpu,
    diskBytes: 0,
    swapBytes: 0,
    limitBytes: 0,
    limitSource: "automatic",
    pressure: "none",
    budget: { auto: true },
  });
  await page.route("**/v1/box/resources", (r) =>
    r.fulfill({
      json: {
        hostname: "box",
        sampledAt: new Date().toISOString(),
        uptimeSeconds: 3600,
        windowSeconds: 5,
        memory: { totalBytes: 4096 * MB, availableBytes: 2900 * MB },
        cpu: { count: 2, usedPercent: 12 },
        disks: { data: { totalBytes: 40960 * MB, usedBytes: 5120 * MB, freeBytes: 35840 * MB } },
        apps: [],
        services: [],
        projects: [project("hello", 310, 4), project("notes", 120, 1)],
      },
    }),
  );
  const usage = (name: string, db: number, kv: number | null, files: number, mem: number, cpu: number) => ({
    project: name,
    budget: { auto: true },
    limitSource: "automatic",
    memory: { usedBytes: mem * MB, cacheBytes: 0, limitBytes: 3000 * MB, headroomBytes: 2000 * MB, protectedBytes: 0, swapBytes: 0, limitSource: "automatic", pressure: "none" },
    cpu: { percent: cpu, weight: 100, limitSource: "automatic" },
    disk: { databaseBytes: db * MB, filesBytes: files * MB, kvBytes: (kv ?? 0) * MB, totalBytes: (db + files) * MB, folders: [] },
    apps: [],
    services: { postgres: { databaseBytes: db * MB, connections: 2 }, storage: { buckets: 2, bytes: files * MB, objects: 12 }, ...(kv === null ? {} : { valkey: { keys: 40, memoryBytes: kv * MB } }) },
    sharePercent: 0,
    limitEvents: [],
    builds: {},
    sampledAt: new Date().toISOString(),
  });
  // Each project's disk by part, as the box measures it once a minute.
  const disk = (name: string, db: number, kv: number, files: number) => ({
    project: name,
    exists: true,
    databaseBytes: db * MB,
    filesBytes: files * MB,
    kvBytes: kv * MB,
    imageBytes: 0,
    images: 0,
    logBytes: 0,
    buildBytes: 0,
    backupBytes: 0,
    totalBytes: (db + kv + files) * MB,
  });
  await page.route("**/v1/box/disk", (r) =>
    r.fulfill({
      json: {
        measuredAt: new Date().toISOString(),
        disk: { mount: "/var/lib/tiffin", totalBytes: 40960 * MB, usedBytes: 5120 * MB, freeBytes: 35840 * MB, usedPercent: 12.5 },
        parts: [],
        projects: [disk("notes", 920, 0, 2), disk("hello", 48, 3, 12)],
        imagesBytes: 0,
        unusedImages: 0,
        unusedImageBytes: 0,
        buildCacheBytes: 0,
        buildCacheCapBytes: 4096 * MB,
      },
    }),
  );
  await page.route("**/v1/projects/hello/usage", (r) => r.fulfill({ json: usage("hello", 48, 3, 12, 310, 4) }));
  await page.route("**/v1/projects/notes/usage", (r) => r.fulfill({ json: usage("notes", 920, null, 2, 120, 1) }));
}

const mod = process.platform === "darwin" ? "Meta" : "Control";

test.beforeEach(async ({ page, baseURL }) => {
  page.setDefaultTimeout(15_000);
  await signIn(page, baseURL!);
});

test("switching projects keeps your section, or says why not", async ({ page }) => {
  await page.goto("/projects/hello/data/sql");
  await page.getByRole("heading", { level: 1, name: "Database" }).waitFor();
  await page.locator("main").click({ position: { x: 5, y: 5 } });
  await page.keyboard.press("g");
  await page.keyboard.press("p");
  await page.getByPlaceholder("Find a project…").fill("notes");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/projects\/notes\/data\/sql$/);

  // Every project has KV: switching from one's KV lands on the other's.
  await page.goto("/projects/hello/data/kv");
  await page.getByRole("heading", { level: 1, name: "KV" }).waitFor();
  await page.getByRole("button", { name: /Switch project/ }).first().click();
  await page.getByRole("option", { name: "notes" }).click();
  await expect(page).toHaveURL(/\/projects\/notes\/data\/kv$/);

  // Recent first: the project just left leads the switcher's list.
  await page.getByRole("button", { name: /Switch project/ }).first().click();
  await expect(page.getByRole("option").first()).toHaveText(/notes|hello/);
  await page.keyboard.press("Escape");
});

test("⌘K jumps to any project's page and runs actions", async ({ page }) => {
  await page.goto("/projects/hello");
  await page.getByRole("heading", { level: 1 }).waitFor();
  await page.keyboard.press(`${mod}+k`);
  const input = page.getByPlaceholder(/Jump to a page/);
  await input.fill("notes files");
  await expect(page.getByRole("option").first()).toContainText("Files");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/projects\/notes\/storage$/);

  // Fuzzy, words in any order.
  await page.keyboard.press(`${mod}+k`);
  await input.fill("db hello");
  await expect(page.getByRole("option").first()).toContainText("Database");
  await axe(page, "palette");

  // An action opens its page and does it there: Connect to the database.
  await input.fill("connect database hello");
  await page.getByRole("option", { name: /Connect to the database/ }).first().click();
  await expect(page).toHaveURL(/\/projects\/hello\/data$/);
  await expect(page.getByRole("dialog", { name: "Connect to the database" })).toBeVisible();
  await page.keyboard.press("Escape");

  // What you picked comes back first next time.
  await page.keyboard.press(`${mod}+k`);
  await expect(page.getByRole("group", { name: "Recent" })).toContainText("Connect to the database");
  await page.keyboard.press("Escape");
});

test("? lists the shortcuts; letters typed into a field stay there", async ({ page }) => {
  await page.goto("/projects/hello");
  await page.getByRole("heading", { level: 1 }).waitFor();
  await page.locator("main").click({ position: { x: 5, y: 5 } });
  await page.keyboard.press("?");
  const sheet = page.getByRole("dialog", { name: "Keyboard shortcuts" });
  await expect(sheet).toBeVisible();
  for (const label of ["Search and commands", "Switch project", "Database", "KV", "Files", "Jobs"]) await expect(sheet.getByText(label, { exact: true })).toBeVisible();
  await axe(page, "shortcuts");
  await page.keyboard.press("Escape");

  await page.keyboard.press("g");
  await page.keyboard.press("d");
  await expect(page).toHaveURL(/\/projects\/hello\/data$/);

  await page.goto("/new");
  await page.locator("#pname").click();
  await page.keyboard.type("gd");
  await expect(page).toHaveURL(/\/new$/);
});

test("Usage lists every project's footprint, sorts, and links in", async ({ page }) => {
  await measured(page);
  await page.goto("/usage");
  const list = page.getByRole("region", { name: "By project" }).getByRole("list", { name: "Each project’s footprint" });
  await expect(list.getByRole("listitem")).toHaveCount((await page.request.get("/v1/projects").then((r) => r.json())).length);
  // By its share of the box first (hello holds the most memory); then by disk, biggest first.
  await expect(list.getByRole("listitem").first()).toContainText("hello");
  const sort = page.getByRole("group", { name: "Sort projects" });
  await sort.getByRole("button", { name: "Disk" }).click();
  await expect(sort.getByRole("button", { name: "Disk" })).toHaveAttribute("aria-pressed", "true");
  const top = list.getByRole("listitem").first();
  await expect(top).toContainText("notes");
  await expect(top).toContainText(/922\sMB/);
  await axe(page, "usage");
  // A row opens to its disk by part and a link to its resources.
  await top.getByRole("button", { name: /^notes/ }).click();
  await expect(top.getByText("Database", { exact: true })).toBeVisible();
  await top.getByRole("link", { name: "notes’s resources and limits" }).click();
  await expect(page).toHaveURL(/\/projects\/notes\/usage$/);
});

test("Connect shows the env, the tunnel and what's not open yet", async ({ page }) => {
  await page.goto("/projects/hello/data");
  await page.getByRole("button", { name: "Connect" }).click();
  const d = page.getByRole("dialog", { name: "Connect to the database" });
  await expect(d.getByText("DATABASE_URL", { exact: true })).toBeVisible();
  await expect(d.getByText(/postgresql:\/\/p_hello:•+@127\.0\.0\.1:5432\/p_hello/)).toBeVisible();
  for (const lang of ["Drizzle", "Prisma", "postgres.js", "Python"]) await expect(d.getByRole("tab", { name: lang })).toBeVisible();
  await d.getByRole("tab", { name: "Python" }).click();
  await expect(d.getByText("psycopg.connect", { exact: false })).toBeVisible();
  await axe(page, "connect");
  await d.getByRole("tab", { name: "From your computer" }).click();
  await expect(d.getByText("tiffin db tunnel hello")).toBeVisible();
  await d.getByRole("tab", { name: "From anywhere" }).click();
  await expect(d.getByText("Not yet.")).toBeVisible();
  await page.keyboard.press("Escape");

  await page.goto("/projects/hello/data/kv");
  await page.getByRole("button", { name: "Connect" }).click();
  const kv = page.getByRole("dialog", { name: "Connect to KV" });
  for (const lang of ["@shiptiffin/sdk", "iovalkey", "From Vercel KV"]) await expect(kv.getByRole("tab", { name: lang })).toBeVisible();
  await kv.getByRole("tab", { name: "From your computer" }).click();
  await expect(kv.getByText("tiffin kv tunnel hello")).toBeVisible();
});

// Visual review, at 1440 and 390, light and dark.
test("shell screens", async ({ page }) => {
  test.skip(!process.env.SCREENS, "set SCREENS=1 for screenshots");
  test.setTimeout(240_000);
  const out = process.env.SHOTS_DIR ?? "screenshots/shell";
  mkdirSync(out, { recursive: true });
  await measured(page);
  for (const theme of ["light", "dark"] as const) {
    for (const [w, h] of [
      [1440, 900],
      [390, 844],
    ]) {
      await page.setViewportSize({ width: w, height: h });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      const shot = async (name: string, fullPage = false) => {
        await page.waitForTimeout(500);
        await page.screenshot({ path: `${out}/${name}-${theme}-${w}.png`, fullPage });
        const wide = await page.evaluate(() => document.documentElement.scrollWidth);
        expect(wide, `${name} scrolls sideways`).toBeLessThanOrEqual(w);
      };
      await page.goto("/projects/hello");
      await page.getByRole("heading", { level: 1 }).waitFor();
      await page.keyboard.press(`${mod}+k`);
      await page.getByPlaceholder(/Jump to a page/).fill("notes");
      await shot("palette");
      await page.keyboard.press("Escape");
      await page.locator("main").click({ position: { x: 5, y: 5 } });
      await page.keyboard.press("?");
      await page.getByRole("dialog", { name: "Keyboard shortcuts" }).waitFor();
      await shot("shortcuts");
      await page.keyboard.press("Escape");
      await page.goto("/projects/hello/data");
      await page.getByRole("button", { name: "Connect" }).click();
      await page.getByRole("dialog", { name: "Connect to the database" }).waitFor();
      await shot("connect");
      await page.keyboard.press("Escape");
      await page.goto("/projects/hello/settings#danger");
      await page.getByRole("heading", { name: "Delete this project" }).waitFor();
      await shot("danger-zone");
      await page.goto("/usage");
      await page.getByRole("region", { name: "By project" }).getByRole("listitem").first().waitFor();
      await shot("usage", true);
      await page.goto("/projects/hello/data/kv");
      await page.getByRole("button", { name: /Switch project/ }).first().waitFor();
      await page.goto("/projects/notes");
      await shot("overview");
    }
  }
});
