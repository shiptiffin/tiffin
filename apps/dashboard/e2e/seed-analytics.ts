// Seeds eleven weeks of believable traffic for the hello project straight into
// a throwaway box's analytics store (before `tiffin serve --box` opens it):
// weekday and daytime rhythm, slow growth, a Hacker News day, sources, UTM
// campaigns, countries, devices, custom events, Web Vitals and the last
// half hour for "right now". Deterministic: the same seed every run.
//
//   bun e2e/seed-analytics.ts <data root>     # the box's data root, e.g. $DIR (TIFFIN_HOME=$DIR/box)
import { Database } from "bun:sqlite";
import { mkdirSync } from "node:fs";
import { join } from "node:path";

const root = process.argv[2];
if (!root) throw new Error("usage: bun e2e/seed-analytics.ts <data root>");
mkdirSync(join(root, "analytics"), { recursive: true });
const db = new Database(join(root, "analytics", "analytics.db"));
db.exec("PRAGMA journal_mode = wal");
// The same tables the box creates (internal/mod/analytics/store.go).
db.exec(`CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, day TEXT NOT NULL,
  project TEXT NOT NULL, app TEXT NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL,
  host TEXT NOT NULL, path TEXT NOT NULL, ref_source TEXT NOT NULL, ref_host TEXT NOT NULL,
  utm_source TEXT NOT NULL, utm_medium TEXT NOT NULL, utm_campaign TEXT NOT NULL,
  country TEXT NOT NULL, browser TEXT NOT NULL, os TEXT NOT NULL, device TEXT NOT NULL,
  visitor INTEGER NOT NULL, session INTEGER NOT NULL, props TEXT NOT NULL, src TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS events_pa_ts ON events(project, app, ts);
CREATE INDEX IF NOT EXISTS events_ts ON events(ts);
CREATE TABLE IF NOT EXISTS daily (project TEXT NOT NULL, app TEXT NOT NULL, day TEXT NOT NULL,
  visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL, sessions INTEGER NOT NULL,
  bounces INTEGER NOT NULL, duration_ms INTEGER NOT NULL, events INTEGER NOT NULL,
  updated_at INTEGER NOT NULL, PRIMARY KEY(project, app, day));
CREATE TABLE IF NOT EXISTS vitals (project TEXT NOT NULL, app TEXT NOT NULL, day TEXT NOT NULL, path TEXT NOT NULL, metric TEXT NOT NULL,
  bucket INTEGER NOT NULL, n INTEGER NOT NULL, PRIMARY KEY(project, app, day, path, metric, bucket)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS salts (day TEXT PRIMARY KEY, salt BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS keys (project TEXT NOT NULL, app TEXT NOT NULL, key TEXT NOT NULL UNIQUE, PRIMARY KEY(project, app));`);

// A small deterministic random source (mulberry32).
let seed = 20261005;
const rnd = () => {
  seed |= 0;
  seed = (seed + 0x6d2b79f5) | 0;
  let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
};
const pick = <T>(xs: Array<[T, number]>): T => {
  const total = xs.reduce((s, x) => s + x[1], 0);
  let r = rnd() * total;
  for (const [v, w] of xs) if ((r -= w) <= 0) return v;
  return xs[xs.length - 1][0];
};
let ids = 1_000_003;
const id = () => (ids += 1 + Math.floor(rnd() * 97)); // unique, like the box's random ids

const PROJECT = "hello";
const HOUR = 3_600_000;
const DAY = 86_400_000;
const now = Date.now();
const start = Math.floor((now - 75 * DAY) / DAY) * DAY;
const launch = Math.floor((now - 12 * DAY) / DAY) * DAY; // the Hacker News day

const countries: Array<[string, number]> = [["US", 30], ["GB", 11], ["DE", 9], ["IN", 8], ["FR", 5], ["CA", 5], ["NL", 4], ["BR", 4], ["AU", 3], ["JP", 3], ["SE", 2], ["ES", 2], ["PL", 2], ["SG", 1.5], ["NG", 1], ["MX", 1], ["KR", 1], ["ZA", 0.8], ["", 1.5]];
const agents: Array<[[string, string, string], number]> = [
  [["Chrome", "Mac OS X", "desktop"], 24], [["Chrome", "Windows", "desktop"], 20], [["Safari", "Mac OS X", "desktop"], 9], [["Firefox", "Windows", "desktop"], 5],
  [["Edge", "Windows", "desktop"], 4], [["Firefox", "Linux", "desktop"], 2], [["Mobile Safari", "iOS", "mobile"], 18], [["Chrome Mobile", "Android", "mobile"], 13],
  [["Samsung Internet", "Android", "mobile"], 2], [["Mobile Safari", "iOS", "tablet"], 3],
];
const sources: Array<[[string, string, string, string, string], number]> = [
  // ref_source, ref_host, utm_source, utm_medium, utm_campaign
  [["", "", "", "", ""], 38], [["Google", "google.com", "", "", ""], 24], [["GitHub", "github.com", "", "", ""], 6], [["X (Twitter)", "t.co", "", "", ""], 5],
  [["DuckDuckGo", "duckduckgo.com", "", "", ""], 3], [["Bing", "bing.com", "", "", ""], 2], [["Reddit", "reddit.com", "", "", ""], 3], [["ChatGPT", "chatgpt.com", "", "", ""], 2],
  [["newsletter", "", "newsletter", "email", "october-launch"], 5], [["producthunt", "producthunt.com", "producthunt", "referral", "launch-day"], 1.5],
  [["dev.to", "dev.to", "", "", ""], 1.5],
];
const entries: Array<[string, number]> = [["/", 46], ["/pricing", 12], ["/blog/shipping-on-a-budget", 14], ["/docs/getting-started", 12], ["/changelog", 5], ["/blog/why-one-box", 6], ["/about", 2], ["/signup", 3]];
const next: Record<string, Array<[string, number]>> = {
  "/": [["/pricing", 30], ["/docs/getting-started", 25], ["/blog/shipping-on-a-budget", 10], ["/changelog", 8], ["/about", 5], ["/signup", 12]],
  "/pricing": [["/signup", 40], ["/", 20], ["/docs/getting-started", 20], ["/about", 5]],
  "/docs/getting-started": [["/docs/deploying", 45], ["/docs/databases", 25], ["/pricing", 15], ["/", 5]],
  "/docs/deploying": [["/docs/databases", 40], ["/docs/getting-started", 15], ["/pricing", 20]],
  "/docs/databases": [["/docs/deploying", 30], ["/pricing", 30], ["/signup", 15]],
  "/blog/shipping-on-a-budget": [["/", 35], ["/pricing", 25], ["/blog/why-one-box", 20]],
  "/blog/why-one-box": [["/", 30], ["/pricing", 30], ["/docs/getting-started", 20]],
  "/changelog": [["/", 40], ["/docs/getting-started", 20]],
  "/about": [["/", 40], ["/pricing", 20]],
  "/signup": [["/docs/getting-started", 50], ["/", 10]],
};
const apiPages: Array<[string, number]> = [["/", 40], ["/reference", 35], ["/reference/projects", 15], ["/status", 10]];

const ins = db.prepare(`INSERT INTO events(ts, day, project, app, kind, name, host, path, ref_source, ref_host, utm_source, utm_medium, utm_campaign,
  country, browser, os, device, visitor, session, props, src) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`);
const dayOf = (t: number) => new Date(t).toISOString().slice(0, 10);

/** Visits per hour at t: weekday rhythm, daytime curve, growth, the launch spike. */
function rate(t: number) {
  const d = new Date(t);
  const h = d.getUTCHours() + d.getUTCMinutes() / 60;
  const daily = 0.35 + 0.65 * Math.exp(-((h - 16) ** 2) / 32);
  const weekday = [0.62, 1.05, 1.1, 1.08, 1.02, 0.9, 0.6][d.getUTCDay()];
  const growth = 1 + (0.45 * (t - start)) / (now - start);
  const spike = t >= launch && t < launch + DAY ? 5.5 * Math.exp(-(((t - launch) / HOUR - 15) ** 2) / 18) + 1 : t >= launch + DAY && t < launch + 3 * DAY ? 1.5 : 1;
  return 22 * daily * weekday * growth * spike;
}

let n = 0;
db.exec("BEGIN");
for (let t = start; t < now; t += HOUR) {
  const visits = Math.round(rate(t) * (0.8 + rnd() * 0.4));
  for (let v = 0; v < visits; v++) {
    let at = t + Math.floor(rnd() * HOUR);
    if (at > now) continue;
    const [browser, os, device] = pick(agents);
    const country = pick(countries);
    const launchDay = t >= launch && t < launch + 2 * DAY;
    let src = pick(sources);
    if (launchDay && rnd() < 0.55) src = ["Hacker News", "news.ycombinator.com", "", "", ""];
    const visitor = id();
    const session = id();
    const app = rnd() < 0.14 ? "api" : "web";
    const host = app === "web" ? "hello.tiffin.localhost" : "api.hello.tiffin.localhost";
    let path = app === "web" ? (launchDay && rnd() < 0.5 ? "/blog/why-one-box" : pick(entries)) : pick(apiPages);
    const views = rnd() < 0.44 ? 1 : 2 + Math.floor(-Math.log(1 - rnd()) * 1.6);
    for (let k = 0; k < views && at <= now; k++) {
      const first = k === 0;
      ins.run(at, dayOf(at), PROJECT, app, "pageview", "pageview", host, path, first ? src[0] : "", first ? src[1] : "", first ? src[2] : "", first ? src[3] : "", first ? src[4] : "",
        country, browser, os, device, visitor, session, "", "edge");
      n++;
      if (app === "web" && path === "/signup" && rnd() < 0.45) {
        ins.run(at + 40_000, dayOf(at), PROJECT, app, "event", "Signup", host, path, "", "", "", "", "", country, browser, os, device, visitor, session, JSON.stringify({ plan: pick([["free", 7], ["pro", 3]]) }), "script");
      }
      if (app === "web" && path === "/pricing" && rnd() < 0.12) {
        ins.run(at + 25_000, dayOf(at), PROJECT, app, "event", "Checkout started", host, path, "", "", "", "", "", country, browser, os, device, visitor, session, JSON.stringify({ plan: "pro" }), "script");
      }
      if (app === "web" && path.startsWith("/docs") && rnd() < 0.08) {
        ins.run(at + 30_000, dayOf(at), PROJECT, app, "event", "Outbound Link: Click", host, path, "", "", "", "", "", country, browser, os, device, visitor, session, JSON.stringify({ url: "https://github.com/shiptiffin/tiffin" }), "script");
      }
      if (app === "web" && path === "/blog/shipping-on-a-budget" && rnd() < 0.06) {
        ins.run(at + 50_000, dayOf(at), PROJECT, app, "event", "File Download", host, path, "", "", "", "", "", country, browser, os, device, visitor, session, JSON.stringify({ url: "https://hello.tiffin.localhost/budget-sheet.pdf" }), "script");
      }
      at += 15_000 + Math.floor(-Math.log(1 - rnd()) * 70_000);
      path = app === "web" ? pick(next[path] ?? [["/", 1]]) : pick(apiPages);
    }
  }
}
db.exec("COMMIT");

// Daily rollups, as the box computes them (Store.Rollup).
const days = db.query(`SELECT DISTINCT app, day FROM events WHERE project = ?`).all(PROJECT) as Array<{ app: string; day: string }>;
const roll = db.prepare(`INSERT OR REPLACE INTO daily(project, app, day, visitors, pageviews, sessions, bounces, duration_ms, events, updated_at)
  SELECT ?1, ?2, ?3,
    (SELECT COUNT(DISTINCT visitor) FROM events WHERE project = ?1 AND app = ?2 AND day = ?3 AND kind = 'pageview'),
    (SELECT COUNT(*) FROM events WHERE project = ?1 AND app = ?2 AND day = ?3 AND kind = 'pageview'),
    COALESCE(SUM(1), 0), COALESCE(SUM(pv = 1), 0), COALESCE(SUM(dur), 0),
    (SELECT COUNT(*) FROM events WHERE project = ?1 AND app = ?2 AND day = ?3 AND kind = 'event'), ?4
  FROM (SELECT session, COUNT(*) AS pv, MAX(ts) - MIN(ts) AS dur FROM events
    WHERE project = ?1 AND app = ?2 AND day = ?3 AND kind = 'pageview' GROUP BY session)`);
db.exec("BEGIN");
for (const d of days) roll.run(PROJECT, d.app, d.day, now);
db.exec("COMMIT");

// Web Vitals: bucket counts per page, metric and day (5% wide buckets, CLS × 1000).
const bucket = (v: number) => (v < 1 ? 0 : 1 + Math.floor(Math.log(v) / Math.log(1.05)));
const normal = () => Math.sqrt(-2 * Math.log(1 - rnd())) * Math.cos(2 * Math.PI * rnd());
const vit = db.prepare(`INSERT INTO vitals(project, app, day, path, metric, bucket, n) VALUES (?, ?, ?, ?, ?, ?, ?)
  ON CONFLICT(project, app, day, path, metric, bucket) DO UPDATE SET n = n + excluded.n`);
// [median, spread] per page and metric: the docs are quick, the blog's hero image is slow, pricing shifts.
const pages: Record<string, Record<string, [number, number]>> = {
  "/": { LCP: [1700, 0.45], INP: [120, 0.5], CLS: [40, 0.8], FCP: [1000, 0.4], TTFB: [260, 0.5] },
  "/pricing": { LCP: [2100, 0.45], INP: [150, 0.5], CLS: [140, 0.6], FCP: [1150, 0.4], TTFB: [300, 0.5] },
  "/blog/shipping-on-a-budget": { LCP: [3600, 0.4], INP: [110, 0.5], CLS: [30, 0.8], FCP: [1500, 0.4], TTFB: [420, 0.5] },
  "/docs/[slug]": { LCP: [1200, 0.4], INP: [90, 0.5], CLS: [10, 0.8], FCP: [800, 0.4], TTFB: [180, 0.5] },
  "/signup": { LCP: [1500, 0.4], INP: [420, 0.45], CLS: [20, 0.8], FCP: [950, 0.4], TTFB: [240, 0.5] },
};
db.exec("BEGIN");
for (let t = start; t < now; t += DAY) {
  for (const [path, ms] of Object.entries(pages)) {
    for (const [metric, [median, spread]] of Object.entries(ms)) {
      const counts = new Map<number, number>();
      const samples = 30 + Math.floor(rnd() * 30);
      for (let i = 0; i < samples; i++) {
        const b = bucket(median * Math.exp(spread * normal()));
        counts.set(b, (counts.get(b) ?? 0) + 1);
      }
      for (const [b, c] of counts) vit.run(PROJECT, "web", dayOf(t), path, metric, b, c);
    }
  }
}
db.exec("COMMIT");
db.close();
console.log(`seeded analytics: ${n} page views over 75 days for ${PROJECT}`);
