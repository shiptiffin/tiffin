import AxeBuilder from "@axe-core/playwright";
import { mkdirSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { signIn } from "./helpers";

// The Database console against a dev box seeded by e2e/seed-box-db.sh (a real
// Postgres: the box's database module runs only on a box):
//   DB_E2E=1 E2E_BASE_URL=http://localhost:5471 E2E_OWNER_TOKEN=… bunx playwright test db-console
// It edits rows and makes tables, and puts back what it changed.
test.skip(!process.env.DB_E2E || !process.env.E2E_OWNER_TOKEN, "set DB_E2E=1 and E2E_OWNER_TOKEN for a box seeded by seed-box-db.sh");


const P = "/projects/bookshop/data";
const out = process.env.SHOTS_DIR ?? "screenshots/db";
const toasts = (page: Page) => page.getByRole("region", { name: "Notifications" });
const grid = (page: Page, table: string) => page.getByRole("grid", { name: `Rows of ${table}` });
const row = (page: Page, table: string, text: string) => grid(page, table).getByRole("row").filter({ hasText: text });
const cell = (page: Page, table: string, text: string, col: string) => row(page, table, text).locator(`[data-col="${col}"]`);

async function open(page: Page, path: string) {
  await page.goto(path);
  await page.getByRole("heading", { name: "Database", exact: true }).waitFor();
}

test.beforeEach(async ({ page, baseURL }) => {
  await signIn(page, baseURL!);
});

test("edit a cell, then Undo puts the old value back", async ({ page }) => {
  await open(page, `${P}/tables/books?f=title.eq.Bread%20%26%20Weather`);
  const price = cell(page, "books", "Bread & Weather", "price");
  await expect(price).toHaveText("24.50");
  await price.click();
  await page.keyboard.press("Enter");
  await page.getByLabel("New price").fill("26");
  await page.keyboard.press("Enter");
  await expect(toasts(page)).toContainText("Saved price on “Bread & Weather”");
  await expect(price).toHaveText("26.00");
  await expect(price).toBeFocused(); // the cursor stays where you were
  await toasts(page).getByRole("button", { name: "Undo" }).first().click();
  await expect(toasts(page)).toContainText("Undone");
  await expect(price).toHaveText("24.50");
  await page.reload();
  await expect(cell(page, "books", "Bread & Weather", "price")).toHaveText("24.50");

  // Escape cancels an edit; a bad value is refused in plain words and nothing changes.
  await cell(page, "books", "Bread & Weather", "price").click();
  await page.keyboard.press("Enter");
  await page.getByLabel("New price").fill("99");
  await page.keyboard.press("Escape");
  await expect(cell(page, "books", "Bread & Weather", "price")).toHaveText("24.50");
  await page.keyboard.press("Enter");
  await page.getByLabel("New price").fill("-3");
  await page.keyboard.press("Enter");
  await expect(toasts(page)).toContainText("The value breaks the rule books_price_check");
  await expect(cell(page, "books", "Bread & Weather", "price")).toHaveText("24.50");

  // Pasting more than one cell shows a summary first, then saves as one edit.
  await cell(page, "books", "Bread & Weather", "price").click();
  await grid(page, "books").evaluate((el) => {
    const dt = new DataTransfer();
    dt.setData("text/plain", "19.99\tfalse");
    el.dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true }));
  });
  const summary = page.getByRole("dialog", { name: "Paste 2 values into 1 row?" });
  await expect(summary).toContainText("price, in_stock");
  await summary.getByRole("button", { name: "Paste 2 values" }).click();
  await expect(toasts(page)).toContainText("Pasted 2 values into “Bread & Weather”");
  await expect(cell(page, "books", "Bread & Weather", "in_stock")).toHaveText("false");
  await toasts(page).getByRole("button", { name: "Undo" }).last().click();
  await expect(cell(page, "books", "Bread & Weather", "price")).toHaveText("24.50");
  await expect(cell(page, "books", "Bread & Weather", "in_stock")).toHaveText("true");
});

test("add rows, delete two (it asks with the count), Undo brings them back", async ({ page }) => {
  await open(page, `${P}/tables/books`);
  const tag = `E2E ${Date.now().toString(36)}`;
  for (const n of [1, 2]) {
    await page.getByRole("button", { name: "Add row" }).click();
    const sheet = page.getByRole("dialog", { name: "New row in books" });
    await sheet.getByLabel("title").fill(`${tag} ${n}`);
    await sheet.getByLabel("price").fill("7.25");
    await sheet.getByRole("button", { name: "Add row" }).click();
    await expect(sheet).toBeHidden();
    await expect(toasts(page)).toContainText(`Added “${tag} ${n}” to books`);
  }
  await expect(row(page, "books", `${tag} 1`)).toBeVisible();
  for (const n of [1, 2]) await row(page, "books", `${tag} ${n}`).getByRole("gridcell").first().click(); // the checkbox cell
  await page.getByRole("button", { name: "Delete 2 rows" }).click();
  const confirm = page.getByRole("dialog", { name: "Delete 2 rows from books?" });
  await confirm.getByRole("button", { name: "Delete 2 rows" }).click();
  await expect(toasts(page)).toContainText("Deleted 2 rows from books");
  await expect(row(page, "books", tag)).toHaveCount(0);
  await toasts(page).getByRole("button", { name: "Undo" }).last().click();
  await expect(toasts(page)).toContainText("Undone");
  await open(page, `${P}/tables/books?f=title.startsWith.${encodeURIComponent(tag)}`);
  await expect(row(page, "books", tag)).toHaveCount(2);
  // Clean up, by keyboard this time: Shift+Space selects a row, Backspace deletes.
  await cell(page, "books", `${tag} 1`, "title").click();
  await page.keyboard.press("Shift+Space");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Shift+Space");
  await page.keyboard.press("Backspace");
  await page.getByRole("dialog", { name: "Delete 2 rows from books?" }).getByRole("button", { name: "Delete 2 rows" }).click();
  await expect(row(page, "books", tag)).toHaveCount(0);
});

test("filters live in the URL", async ({ page }) => {
  await open(page, `${P}/tables/books`);
  await page.getByRole("button", { name: "Filter", exact: true }).click();
  const form = page.getByRole("form", { name: "Filter" });
  await form.getByLabel("Column").selectOption("in_stock");
  await form.getByLabel("Matches").selectOption("eq");
  await form.getByLabel("Value").selectOption("false");
  await form.getByRole("button", { name: "Add filter" }).click();
  await expect(page).toHaveURL(/f=in_stock\.eq\.false/);
  await expect(page.getByText(/rows match/)).toBeVisible();
  const values = await grid(page, "books").locator('[data-col="in_stock"]').allTextContents();
  expect(values.length).toBeGreaterThan(0);
  expect(new Set(values)).toEqual(new Set(["false"]));
  // Sorting by a header goes in the URL too.
  await grid(page, "books").getByRole("columnheader", { name: /price/ }).click();
  await expect(page).toHaveURL(/s=price/);
  await page.reload();
  await expect(page.getByRole("button", { name: /Filter: in_stock is false/ })).toBeVisible();
  // Export what's shown as CSV.
  await page.getByRole("button", { name: "More for books" }).click();
  const [file] = await Promise.all([page.waitForEvent("download"), page.getByRole("menuitem", { name: "Export these rows as CSV" }).click()]);
  expect(file.suggestedFilename()).toBe("books.csv");
  const csv = await (await file.createReadStream()).toArray().then((b) => Buffer.concat(b).toString());
  const lines = csv.trim().split("\r\n");
  expect(lines[0]).toBe("id,title,author_id,price,in_stock,format,published,tags,meta");
  expect(lines.length).toBe(values.length + 1);
  expect(lines.slice(1).every((l) => l.includes(",false,"))).toBe(true);
});

test("a link shows the other row's name; the picker changes it; it opens the linked row", async ({ page }) => {
  await open(page, `${P}/tables/books?f=title.eq.Small%20Engines`);
  const author = cell(page, "books", "Small Engines", "author_id");
  await expect(author).toContainText("Ruth Marsh");
  await author.click({ position: { x: 12, y: 12 } });
  await page.keyboard.press("Enter");
  const picker = page.getByRole("dialog", { name: /Link author_id to a row of authors/ });
  await picker.getByRole("combobox").fill("Mei");
  await picker.getByRole("option", { name: /Mei Tanaka/ }).click();
  await expect(toasts(page)).toContainText("Linked “Small Engines” to “Mei Tanaka”");
  await expect(author).toContainText("Mei Tanaka");
  await toasts(page).getByRole("button", { name: "Undo" }).first().click();
  await expect(author).toContainText("Ruth Marsh");
  // ⌥↵ opens the linked row: authors, filtered to it.
  await author.click({ position: { x: 12, y: 12 } });
  await page.keyboard.press("Alt+Enter");
  await expect(page).toHaveURL(/tables\/authors\?f=id\.eq\.3/);
  await expect(grid(page, "authors").getByRole("row")).toHaveCount(2); // the header and Ruth
});

test("make a table from the form, see its SQL, then delete it", async ({ page }) => {
  const name = `e2e_${Date.now().toString(36)}`;
  await open(page, `${P}/tables/books`);
  await page.getByRole("button", { name: "New table" }).click();
  const sheet = page.getByRole("dialog", { name: "New table" });
  await sheet.getByLabel("Name", { exact: true }).fill(name);
  await sheet.getByRole("button", { name: "Add a column" }).click();
  await sheet.getByLabel("Column name").last().fill("book_id");
  await sheet.getByLabel("Type").last().selectOption("link");
  await sheet.getByLabel("Linked table").selectOption("public.books");
  await sheet.getByText("Details: the SQL this runs").click();
  await expect(sheet.locator("pre")).toContainText(`CREATE TABLE "public"."${name}"`);
  await expect(sheet.locator("pre")).toContainText(`REFERENCES "public"."books" ("id")`);
  await sheet.getByRole("button", { name: `Create ${name}` }).click();
  await expect(page).toHaveURL(new RegExp(`/tables/${name}`));
  await expect(toasts(page)).toContainText(`Made the table ${name}`);
  await expect(page.getByText("No rows yet.")).toBeVisible();
  await page.getByRole("button", { name: `More for ${name}` }).click();
  await page.getByRole("menuitem", { name: "Delete table…" }).click();
  const hazard = page.getByRole("dialog");
  await hazard.getByRole("textbox").fill(name);
  await hazard.getByRole("button", { name: `Delete ${name}` }).click();
  await expect(toasts(page)).toContainText(`Deleted ${name}`);
  await expect(page.getByRole("navigation", { name: "Tables" }).getByText(name)).toHaveCount(0);
});

test("the schema diagram draws 34 linked tables and opens one", async ({ page }) => {
  await page.goto("/projects/warehouse/data/schema");
  await expect(page.getByText(/34 tables, \d+ links/)).toBeVisible();
  const node = page.getByRole("link", { name: /^stock_moves, / });
  await expect(node).toBeVisible();
  await node.focus();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/warehouse\/data\/tables\/stock_moves/);
});

test("SQL: completion from the schema, ⌘↵ runs, results in the grid, saved queries", async ({ page }) => {
  await open(page, `${P}/sql`);
  const editor = page.getByRole("textbox", { name: "SQL" });
  await editor.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.type("select title, price from boo");
  await page.keyboard.press("Control+Space");
  await expect(page.getByRole("option", { name: "books" })).toBeVisible();
  await page.waitForTimeout(150); // CodeMirror ignores keys for a moment after the list opens
  await page.keyboard.press("Enter");
  await page.keyboard.type(" order by price desc limit 5");
  await expect(editor).toContainText("from books order by price desc limit 5");
  await page.keyboard.press("ControlOrMeta+Enter");
  await expect(page.getByText(/5 rows in .* read only/)).toBeVisible();
  await expect(page.getByRole("grid", { name: "Query results" }).getByRole("row")).toHaveCount(6);
  // A DELETE without WHERE is called out before it can run.
  await editor.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.type("delete from reviews");
  await page.getByRole("switch", { name: "Allow changes" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "DELETE with no WHERE removes every row of reviews" })).toBeVisible();
  await page.getByRole("switch", { name: "Allow changes" }).click();
  // Save, then delete the saved query.
  await editor.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.type("select count(*) from orders");
  const qname = `E2E ${Date.now().toString(36)}`;
  await page.getByRole("button", { name: "Save…" }).click();
  await page.getByRole("dialog").getByLabel("Name").fill(qname);
  await page.getByRole("dialog").getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("button", { name: qname, exact: true })).toBeVisible();
  await page.getByRole("button", { name: `Delete the saved query ${qname}` }).click();
  await expect(page.getByRole("button", { name: qname, exact: true })).toHaveCount(0);
});

test("the grid stays smooth with 50,000 rows", async ({ page }) => {
  await open(page, `${P}/tables/events`);
  const g = grid(page, "events");
  await expect(g.getByRole("row").nth(1)).toBeVisible();
  const r = await page.evaluate(async () => {
    const el = document.querySelector<HTMLElement>('[role="grid"]')!;
    const frames: number[] = [];
    let last = performance.now();
    let maxRows = 0;
    const t0 = performance.now();
    // Scroll steadily for 3 seconds, as a fast flick would, sampling frame times.
    await new Promise<void>((done) => {
      const step = (now: number) => {
        frames.push(now - last);
        last = now;
        el.scrollTop += 900;
        maxRows = Math.max(maxRows, el.querySelectorAll('[role="row"]').length);
        if (now - t0 < 3000) requestAnimationFrame(step);
        else done();
      };
      requestAnimationFrame(step);
    });
    frames.sort((a, b) => a - b);
    const p = (q: number) => frames[Math.floor(frames.length * q)];
    return { frames: frames.length, p50: p(0.5), p95: p(0.95), max: frames[frames.length - 1], maxRows, scrolled: el.scrollTop, loaded: Number(el.getAttribute("aria-rowcount")) - 1 };
  });
  console.log(`50k grid: ${r.frames} frames in 3 s, p50 ${r.p50.toFixed(1)} ms, p95 ${r.p95.toFixed(1)} ms, max ${r.max.toFixed(1)} ms; at most ${r.maxRows} rows in the page; scrolled ${r.scrolled}px of ${r.loaded} rows`);
  expect(r.maxRows).toBeLessThan(120); // virtualized: only what's on screen is in the page
  expect(r.p95).toBeLessThan(50);
});

test("no serious or critical accessibility problems", async ({ page }) => {
  for (const path of [`${P}/tables/books`, `${P}/sql`, `${P}/schema`, `${P}/branches`, `${P}/restore`]) {
    await open(page, path);
    await page.waitForTimeout(600);
    const res = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"]).analyze();
    const bad = res.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
    expect(bad.map((v) => `${path}: ${v.id} (${v.nodes.length}) ${v.nodes[0]?.target}`)).toEqual([]);
  }
  // The row panel and the new-table form too.
  await open(page, `${P}/tables/books`);
  await page.getByRole("button", { name: "Add row" }).click();
  await page.getByRole("dialog").waitFor();
  await page.waitForTimeout(500); // let the panel finish sliding in (axe reads colours mid-fade otherwise)
  const serious = (r: Awaited<ReturnType<AxeBuilder["analyze"]>>) =>
    r.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => `${v.id}: ${v.nodes.map((n) => n.target).join(" ")}`);
  let res = await new AxeBuilder({ page }).include('[role="dialog"]').analyze();
  expect(serious(res)).toEqual([]);
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "New table" }).click();
  await page.getByRole("dialog").waitFor();
  await page.waitForTimeout(500);
  res = await new AxeBuilder({ page }).include('[role="dialog"]').analyze();
  expect(serious(res)).toEqual([]);
});

// Screenshots at 1440 and 390, light and dark, for review.
for (const theme of ["light", "dark"] as const) {
  for (const width of [1440, 390]) {
    test(`screens ${width} ${theme}`, async ({ page }) => {
      test.skip(!process.env.SCREENS, "set SCREENS=1 for screenshots");
      mkdirSync(out, { recursive: true });
      await page.setViewportSize({ width, height: width > 500 ? 900 : 844 });
      await page.emulateMedia({ colorScheme: theme });
      const shot = async (name: string) => {
        await page.waitForTimeout(500);
        await page.screenshot({ path: `${out}/${name}-${width}-${theme}.png` });
        const wide = await page.evaluate(() => document.documentElement.scrollWidth);
        expect(wide, `${name} scrolls sideways at ${width}`).toBeLessThanOrEqual(width + 1);
      };
      await open(page, `${P}/tables/books?f=price.lt.30&s=title`);
      await shot("db-table");
      await grid(page, "books").getByRole("button", { name: "Open row 1", exact: true }).click();
      await page.getByRole("dialog").waitFor();
      await shot("db-row");
      await page.keyboard.press("Escape");
      await open(page, `${P}/sql`);
      await page.getByRole("textbox", { name: "SQL" }).click();
      await page.keyboard.press("ControlOrMeta+A");
      await page.keyboard.type("select b.title, sum(oi.qty) as sold\nfrom order_items oi\njoin books b on b.id = oi.book_id\ngroup by 1 order by 2 desc limit 8;");
      await page.keyboard.press("ControlOrMeta+Enter");
      await page.getByText(/rows in/).waitFor();
      await shot("db-sql");
      await open(page, `${P}/schema`);
      await shot("db-schema");
      await open(page, `${P}/tables/books`);
      await page.getByRole("button", { name: "New table" }).click();
      await page.getByRole("dialog").waitFor();
      await shot("db-new-table");
      await page.keyboard.press("Escape");
      await open(page, `${P}/branches`);
      await shot("db-copies");
      await open(page, `${P}/tables/books?branch=pr-12`);
      await shot("db-copy");
    });
  }
}
