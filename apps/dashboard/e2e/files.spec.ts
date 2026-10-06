import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { ownerToken, signIn } from "./helpers";

// The Files console against a box with storage (a dev box, see seed-box.sh):
//   FILES=1 E2E_BASE_URL=https://dashboard.<domain>:<port> E2E_OWNER_TOKEN=... bunx playwright test files
// It makes a bucket called e2e-media in the project (E2E_FILES_PROJECT,
// default shop), works in it, and deletes it (and its trash) at the end.
// SHOTS_DIR gets screenshots at 1440 and 390, light and dark.
test.skip(!process.env.FILES || !process.env.E2E_OWNER_TOKEN, "set FILES=1 and E2E_OWNER_TOKEN for a box with storage");
test.describe.configure({ mode: "serial" });

const project = process.env.E2E_FILES_PROJECT ?? "shop";
const bucket = "e2e-media";
const out = process.env.SHOTS_DIR ?? "screenshots/files";
const fx = (name: string) => readFileSync(new URL(`./fixtures/${name}`, import.meta.url));
const filesUrl = (q = "") => `/projects/${project}/storage${q}`;
const bucketUrl = (q = "") => `/projects/${project}/storage/${bucket}${q}`;

const auth = () => ({ Authorization: `Bearer ${ownerToken()}` });

async function put(request: APIRequestContext, baseURL: string, key: string, text: string) {
  for (let i = 0; ; i++) {
    const res = await request.put(`${baseURL}/v1/projects/${project}/storage/buckets/${bucket}/objects`, { headers: auth(), data: { key, text } });
    if (res.status() === 429 && i < 30) {
      await new Promise((r) => setTimeout(r, 1000)); // the box's rate limit: wait it out
      continue;
    }
    expect(res.ok(), await res.text()).toBeTruthy();
    return;
  }
}

/** Zero serious or critical accessibility problems on what is on screen. */
async function axe(page: Page, what: string) {
  await page.waitForTimeout(400);
  const r = await new AxeBuilder({ page }).analyze();
  const bad = r.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  expect(bad.map((v) => `${what}: ${v.id} ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
}

const toast = (page: Page, text: string | RegExp) => page.getByRole("status").filter({ hasText: text }).first();
const grid = (page: Page) => page.getByRole("grid", { name: /^Files in/ });
const cell = (page: Page, name: string) => grid(page).getByRole("gridcell", { name, exact: true });

/** Waits for a new bucket to be made on the box. */
async function settle(page: Page) {
  await expect(page.getByText(/being made|Making the bucket/)).toHaveCount(0, { timeout: 60_000 });
}

/** Takes the bucket out of the project (if a run left it) and empties it from the trash. */
async function removeBucket(request: APIRequestContext, baseURL: string) {
  const m = await (await request.get(`${baseURL}/v1/projects/${project}/manifest`, { headers: auth() })).json();
  const buckets = m.manifest?.services?.storage?.buckets;
  const had = !!buckets?.[bucket];
  if (had) {
    delete buckets[bucket];
    const plan = await (await request.post(`${baseURL}/v1/plan`, { headers: auth(), data: { manifest: m.manifest } })).json();
    const res = await request.post(`${baseURL}/v1/apply`, { headers: auth(), data: { manifest: m.manifest, confirm: plan.hash, intent: "Remove the e2e bucket" } });
    expect(res.ok(), await res.text()).toBeTruthy();
  }
  // The box moves it to the trash as it converges: wait for that, then empty it (else making it again brings it back).
  for (let i = 0; i < 60; i++) {
    const trash = (await (await request.get(`${baseURL}/v1/storage/trash?project=${project}`, { headers: auth() })).json()) as Array<{ id: string; bucket: string }> | null;
    const mine = (trash ?? []).filter((t) => t.bucket === bucket);
    for (const t of mine) await request.delete(`${baseURL}/v1/storage/trash/${t.id}`, { headers: auth() });
    if (mine.length > 0 || (!had && i > 0)) return;
    await new Promise((r) => setTimeout(r, 500));
  }
}

test.beforeAll(async ({ request, baseURL }) => {
  await removeBucket(request, baseURL!);
});

test.beforeEach(async ({ page, baseURL }) => {
  page.setDefaultTimeout(20_000);
  await page.emulateMedia({ reducedMotion: "reduce" });
  page.on("pageerror", (e) => console.log(`PAGEERROR ${e.message}`));
  await signIn(page, baseURL!);
});

test("make a bucket with New bucket", async ({ page }) => {
  await page.goto(filesUrl());
  await page.getByRole("button", { name: "New bucket" }).first().click();
  const d = page.getByRole("dialog", { name: "New bucket" });
  await d.getByRole("textbox", { name: "Name" }).fill("Bad Name");
  await expect(d.getByRole("textbox", { name: "Name" })).toHaveValue("bad name");
  await expect(d.getByText("Use lowercase letters")).toBeVisible();
  await d.getByRole("textbox", { name: "Name" }).fill(bucket);
  await axe(page, "new bucket");
  await d.getByRole("button", { name: "Make bucket" }).click();
  await expect(page).toHaveURL(new RegExp(`/storage/${bucket}`));
  await expect(toast(page, `Added the ${bucket} bucket`)).toBeVisible({ timeout: 30_000 });
  await settle(page);
  await expect(page.getByText("No files yet.")).toBeVisible({ timeout: 30_000 });
});

test("upload files and a folder, with the bucket's rules checked first", async ({ page, request, baseURL }) => {
  await page.goto(bucketUrl());
  const chooser = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: /^Upload/ }).click();
  await page.getByRole("menuitem", { name: "Files…" }).click();
  await (await chooser).setFiles([
    { name: "bowl.png", mimeType: "image/png", buffer: fx("bowl.png") },
    { name: "receipt.pdf", mimeType: "application/pdf", buffer: fx("receipt-1042.pdf") },
    { name: "notes.json", mimeType: "application/json", buffer: Buffer.from('{"shelf":"B4","count":12}') },
  ]);
  const tray = page.getByRole("region", { name: "Uploads" });
  await expect(tray.getByRole("status")).toHaveText(/Uploaded 3 files/, { timeout: 30_000 });
  await expect(cell(page, "bowl.png")).toBeVisible();
  // A folder keeps its paths.
  const dir = join(mkdtempSync(join(tmpdir(), "files-e2e-")), "covers");
  mkdirSync(join(dir, "2026"), { recursive: true });
  writeFileSync(join(dir, "2026", "sunset.png"), fx("sunset.png"));
  writeFileSync(join(dir, "2026", "pattern.png"), fx("pattern.png"));
  await page.locator('input[type="file"][webkitdirectory]').setInputFiles(dir);
  await expect(tray.getByRole("status")).toHaveText(/Uploaded 5 files/, { timeout: 30_000 });
  await expect(cell(page, "covers")).toBeVisible();
  await axe(page, "after uploads");

  // Rules: images only, up to 1 MB. A file over them is refused before a byte moves.
  await page.getByRole("button", { name: "Settings" }).click();
  const s = page.getByRole("dialog", { name: `${bucket} settings` });
  await s.getByRole("combobox", { name: "Largest file" }).selectOption({ label: "1 MB" });
  await expect(toast(page, `Limited files in ${bucket} to 1 MB`)).toBeVisible({ timeout: 30_000 });
  await s.getByRole("checkbox", { name: "Images" }).click();
  await expect(toast(page, `Let ${bucket} take images only`)).toBeVisible({ timeout: 30_000 });
  await axe(page, "settings");
  await page.keyboard.press("Escape");
  await page.reload();
  await page.locator('input[type="file"]:not([webkitdirectory])').setInputFiles([
    { name: "big.png", mimeType: "image/png", buffer: Buffer.alloc(1_100_000, 1) },
    { name: "song.mp3", mimeType: "audio/mpeg", buffer: Buffer.from("ID3") },
  ]);
  await expect(tray.getByText(/Too big: e2e-media takes files up to 1\sMB/)).toBeVisible();
  await expect(tray.getByText(/takes images only; this is audio\/mpeg/)).toBeVisible();
  // The API says the same to anyone who skips the console.
  const res = await request.post(`${baseURL}/v1/projects/${project}/storage/buckets/${bucket}/uploads`, {
    headers: auth(),
    data: { key: "x.mp3", size: 10, contentType: "audio/mpeg" },
  });
  expect(res.status()).toBe(422);
  // Undo the rules from the toast-less way: open settings and clear them.
  await page.getByRole("button", { name: "Settings" }).click();
  await s.getByRole("combobox", { name: "Largest file" }).selectOption({ label: "No limit" });
  await s.getByRole("checkbox", { name: "Images" }).click();
  await expect(toast(page, `Let ${bucket} take any type of file`)).toBeVisible({ timeout: 30_000 });
  await page.keyboard.press("Escape");
});

test("a large file goes in parts: pause, resume, done", async ({ page }) => {
  test.setTimeout(180_000);
  await page.goto(bucketUrl());
  // Slow the connection so there is time to pause.
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Network.enable");
  await cdp.send("Network.emulateNetworkConditions", { offline: false, latency: 20, downloadThroughput: -1, uploadThroughput: 4 * 1024 * 1024 });
  const size = 20 * 1024 * 1024;
  await page.locator('input[type="file"]:not([webkitdirectory])').setInputFiles([{ name: "launch-video.mp4", mimeType: "video/mp4", buffer: Buffer.alloc(size, 7) }]);
  const tray = page.getByRole("region", { name: "Uploads" });
  await expect(tray.getByText("Sends in parts")).toBeVisible();
  await tray.getByRole("progressbar").waitFor();
  await page.waitForTimeout(1500);
  await tray.getByRole("button", { name: "Pause launch-video.mp4" }).click();
  await expect(tray.getByText(/Paused at \d+%/)).toBeVisible();
  await axe(page, "paused upload");
  await cdp.send("Network.emulateNetworkConditions", { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
  await tray.getByRole("button", { name: "Resume launch-video.mp4" }).click();
  await expect(tray.getByRole("status")).toHaveText(/Uploaded/, { timeout: 120_000 });
  await expect(cell(page, "launch-video.mp4")).toBeVisible();
  await cell(page, "launch-video.mp4").click();
  await expect(page.getByRole("complementary", { name: "File" })).toContainText("20 MB");
});

test("browse: folders, search by name, sort, grid with thumbnails, keyboard", async ({ page }) => {
  await page.goto(bucketUrl());
  await cell(page, "covers").click();
  await expect(page).toHaveURL(/prefix=covers%2F/);
  await cell(page, "2026").focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("navigation", { name: "Folder" })).toContainText("2026");
  // Back up with Alt+↑ from the grid.
  await cell(page, "pattern.png").focus();
  await page.keyboard.press("Alt+ArrowUp");
  await page.keyboard.press("Alt+ArrowUp");
  await expect(cell(page, "bowl.png")).toBeVisible();

  // Search by name start.
  await page.keyboard.press("/");
  await page.keyboard.type("no");
  await expect(cell(page, "notes.json")).toBeVisible();
  await expect(cell(page, "bowl.png")).toHaveCount(0);
  await page.getByRole("button", { name: "Clear the search" }).click();

  // Sort by size, largest first.
  await page.getByRole("columnheader", { name: "Size" }).getByRole("button").click();
  await expect(page.getByRole("columnheader", { name: "Size" })).toHaveAttribute("aria-sort", "descending");
  const names = await grid(page).getByRole("row").allInnerTexts();
  expect(names.findIndex((t) => t.includes("launch-video.mp4"))).toBeLessThan(names.findIndex((t) => t.includes("notes.json")));

  // Keyboard: arrows move, Space selects, Shift extends, Escape clears.
  await cell(page, "covers").focus();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Space");
  await page.keyboard.press("Shift+ArrowDown");
  await expect(page.getByRole("region", { name: "Selection" })).toContainText("2 selected");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("region", { name: "Selection" })).toHaveCount(0);

  // Grid view with thumbnails from the box's own resizing.
  await page.getByRole("radio", { name: "Grid" }).click();
  const thumb = grid(page).locator('img[src*="w=256"]').first();
  await expect(thumb).toBeVisible();
  await expect.poll(() => thumb.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth > 0), { timeout: 15_000 }).toBeTruthy();
  await axe(page, "grid view");
  await page.getByRole("radio", { name: "List" }).click();
  await page.keyboard.press("?");
  await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toContainText("Up a folder");
  await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toContainText("Upload files");
  await axe(page, "shortcuts");
  await page.keyboard.press("Escape");
});

test("file panel: previews, a private link with an expiry, the resized-link builder", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.goto(bucketUrl("?file=bowl.png"));
  const panel = page.getByRole("complementary", { name: "File" });
  await expect(panel.getByRole("img", { name: "Preview of bowl.png", exact: true })).toBeVisible();
  await expect(panel.getByText("image/png")).toBeVisible();
  await expect(panel.getByText(/\d×\d/).first()).toBeVisible(); // pixel size
  // Private bucket: a signed link that works for a day.
  await panel.getByRole("combobox", { name: "How long the link works" }).selectOption({ label: "1 day" });
  await panel.getByRole("button", { name: "Copy private link" }).click();
  await expect(panel.getByText(/Works until/)).toBeVisible();
  const link = await page.evaluate(() => navigator.clipboard.readText());
  expect(link).toMatch(new RegExp(`/${project}/${bucket}/bowl\\.png\\?exp=\\d+&sig=`));
  // Resize to 640 WebP: the preview and its size update, and the link carries it.
  await panel.getByRole("radio", { name: "640" }).click();
  await expect(panel.getByText(/smaller than the original|no smaller/)).toBeVisible();
  await expect(panel.locator("code").filter({ hasText: "w=640" })).toBeVisible();
  await panel.getByText("Use it with next/image").click();
  await expect(panel.getByText('export { default } from "tiffin-sdk/next/image-loader";')).toBeVisible();
  await axe(page, "image panel");
  // Text and PDF previews.
  await page.goto(bucketUrl("?file=notes.json"));
  await expect(panel.getByText('"shelf": "B4"')).toBeVisible();
  await page.goto(bucketUrl("?file=receipt.pdf"));
  await expect(panel.locator('iframe[title="PDF receipt.pdf"]')).toBeVisible();
  // The file op serves the bytes only as media, sandboxed.
  const res = await page.request.get(`/v1/projects/${project}/storage/buckets/${bucket}/file?key=notes.json`);
  expect(res.headers()["content-disposition"]).toMatch(/^attachment/);
  expect(res.headers()["x-content-type-options"]).toBe("nosniff");
});

test("rename, move and delete, each with Undo; a folder delete names what goes", async ({ page }) => {
  await page.goto(bucketUrl());
  // Rename with F2.
  await cell(page, "notes.json").focus();
  await page.keyboard.press("F2");
  const d = page.getByRole("dialog", { name: "Rename file" });
  await d.getByRole("textbox", { name: "New name" }).fill("shelf.json");
  await d.getByRole("button", { name: "Rename" }).click();
  await expect(toast(page, "Renamed notes.json to shelf.json.")).toBeVisible();
  await expect(cell(page, "shelf.json")).toBeVisible();
  await toast(page, "Renamed notes.json").getByRole("button", { name: "Undo" }).click();
  await expect(toast(page, "Put back as it was.")).toBeVisible();
  await expect(cell(page, "notes.json")).toBeVisible();

  // Move two files into a folder.
  await cell(page, "bowl.png").click({ modifiers: ["ControlOrMeta"] });
  await cell(page, "receipt.pdf").click({ modifiers: ["ControlOrMeta"] });
  await page.getByRole("region", { name: "Selection" }).getByRole("button", { name: "Move" }).click();
  const m = page.getByRole("dialog", { name: "Move 2 items" });
  await m.getByRole("textbox", { name: "To the folder" }).fill("archive");
  await m.getByRole("button", { name: "Move" }).click();
  await expect(toast(page, "Moved 2 files to archive.")).toBeVisible();
  await expect(cell(page, "archive")).toBeVisible();
  await toast(page, "Moved 2 files").getByRole("button", { name: "Undo" }).click();
  await expect(cell(page, "bowl.png")).toBeVisible();

  // Delete a file with the Delete key, then Undo.
  await cell(page, "notes.json").focus();
  await page.keyboard.press("Delete");
  await expect(toast(page, "Deleted notes.json.")).toBeVisible();
  await expect(cell(page, "notes.json")).toHaveCount(0);
  await toast(page, "Deleted notes.json").getByRole("button", { name: "Undo" }).click();
  await expect(cell(page, "notes.json")).toBeVisible();

  // A folder: the dialog says how many files and how much, first.
  await cell(page, "covers").focus();
  await page.keyboard.press("Delete");
  const c = page.getByRole("dialog", { name: "Delete the covers folder?" });
  await expect(c).toContainText("2 files");
  await axe(page, "folder delete");
  await c.getByRole("button", { name: "Delete 2 files" }).click();
  await expect(toast(page, /Deleted the covers folder: 2 files/)).toBeVisible();
  await toast(page, /Deleted the covers folder/).getByRole("button", { name: "Undo" }).click();
  await expect(cell(page, "covers")).toBeVisible();
});

test("10,000 files scroll smoothly", async ({ page, request, baseURL }) => {
  test.setTimeout(900_000);
  const keys = Array.from({ length: 10_000 }, (_, i) => `many/file-${String(i).padStart(5, "0")}.txt`);
  for (let i = 0; i < keys.length; i += 25) await Promise.all(keys.slice(i, i + 25).map((k) => put(request, baseURL!, k, k)));
  await page.goto(bucketUrl("?prefix=many%2F"));
  const t0 = Date.now();
  await expect(page.getByText("10,000 files")).toBeVisible({ timeout: 60_000 });
  const loaded = Date.now() - t0;
  const stats = await page.evaluate(async () => {
    const el = document.querySelector<HTMLElement>('[role="grid"]')!;
    const gaps: number[] = [];
    let last = performance.now();
    for (let i = 0; i < 90; i++) {
      el.scrollTop += 600;
      await new Promise((r) => requestAnimationFrame(r));
      const now = performance.now();
      gaps.push(now - last);
      last = now;
    }
    gaps.sort((a, b) => a - b);
    return { rows: el.querySelectorAll('[role="row"]').length, p95: gaps[Math.floor(gaps.length * 0.95)] };
  });
  console.log(`10k files: listed in ${loaded} ms, ${stats.rows} rows in the DOM, p95 frame ${stats.p95.toFixed(1)} ms`);
  expect(stats.rows).toBeLessThan(80);
  expect(stats.p95).toBeLessThan(50);
  // Select everything and delete it: a folder of 10,000 goes with one confirm.
  await page.goto(bucketUrl());
  await cell(page, "many").focus();
  await page.keyboard.press("Delete");
  await page.getByRole("dialog", { name: "Delete the many folder?" }).getByRole("button", { name: "Delete 10,000 files" }).click();
  await expect(toast(page, /Deleted the many folder: 10,000 files/)).toBeVisible({ timeout: 60_000 });
});

test("connect: the env apps get, values only on request", async ({ page }) => {
  await page.goto(filesUrl());
  await page.getByRole("button", { name: "Connect" }).click();
  const d = page.getByRole("dialog", { name: "Connect to files" });
  await expect(d.getByText("S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY")).toBeVisible();
  await expect(d.getByLabel(/S3_SECRET_ACCESS_KEY, hidden/)).toBeVisible();
  await axe(page, "connect");
  await d.getByRole("button", { name: "Show values" }).click();
  await expect(d.getByLabel(/hidden/)).toHaveCount(0);
  await d.getByRole("tab", { name: "tiffin-sdk" }).click();
  await expect(d.getByText('from "tiffin-sdk/storage"')).toBeVisible();
});

for (const theme of ["light", "dark"] as const) {
  for (const w of [1440, 390]) {
    test(`screens ${theme} ${w}`, async ({ page }) => {
      test.setTimeout(120_000);
      mkdirSync(out, { recursive: true });
      await page.setViewportSize(w === 390 ? { width: 390, height: 844 } : { width: 1440, height: 900 });
      await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
      const shot = async (name: string, full = true) => {
        await page.waitForTimeout(600);
        await page.screenshot({ path: `${out}/files-${name}-${theme}-${w}.png`, fullPage: full });
        const wide = await page.evaluate(() => document.documentElement.scrollWidth);
        expect(wide, `${name} scrolls sideways`).toBeLessThanOrEqual(w + 1);
      };
      await page.goto(filesUrl());
      await page.getByRole("link", { name: bucket }).waitFor();
      await shot("buckets");
      await axe(page, `buckets ${theme} ${w}`);
      await page.goto(bucketUrl());
      await cell(page, "bowl.png").waitFor();
      await shot("list");
      await page.getByRole("radio", { name: "Grid" }).click();
      await page.waitForTimeout(800);
      await shot("grid");
      await page.getByRole("radio", { name: "List" }).click();
      await page.goto(bucketUrl("?file=bowl.png"));
      await page.getByRole("complementary", { name: "File" }).getByRole("img").first().waitFor();
      await shot("panel");
      await axe(page, `panel ${theme} ${w}`);
      await page.goto(bucketUrl());
      await page.getByRole("button", { name: "Settings" }).click();
      await shot("settings", false);
      await page.keyboard.press("Escape");
    });
  }
}

test("delete the bucket: it asks first, naming the files", async ({ page, request, baseURL }) => {
  await page.goto(filesUrl());
  await page.getByRole("button", { name: `${bucket} settings` }).click();
  await page.getByRole("dialog", { name: `${bucket} settings` }).getByRole("button", { name: "Delete bucket" }).click();
  const tray = page.getByRole("dialog").filter({ hasText: /will be gone for good/ });
  await expect(tray).toContainText(/\d+ files?/);
  await tray.getByRole("textbox").fill(project);
  await axe(page, "delete bucket");
  await tray.getByRole("button", { name: /Delete|Remove|Confirm/ }).last().click();
  await expect(page.getByRole("link", { name: bucket })).toHaveCount(0, { timeout: 30_000 });
  // Leave nothing behind: purge it from the trash.
  await removeBucket(request, baseURL!);
});
