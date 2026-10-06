// Functional checks of every showcase feature, from outside the box: raw
// HTTP (headers, chunk timing) and a real Chromium (hydration, actions).
import { chromium, type BrowserContext, type Page } from "playwright";
import { BROWSER_AE, chunks, get, host, origin, type Target } from "./net.ts";

export type Check = { id: string; name: string; ok: boolean; detail: string };

class Fail extends Error {}
function must(cond: unknown, msg: string): asserts cond {
  if (!cond) throw new Fail(msg);
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const text = (b: Buffer) => b.toString("utf8");
const attr = (html: string, re: RegExp) => html.match(re)?.[1] ?? "";
const pick = (h: Record<string, string>, ...ks: string[]) => ks.map((k) => `${k}=${h[k] ?? "-"}`).join(" ");
/** When (ms) the text received so far first contains s. */
const arrived = (parts: [number, string][], s: string) => {
  let acc = "";
  for (const [ms, x] of parts) if ((acc += x).includes(s)) return ms;
  return NaN;
};

export type Actions = Record<string, string>; // exportedName → action id

export async function functional(t: Target, o: { instances: number; actions: Actions }): Promise<Check[]> {
  const out: Check[] = [];
  const check = async (id: string, name: string, fn: () => Promise<string>) => {
    const s = performance.now();
    try {
      const d = await fn();
      out.push({ id, name, ok: true, detail: `${d} (${Math.round(performance.now() - s)} ms)` });
    } catch (e) {
      out.push({ id, name, ok: false, detail: (e as Error).message.split("\n")[0]!.slice(0, 400) });
    }
    console.log(`  ${out.at(-1)!.ok ? "pass" : "FAIL"}  ${id} ${name}: ${out.at(-1)!.detail}`);
  };

  const browser = await chromium.launch({ args: ["--ignore-certificate-errors"] });
  const problems: string[] = [];
  const ctx = async (session: boolean): Promise<BrowserContext> => {
    const c = await browser.newContext({ ignoreHTTPSErrors: true, baseURL: t.base });
    if (session) await c.addCookies([{ name: "showcase_session", value: "demo", url: t.base }]);
    c.on("page", watch);
    return c;
  };
  const watch = (p: Page) => {
    p.on("console", (m) => m.type() === "error" && problems.push(`console: ${m.text().slice(0, 160)} (${p.url()})`));
    p.on("pageerror", (e) => problems.push(`pageerror: ${e.message.slice(0, 160)} (${p.url()})`));
    p.on("response", (r) => r.status() >= 400 && problems.push(`HTTP ${r.status()} ${r.url().replace(t.base, "")}`));
    // A navigation the bench itself interrupts shows up as ERR_ABORTED: not a problem.
    p.on("requestfailed", (r) => r.failure()?.errorText !== "net::ERR_ABORTED" && problems.push(`failed: ${r.url().replace(t.base, "")} ${r.failure()?.errorText}`));
  };
  const anon = await ctx(false);
  const user = await ctx(true);

  const home = await get(t, "/");
  const homeHtml = text(home.body);

  await check("F01", "home: static, proxy header, compressed", async () => {
    must(home.status === 200, `GET / → ${home.status}`);
    must(home.headers["x-showcase-proxy"] === "1", "x-showcase-proxy missing: proxy.ts did not run");
    const z = await get(t, "/", { headers: { "accept-encoding": BROWSER_AE } });
    must(["zstd", "br", "gzip"].includes(z.headers["content-encoding"] ?? ""), `not compressed (${z.headers["content-encoding"] ?? "none"})`);
    return `${pick(home.headers, "cache-control", "x-nextjs-cache", "x-nextjs-prerender")} encoding=${z.headers["content-encoding"]} ${home.body.length}→${z.body.length} B`;
  });

  await check("F02", "next/font: woff2 preloaded and served immutable", async () => {
    const font = attr(home.headers["link"] ?? "", /<([^>]+\.woff2[^>]*)>; rel=preload/) || attr(homeHtml, /<link rel="preload" href="([^"]+\.woff2[^"]*)" as="font"/);
    must(font, "no font preload (Link header or <link rel=preload>)");
    must(/class="[^"]*__className|class="[^"]*geist/i.test(homeHtml) || homeHtml.includes("className"), "font class missing on <html>");
    const r = await get(t, font);
    must(r.status === 200, `${font} → ${r.status}`);
    must((r.headers["cache-control"] ?? "").includes("immutable"), `font cache-control ${r.headers["cache-control"]}`);
    return `${font.split("/").pop()} ${r.headers["content-type"]} ${r.headers["cache-control"]}`;
  });

  const chunk = attr(homeHtml, /src="(\/_next\/static\/chunks\/[^"?]+\.js(?:\?[^"]*)?)"/);
  await check("F03", "client chunk: immutable, compressed by the edge", async () => {
    must(chunk, "no chunk in the page");
    const r = await get(t, chunk, { headers: { "accept-encoding": BROWSER_AE } });
    must(r.status === 200, `${chunk} → ${r.status}`);
    must((r.headers["cache-control"] ?? "").includes("immutable"), `cache-control ${r.headers["cache-control"]}`);
    must(r.headers["content-encoding"], "chunk not compressed");
    return `${pick(r.headers, "cache-control", "content-encoding")}`;
  });

  const page = await anon.newPage();
  await page.goto("/", { waitUntil: "load" });

  await check("F04", "next/image local: every srcset candidate loads", async () => {
    const img = await page.$eval("#hero", (e) => {
      const i = e as HTMLImageElement;
      return { srcset: i.srcset, sizes: i.sizes, current: i.currentSrc, w: i.naturalWidth };
    });
    must(img.w > 0, `hero did not load (${img.current})`);
    const cands = img.srcset.split(",").map((s) => s.trim().split(" ")[0]!).filter(Boolean);
    must(cands.length >= 4, `srcset has ${cands.length} candidates`);
    const types = new Set<string>();
    for (const c of cands) {
      const r = await get(t, c.replace(t.base, ""), { headers: { accept: "image/avif,image/webp,*/*" } });
      must(r.status === 200, `${c} → ${r.status} ${text(r.body).slice(0, 120)}`);
      types.add(r.headers["content-type"] ?? "?");
    }
    must([...types].every((x) => /image\/(webp|avif)/.test(x)), `types ${[...types]}`);
    return `${cands.length} widths, ${[...types].join("/")}, browser picked ${new URL(img.current).searchParams.get("w")}w`;
  });

  await check("F05", "next/image remote: the bucket's featured image loads", async () => {
    const img = await page.$eval("#featured", (e) => ({ src: (e as HTMLImageElement).currentSrc, w: (e as HTMLImageElement).naturalWidth }));
    const remote = new URL(img.src).searchParams.get("url") ?? "";
    if (img.w > 0) return `${remote} → ${img.w}px`;
    const opt = await get(t, img.src.replace(t.base, ""), { headers: { accept: "image/webp" } });
    let raw = "not fetched";
    try {
      const r = await get(t, remote);
      raw = `${r.status} ${r.headers["content-type"]}`;
    } catch (e) {
      raw = (e as Error).message;
    }
    throw new Fail(`optimizer → ${opt.status} "${text(opt.body).slice(0, 100)}"; the file itself (${remote}) → ${raw}`);
  });

  const og = attr(homeHtml, /<meta property="og:image" content="([^"]+)"/);
  await check("F06", "Open Graph: og:image is an absolute URL on this site", async () => {
    must(og, "no og:image");
    must(og.startsWith(origin(t)), `og:image is ${og} (no metadataBase: Next.js falls back to localhost)`);
    return og;
  });

  await check("F07", "Open Graph image decodes (1200×630 PNG)", async () => {
    must(og, "no og:image");
    const path = new URL(og).pathname + new URL(og).search;
    const r = await get(t, path);
    must(r.status === 200 && r.headers["content-type"] === "image/png", `${path} → ${r.status} ${r.headers["content-type"]}`);
    const [w, h] = [r.body.readUInt32BE(16), r.body.readUInt32BE(20)];
    const decoded = await page.evaluate(async (u) => {
      const b = await createImageBitmap(await (await fetch(u)).blob());
      return [b.width, b.height];
    }, path);
    must(w === 1200 && h === 630 && decoded[0] === 1200, `IHDR ${w}×${h}, decoded ${decoded}`);
    return `${r.body.length} B, ${pick(r.headers, "cache-control")}`;
  });

  await check("F08", "client navigation (RSC) from / to /products", async () => {
    await page.evaluate(() => ((window as unknown as { __mark: number }).__mark = 1));
    await page.click('nav a[href="/products"]');
    await page.waitForSelector("[data-product]", { timeout: 10_000 });
    const same = await page.evaluate(() => (window as unknown as { __mark?: number }).__mark === 1);
    must(same, "full page load instead of a client navigation");
    return "soft navigation";
  });

  const blog = async () => {
    const r = await get(t, "/blog/hello-box");
    const h = text(r.body);
    return { status: r.status, rev: Number(attr(h, /id="post-rev">(\d+)</)), at: attr(h, /id="rendered-at"[^>]*>([^<]+)</), headers: r.headers };
  };
  const b0 = await blog();
  await check("F09", "blog: prerendered at build, served from cache (ISR)", async () => {
    must(b0.status === 200 && b0.rev > 0, `GET /blog/hello-box → ${b0.status}`);
    // The first request after a deploy gets the build's prerender: fresh (HIT),
    // or past its revalidate time (STALE, and Next.js renders it anew once).
    const first = b0.headers["x-nextjs-cache"] ?? "-";
    must(first === "HIT" || first === "STALE", `the first request was rendered (x-nextjs-cache=${first}), not the build's prerender`);
    if (first === "STALE") await sleep(1000);
    const [a, b] = [await blog(), await blog()];
    must(a.at === b.at, `rendered again on a plain GET (${a.at} → ${b.at})`);
    const age = ((Date.now() - Date.parse(b0.at)) / 1000).toFixed(0);
    return `${pick(b0.headers, "cache-control", "x-nextjs-cache", "x-nextjs-prerender")}, first copy rendered ${age} s ago`;
  });

  await check("F10", "proxy: /dashboard without a session redirects to /login on this site", async () => {
    const r = await get(t, "/dashboard");
    must([302, 303, 307, 308].includes(r.status), `GET /dashboard → ${r.status}`);
    const loc = r.headers["location"] ?? "";
    const abs = new URL(loc, t.base);
    must(abs.origin === origin(t) && abs.pathname === "/login", `Location: ${loc}`);
    return `${r.status} → ${loc}`;
  });

  await check("F11", "login: form Server Action sets the session, redirects to /dashboard", async () => {
    const c = await ctx(false);
    const p = await c.newPage();
    await p.goto("/login");
    await p.click("#login");
    await p.waitForURL("**/dashboard", { timeout: 15_000 });
    await p.waitForSelector("#dash-shell");
    const ck = (await c.cookies()).find((x) => x.name === "showcase_session");
    must(ck, "no session cookie");
    await c.close();
    return `cookie secure=${ck.secure} httpOnly=${ck.httpOnly}`;
  });

  await check("F12", "streaming: dashboard shell arrives before the slow section (edge, browser encoding)", async () => {
    const r = await chunks(t, "/dashboard?delay=1000", { cookie: t.cookie, "accept-encoding": BROWSER_AE });
    must(r.status === 200, `status ${r.status}`);
    const [shell, orders] = [arrived(r.parts, 'id="dash-shell"'), arrived(r.parts, 'id="orders"')];
    const plain = await chunks(t, "/dashboard?delay=1000", { cookie: t.cookie, "accept-encoding": "identity" });
    const pShell = arrived(plain.parts, 'id="dash-shell"');
    const detail = `${r.headers["content-encoding"] ?? "identity"}: shell ${shell.toFixed(0)} ms, slow section ${orders.toFixed(0)} ms, ${r.parts.length} chunks; uncompressed: shell ${pShell.toFixed(0)} ms`;
    must(orders - shell > 600, `shell held back until the slow part: ${detail}`);
    return detail;
  });

  await check("F13", "streaming in Chromium: shell painted before the slow section", async () => {
    const p = await user.newPage();
    const s = performance.now();
    await p.goto("/dashboard?delay=1000", { waitUntil: "commit" });
    await p.waitForSelector("#dash-shell", { state: "attached" });
    const shell = performance.now() - s;
    await p.waitForSelector("#orders", { state: "attached", timeout: 15_000 });
    const orders = performance.now() - s;
    await p.close();
    must(orders - shell > 600, `shell at ${shell.toFixed(0)} ms, slow section at ${orders.toFixed(0)} ms`);
    return `shell ${shell.toFixed(0)} ms, slow section ${orders.toFixed(0)} ms`;
  });

  const dash = await user.newPage();
  await dash.goto("/dashboard?delay=0");
  await dash.waitForSelector("#note-form");

  await check("F14", "Server Action: useActionState + useOptimistic note, then saved", async () => {
    const note = `bench ${Date.now()}`;
    await dash.fill('#note-form input[name="text"]', note);
    const s = performance.now();
    await dash.click("#note-form button");
    await dash.waitForSelector(`#notes li[data-pending="true"]:has-text("${note}")`, { timeout: 2000 });
    const optimistic = performance.now() - s;
    await dash.waitForSelector(`#notes li:not([data-pending]):has-text("${note}")`, { timeout: 15_000 });
    const saved = performance.now() - s;
    must(optimistic < 400, `optimistic row after ${optimistic.toFixed(0)} ms`);
    await dash.reload();
    await dash.waitForSelector(`#notes li:has-text("${note}")`, { timeout: 10_000 });
    return `optimistic ${optimistic.toFixed(0)} ms, saved ${saved.toFixed(0)} ms (400 ms of it is the action's own wait), kept after reload`;
  });

  await check("F15", "Server Action: useOptimistic like, round trip", async () => {
    const btn = dash.locator("[data-like]").first();
    const id = await btn.getAttribute("data-like");
    const before = Number(await btn.locator("[data-likes]").textContent());
    const s = performance.now();
    const done = dash.waitForResponse((r) => r.request().method() === "POST" && !!r.request().headers()["next-action"]);
    await btn.click();
    await dash.waitForFunction(([i, n]) => Number(document.querySelector(`[data-like="${i}"] [data-likes]`)?.textContent) === n, [id, before + 1] as const, { timeout: 2000 });
    const optimistic = performance.now() - s;
    const res = await done;
    const rt = performance.now() - s;
    must(res.status() === 200, `action → ${res.status()}`);
    return `optimistic ${optimistic.toFixed(0)} ms, server round trip ${rt.toFixed(0)} ms`;
  });

  await check("F16", "Server Action over fetch (Next-Action header, as in the load test)", async () => {
    const id = o.actions.likeProduct;
    must(id, "no action id for likeProduct");
    const r = await get(t, "/dashboard", {
      method: "POST",
      body: "[1]",
      headers: { "next-action": id, "content-type": "text/plain;charset=UTF-8", accept: "text/x-component", origin: origin(t), cookie: t.cookie },
    });
    must(r.status === 200 && (r.headers["content-type"] ?? "").includes("text/x-component"), `→ ${r.status} ${r.headers["content-type"]} ${text(r.body).slice(0, 120)}`);
    must(/\d/.test(text(r.body)), "no result in the response");
    return `${r.status} ${r.body.length} B in ${r.ms.toFixed(0)} ms`;
  });

  const spread = async (path: string, re: RegExp, n = 4 * Math.max(o.instances, 1)) => {
    const seen = new Set<string>();
    for (let i = 0; i < n; i++) seen.add(attr(text((await get(t, path)).body), re));
    return seen;
  };

  await check("F17", "ISR on demand: revalidatePath from the dashboard reaches every instance", async () => {
    const before = await blog();
    await dash.goto("/dashboard?delay=0");
    const s = performance.now();
    const done = dash.waitForResponse((r) => r.request().method() === "POST");
    await dash.click("#edit-hello-box");
    await done;
    let ok = false;
    let seen = new Set<string>();
    for (let i = 0; i < 20 && !ok; i++) {
      const revs = new Set<number>();
      seen = new Set();
      for (let k = 0; k < 4 * Math.max(o.instances, 1); k++) {
        const b = await blog();
        revs.add(b.rev);
        seen.add(b.at);
      }
      ok = revs.size === 1 && [...revs][0]! > before.rev;
      if (!ok) await sleep(500);
    }
    must(ok, `still serving rev ${before.rev} after 10 s`);
    return `new revision everywhere after ${(performance.now() - s).toFixed(0)} ms; ${seen.size} distinct render time(s) across instances`;
  });

  await check("F18", "use cache + updateTag: restock refreshes /products everywhere, one shared entry", async () => {
    const re = /id="products-at"[^>]*>([^<]+)</;
    const before = await spread("/products", re);
    await dash.goto("/dashboard?delay=0");
    const done = dash.waitForResponse((r) => r.request().method() === "POST");
    await dash.click("#restock");
    await done;
    let after = new Set<string>();
    for (let i = 0; i < 20; i++) {
      after = await spread("/products", re);
      if (![...after].some((x) => before.has(x))) break;
      await sleep(500);
    }
    must(![...after].some((x) => before.has(x)), `old cache entry still served: before ${[...before]}, after ${[...after]}`);
    must(before.size === 1 && after.size === 1, `instances keep separate caches: ${before.size} entries before, ${after.size} after`);
    return `1 shared entry before and after`;
  });

  await check("F19", "PPR: /products static shell first, cart count streamed from the cookie", async () => {
    const r = await chunks(t, "/products", { cookie: "cart=3", "accept-encoding": BROWSER_AE });
    const all = r.parts.map(([, x]) => x).join("");
    must(all.includes("Cart: <!-- -->3") || all.includes("Cart: 3"), "cart count from the cookie missing");
    const grid = arrived(r.parts, "data-product");
    const p = await anon.newPage();
    await p.goto("/products");
    const before = await p.textContent("#cart-count");
    await p.locator("[data-product] button").first().click();
    await p.waitForFunction((b) => document.querySelector("#cart-count")?.textContent !== b, before, { timeout: 10_000 });
    const after = await p.textContent("#cart-count");
    await p.close();
    return `grid at ${grid.toFixed(0)} ms of ${r.parts.at(-1)?.[0].toFixed(0)} ms; add to cart: "${before}" → "${after}"`;
  });

  await check("F20", "route handler /api/items: JSON from Postgres on every instance", async () => {
    const seen = new Set<string>();
    let n = 0;
    for (let i = 0; i < 4 * Math.max(o.instances, 1); i++) {
      const r = await get(t, "/api/items");
      must(r.status === 200, `→ ${r.status}`);
      const j = JSON.parse(text(r.body)) as { items: unknown[]; instance: string };
      n = j.items.length;
      seen.add(j.instance);
    }
    must(n === 12, `${n} items`);
    const z = await get(t, "/api/items", { headers: { "accept-encoding": BROWSER_AE } });
    must(seen.size === o.instances, `answered by instances ${[...seen]}, expected ${o.instances}`);
    return `${n} items, instances ${[...seen].sort()}, encoding=${z.headers["content-encoding"] ?? "none"}`;
  });

  await check("F21", "SSE /api/stream: events arrive as sent (browser encoding)", async () => {
    const r = await chunks(t, "/api/stream", { accept: "text/event-stream", "accept-encoding": BROWSER_AE });
    const times: number[] = [];
    for (const [ms, x] of r.parts) for (const _ of x.matchAll(/^event: tick$/gm)) times.push(ms);
    const plain = await chunks(t, "/api/stream", { accept: "text/event-stream", "accept-encoding": "identity" });
    const ptimes = plain.parts.filter(([, x]) => x.includes("event: tick")).map(([ms]) => ms);
    const d = `${r.headers["content-encoding"] ?? "identity"}: ${times.length} events, first ${times[0]?.toFixed(0)} ms, last ${times.at(-1)?.toFixed(0)} ms; identity: first ${ptimes[0]?.toFixed(0)} ms`;
    must(times.length === 10, `${times.length} events: ${d}`);
    must(times[0]! < 500 && times.at(-1)! - times[0]! > 1400, `buffered: ${d}`);
    return d;
  });

  await check("F22", "next/image: a new image is optimized, then served from the image cache", async () => {
    const u = `/_next/image?url=%2Fapi%2Fswatch%2F${Date.now()}&w=640&q=75`;
    const a = await get(t, u, { headers: { accept: "image/avif,image/webp,*/*" } });
    must(a.status === 200 && /image\/(webp|avif)/.test(a.headers["content-type"] ?? ""), `cold → ${a.status} ${a.headers["content-type"]} ${text(a.body).slice(0, 100)}`);
    const b = await get(t, u, { headers: { accept: "image/avif,image/webp,*/*" } });
    must(b.headers["x-nextjs-cache"] === "HIT", `second request x-nextjs-cache=${b.headers["x-nextjs-cache"]}`);
    return `cold ${a.ms.toFixed(0)} ms (${a.headers["content-type"]}, ${a.body.length} B), warm ${b.ms.toFixed(0)} ms HIT`;
  });

  await check("F23", "no console errors, failed requests or 4xx/5xx in the browser", async () => {
    const uniq = [...new Set(problems)];
    must(uniq.length === 0, uniq.slice(0, 4).join(" | ") + (uniq.length > 4 ? ` (+${uniq.length - 4})` : ""));
    return "clean";
  });

  await browser.close();
  return out;
}

export { host };
