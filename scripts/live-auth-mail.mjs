#!/usr/bin/env node
// Live check of every email-driven sign-in flow on a real box, as a hosted
// user would see it: sign-up + verification, password reset, magic link and
// email code, plus the bot check and rate limiting.
//
// It makes a throwaway project (a static page with auth + email), runs each
// flow against https://<project>.<app domain>/api/auth/*, reads every mail
// back from the project's email log (`tiffin email messages`), follows the
// links in it, and destroys the project at the end (users go with it).
//
// Real mail goes out: 5 messages to --to per full run (two verification
// mails, reset, magic link, code). Nothing else is mailed; --only f sends none.
//
// Run it from an address on the box's CrowdSec owner allowlist (the
// machine you ran tiffin up from): flow f deliberately trips the sign-in
// limits, which would get any other address banned for a minute.
//
//   node scripts/live-auth-mail.mjs                 # the box ./bin/tiffin points at
//   node scripts/live-auth-mail.mjs --keep          # leave the project up afterwards
//   node scripts/live-auth-mail.mjs --teardown      # only destroy the project
//   node scripts/live-auth-mail.mjs --reuse         # keep the project if it exists (no destroy first)
//   node scripts/live-auth-mail.mjs --only f        # just the bot-check and rate-limit checks: no mail
//   TIFFIN=/path/to/tiffin node scripts/live-auth-mail.mjs --project mailcheck3
//
// Tokens never pass through this script: every box call goes through the
// tiffin CLI, which reads ~/.tiffin/boxes.json itself. Links and codes from
// the mails are redacted in the output.
import { execFile } from "node:child_process";
import { createRequire } from "node:module";
import { mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { resolveCname, resolveTxt } from "node:dns/promises";
import { randomBytes } from "node:crypto";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");

// ---- options -------------------------------------------------------------

const argv = process.argv.slice(2);
const flag = (name) => argv.includes(`--${name}`);
const opt = (name, def) => {
  const i = argv.indexOf(`--${name}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : def;
};
const PROJECT = opt("project", "mailcheck2");
const TO = opt("to", "hello@shiptiffin.com").toLowerCase();
const FROM = opt("from", "hello@shiptiffin.com");
const APP_NAME = opt("app-name", "Mail Check");
const TIFFIN = process.env.TIFFIN || join(ROOT, "bin", "tiffin");
const KEEP = flag("keep");
const TEARDOWN_ONLY = flag("teardown");
// --reuse: keep an existing project (no destroy + re-create first); its
// auth rows are cleared before the flows run.
const REUSE = flag("reuse");
// --only a,c,f: run just these flows (b needs a; a-e send mail, f sends none).
const ONLY = new Set((opt("only", "a,b,c,d,e,f") || "").split(",").map((x) => x.trim()));
// There is no dry run: a suppressed recipient is refused at RCPT TO by the
// box's SMTP server, so the auth mail is neither sent nor logged.
const OK_STATUS = ["sent", "delivered"];
const OK_DELIVERY = "relay";

if (/@youtldr\.com$/i.test(TO)) {
  console.error("refusing to mail @youtldr.com");
  process.exit(2);
}

// ---- altcha-lib (lives in the workspace packages, not at the root) -------

async function loadAltcha() {
  for (const pkg of ["packages/sdk", "packages/auth-engine"]) {
    try {
      const req = createRequire(join(ROOT, pkg, "package.json"));
      const lib = await import(pathToFileURL(req.resolve("altcha-lib")).href);
      const sha = await import(pathToFileURL(req.resolve("altcha-lib/algorithms/sha")).href);
      const solve = lib.solveChallenge ?? lib.default?.solveChallenge;
      const deriveKey = sha.deriveKey ?? sha.default?.deriveKey;
      if (solve && deriveKey) return { solve, deriveKey };
    } catch {
      // try the next one
    }
  }
  throw new Error("altcha-lib not found: run `bun install` in the repo first");
}

// ---- output ----------------------------------------------------------------

/** Hides one-time tokens, codes and anything long. */
function red(s) {
  return String(s)
    .replace(/([?&](token|otp|code)=)[^&#\s"]+/gi, "$1…")
    .replace(/("(token|otp|password|newPassword)"\s*:\s*")[^"]+"/g, '$1…"')
    .replace(/(\/reset-password\/)[^?\s"]+/g, "$1…")
    .replace(/\b(\d{6})\b(?= is your)/g, "••••••")
    .replace(/[A-Za-z0-9_\-.]{40,}/g, (m) => `${m.slice(0, 6)}…(${m.length})`);
}
const log = (...a) => console.log(...a.map((x) => red(typeof x === "string" ? x : JSON.stringify(x))));

const results = []; // { flow, check, ok, evidence }
let current = "setup";
function check(name, ok, evidence = "") {
  results.push({ flow: current, check: name, ok: !!ok, evidence: red(evidence) });
  console.log(`  ${ok ? "PASS" : "FAIL"}  ${name}${evidence ? `  — ${red(evidence)}` : ""}`);
  return !!ok;
}
function section(id, title) {
  current = id;
  console.log(`\n== ${id}. ${title}`);
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---- the tiffin CLI ----------------------------------------------------------

function cli(args, { allowFail = false } = {}) {
  return new Promise((res, rej) => {
    execFile(TIFFIN, [...args, "--json"], { maxBuffer: 64 << 20, timeout: 600_000 }, (err, stdout, stderr) => {
      let json = null;
      try {
        json = JSON.parse(stdout);
      } catch {
        // not JSON
      }
      if (err && !allowFail) {
        rej(new Error(`tiffin ${args.join(" ")} failed (${err.code}): ${red((stdout || stderr).slice(0, 800))}`));
        return;
      }
      res({ code: err ? err.code : 0, json, stdout, stderr });
    });
  });
}

// ---- HTTP against the app's host -------------------------------------------

let APP = ""; // https://<project>.<domain>
let altcha;

class Jar {
  constructor() {
    this.c = new Map();
  }
  take(res) {
    for (const sc of res.headers.getSetCookie?.() ?? []) {
      const [pair, ...attrs] = sc.split(";");
      const i = pair.indexOf("=");
      const name = pair.slice(0, i).trim();
      const value = pair.slice(i + 1).trim();
      const expired = attrs.some((a) => /max-age=0\b/i.test(a)) || value === "";
      if (expired) this.c.delete(name);
      else this.c.set(name, value);
    }
  }
  header() {
    return [...this.c].map(([k, v]) => `${k}=${v}`).join("; ");
  }
  names() {
    return [...this.c.keys()];
  }
}

async function solveCaptcha() {
  const r = await fetch(`${APP}/api/auth/altcha/challenge`, { headers: { origin: APP } });
  if (!r.ok) throw new Error(`challenge: HTTP ${r.status}`);
  const challenge = await r.json();
  const solution = await altcha.solve({ challenge, deriveKey: altcha.deriveKey, timeout: 60_000 });
  if (!solution) throw new Error("couldn't solve the challenge");
  return Buffer.from(JSON.stringify({ challenge, solution })).toString("base64");
}

// The box's edge allows 10 sign-in-type POSTs a minute per address (on top of
// the engine's own limits). Stay under it, so only the deliberate burst in
// flow f meets a limit.
const EDGE_AUTH = /^\/api\/auth\/(sign-in\/|sign-up\/|request-password-reset|reset-password|email-otp\/|magic-link\/|two-factor\/verify-|forget-password)/;
const edgePosts = [];
async function paceEdge(method, path) {
  if (!["POST", "PUT", "PATCH"].includes(method) || !EDGE_AUTH.test(new URL(path, APP).pathname)) return;
  for (;;) {
    const now = Date.now();
    while (edgePosts.length && now - edgePosts[0] > 61_000) edgePosts.shift();
    if (edgePosts.length < 9) break;
    const wait = 61_000 - (now - edgePosts[0]);
    log(`    (pacing: ${Math.ceil(wait / 1000)}s for the edge's sign-in limit)`);
    await sleep(wait);
  }
  edgePosts.push(Date.now());
}

/** Waits until n more paced POSTs fit in the edge's minute. */
async function edgeRoom(n) {
  const now = Date.now();
  const live = edgePosts.filter((t) => now - t <= 61_000);
  if (live.length + n <= 9) return;
  const wait = 61_000 - (now - live[live.length + n - 10]);
  log(`    (waiting ${Math.ceil(wait / 1000)}s for room under the edge's sign-in limit)`);
  await sleep(wait);
}

/** One request. captcha: true solves a fresh challenge; a string is sent as is. */
async function call(method, path, { body, jar, captcha, retry429 = true, pace = true } = {}) {
  for (let attempt = 0; ; attempt++) {
    if (pace) await paceEdge(method, path);
    const headers = { origin: APP, accept: "application/json" };
    if (body) headers["content-type"] = "application/json";
    if (jar?.c.size) headers.cookie = jar.header();
    if (captcha === true) headers["x-captcha-response"] = await solveCaptcha();
    else if (typeof captcha === "string") headers["x-captcha-response"] = captcha;
    const url = path.startsWith("http") ? path : `${APP}${path}`;
    const res = await fetch(url, { method, headers, body: body ? JSON.stringify(body) : undefined, redirect: "manual" });
    jar?.take(res);
    const text = await res.text();
    let json = null;
    try {
      json = JSON.parse(text);
    } catch {
      // not JSON
    }
    if (res.status === 429 && retry429 && attempt < 3) {
      const edge = json?.code === "RATE_LIMITED" || /text\/html/.test(res.headers.get("content-type") ?? "");
      const wait = Number(res.headers.get("x-retry-after") || res.headers.get("retry-after") || (edge ? 60 : 10));
      log(`    (429 from the ${edge ? "edge" : "engine"} on ${path}, waiting ${wait}s)`);
      await sleep((wait + 1) * 1000);
      continue;
    }
    return { status: res.status, json, text, headers: res.headers, location: res.headers.get("location") };
  }
}

const brief = (r) => `HTTP ${r.status} ${r.json ? JSON.stringify(r.json).slice(0, 220) : r.text.slice(0, 160)}`;
const sessionCookie = (jar) => jar.names().find((n) => /session_token$/.test(n));

async function session(jar) {
  const r = await call("GET", "/api/auth/get-session", { jar });
  return r.json && r.json.user ? r.json : null;
}

// ---- the email log -----------------------------------------------------------

const seen = new Set(); // message IDs already matched
const ours = []; // every message this run made, to the recipient
const startedAt = Date.now();

async function listMail() {
  const { json } = await cli(["email", "messages", "list", PROJECT, "--all", "--limit", "100"]);
  return Array.isArray(json) ? json : (json?.messages ?? json?.items ?? []);
}

/** Waits for a new message to the recipient whose subject matches. */
async function waitMail(subject, label, timeoutMs = 60_000) {
  const until = Date.now() + timeoutMs;
  while (Date.now() < until) {
    const list = await listMail();
    const m = list.find(
      (x) =>
        !seen.has(x.id) &&
        new Date(x.createdAt).getTime() >= startedAt - 5_000 &&
        (x.to ?? []).some((t) => t.toLowerCase().includes(TO)) &&
        subject.test(x.subject ?? ""),
    );
    if (m) {
      seen.add(m.id);
      ours.push({ id: m.id, label });
      return m;
    }
    await sleep(1500);
  }
  return null;
}

/** Waits until the relay has taken it (status leaves "queued"), then reads it. */
async function settle(id, timeoutMs = 120_000) {
  const until = Date.now() + timeoutMs;
  let d;
  for (;;) {
    ({ json: d } = await cli(["email", "messages", "get", PROJECT, id]));
    if (d.status !== "queued" || Date.now() > until) return d;
    await sleep(2000);
  }
}

const header = (d, name) => (d.headers ?? []).find((h) => h.name.toLowerCase() === name.toLowerCase())?.value ?? "";

/** The checks every auth mail must pass (flow g), recorded under the flow that sent it. */
function checkMail(d, { kind, subject, needLink = true }) {
  check(`${kind}: relayed, status sent (not captured/failed)`, d.delivery === OK_DELIVERY && OK_STATUS.includes(d.status), `delivery=${d.delivery} status=${d.status} attempts=${d.attempts ?? 0}${d.lastError ? ` lastError=${d.lastError}` : ""}${d.reason ? ` reason=${d.reason}` : ""}`);
  check(`${kind}: From is ${FROM}`, (d.from ?? "").toLowerCase().includes(FROM.toLowerCase()), `From: ${d.from}`);
  check(`${kind}: To is ${TO}`, (d.to ?? []).map((t) => t.toLowerCase()).some((t) => t.includes(TO)), `To: ${(d.to ?? []).join(", ")}`);
  check(`${kind}: subject names the app`, subject.test(d.subject ?? "") && (d.subject ?? "").includes(APP_NAME), `Subject: ${d.subject}`);
  check(`${kind}: X-Tiffin-Kind header`, header(d, "X-Tiffin-Kind") === `auth.${kind}`, `X-Tiffin-Kind: ${header(d, "X-Tiffin-Kind")}`);
  const ct = header(d, "Content-Type");
  check(`${kind}: text and HTML parts`, /multipart\/alternative/i.test(ct) && (d.text ?? "").trim().length > 40 && (d.html ?? "").length > 200, `Content-Type: ${ct.split(";")[0]}; text ${(d.text ?? "").length} chars; html ${(d.html ?? "").length} chars`);
  check(`${kind}: HTML shows the app name`, (d.html ?? "").includes(APP_NAME.replace(/&/g, "&amp;")), "");
  if (needLink) {
    const links = d.links ?? [];
    // The footer links the app's address itself (no path).
    const bad = links.filter((l) => !(l === APP || l.startsWith(`${APP}/`)) || /localhost|127\.0\.0\.1|dashboard\.|:\d{2,5}\//.test(l));
    check(`${kind}: links point at ${APP}`, links.length > 0 && bad.length === 0, `links: ${links.map((l) => red(l)).join(" ")}`);
    const link = links.find((l) => l.startsWith(`${APP}/api/auth/`));
    const html = d.html ?? "";
    check(`${kind}: same link in text and HTML`, !!link && (d.text ?? "").includes(link) && (html.includes(link) || html.includes(link.replace(/&/g, "&amp;"))), "");
  }
  const dkim = header(d, "DKIM-Signature");
  // The log keeps the message as the box handed it to the relay; SendGrid signs after.
  results.push({ flow: current, check: `${kind}: DKIM-Signature in stored headers`, ok: null, evidence: dkim ? `d=${(/\bd=([^;\s]+)/.exec(dkim) ?? [])[1]}` : "none stored (SendGrid signs after the box hands it over)" });
  check(`${kind}: has Message-ID`, !!header(d, "Message-ID"), `Message-ID: ${header(d, "Message-ID")}`);
}

const authLink = (d, path) => (d?.links ?? []).find((l) => l.startsWith(`${APP}/api/auth${path}`));

// ---- setup and teardown ------------------------------------------------------

function writeApp() {
  const dir = join(tmpdir(), `tiffin-live-auth-mail-${PROJECT}`);
  mkdirSync(join(dir, "public"), { recursive: true });
  writeFileSync(
    join(dir, "tiffin.config.ts"),
    `import { defineConfig } from "@shiptiffin/sdk";

// Throwaway app for scripts/live-auth-mail.mjs. Destroyed when the run ends.
export default defineConfig({
  project: ${JSON.stringify(PROJECT)},
  env: { APP_NAME: ${JSON.stringify(APP_NAME)} },
  apps: { site: { framework: "static" } },
  services: {
    postgres: {},
    auth: { methods: ["email", "magic-link", "otp"], organizations: true },
    email: { from: ${JSON.stringify(FROM)} },
  },
});
`,
  );
  writeFileSync(join(dir, "public", "index.html"), `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>${PROJECT}</title></head><body><h1>${PROJECT}</h1><p>Sign-in mail check. This project is temporary.</p></body></html>\n`);
  return dir;
}

async function projectExists() {
  const { json } = await cli(["projects", "list"]);
  return (Array.isArray(json) ? json : []).some((p) => p.name === PROJECT);
}

/** What the box still keeps for the project: null when nothing. */
async function leftovers() {
  const r = await cli(["projects", "get", PROJECT], { allowFail: true });
  if (r.code !== 0) return null;
  const st = Object.values(r.json?.status ?? {});
  if (!(r.json?.resources ?? []).length && !st.length) return null;
  return st.map((x) => `${x.address}: ${x.state}: ${x.message}`).join("; ") || `resources: ${JSON.stringify(r.json?.resources)}`;
}

/** Destroys the project and checks nothing of it is left. */
async function destroy() {
  if (await projectExists()) {
    // Clear the auth rows first: the database snapshot taken at the destroy
    // (kept 7 days, and restored if a project of the same name is created
    // again) then holds no test user.
    const left = await purgeAuth();
    check("test user and its rows deleted before the destroy", !left.user && !left.session && !left.account && !left.verification && !left.organization, JSON.stringify(left));
    const plan = await cli(["projects", "destroy", PROJECT], { allowFail: true });
    const hash = plan.json?.hash ?? plan.json?.plan?.hash;
    if (!hash) throw new Error(`no plan hash from projects destroy: ${red(plan.stdout.slice(0, 400))}`);
    await cli(["projects", "destroy", PROJECT, "--confirm", hash, "-m", "Remove the throwaway sign-in mail test project"]);
    for (let i = 0; i < 30 && (await projectExists()); i++) await sleep(2000);
  }
  check("project gone from the box", !(await projectExists()));
  // The reconcile finishes after the API answers: watch it settle (up to 3 min).
  const t0 = Date.now();
  let left = null;
  let firstFailure = null;
  for (;;) {
    left = await leftovers();
    if (left && !firstFailure && /failed/.test(left)) firstFailure = { at: Date.now() - t0, what: left };
    if (!left || Date.now() - t0 > 180_000) break;
    await sleep(3000);
  }
  const settledIn = Math.round((Date.now() - t0) / 1000);
  check("destroy converges without a failed resource", !firstFailure, firstFailure ? `after ${Math.round(firstFailure.at / 1000)}s: ${firstFailure.what}${left ? "" : `; cleared by itself within ${settledIn}s`}` : "");
  check("nothing left behind", !left, left ?? `settled in ${settledIn}s`);
  return !(await projectExists());
}

// The project's auth rows, through the db API (the test user, its personal
// organization, pending verification tokens). Deleting the user cascades to
// its accounts, sessions and memberships.
const AUTH_TABLES = ["user", "organization", "verification"];

async function tableRows(table) {
  const r = await cli(["db", "rows", PROJECT, "tiffin_auth", table, "--limit", "100"], { allowFail: true });
  if (r.code !== 0) return [];
  const cols = (r.json?.columns ?? []).map((c) => (typeof c === "string" ? c : c.name));
  return (r.json?.rows ?? []).map((row) => Object.fromEntries(cols.map((c, i) => [c, row[i]])));
}

async function authRows() {
  const n = {};
  for (const t of [...AUTH_TABLES, "session", "account"]) n[t] = (await tableRows(t)).length;
  return n;
}

async function purgeAuth() {
  for (const t of AUTH_TABLES) {
    const rows = await tableRows(t);
    if (!rows.length) continue;
    await cli(["db", "delete", PROJECT, "tiffin_auth", t, "--body", JSON.stringify({ keys: rows.map((r) => ({ id: r.id })) })], { allowFail: true });
  }
  return authRows();
}

async function setup() {
  section("0", `Set up ${PROJECT}`);
  const status = (await cli(["email", "status"])).json;
  check("box sends through a relay", status?.mode === "relay", `mode=${status?.mode} relay=${status?.relay?.host ?? "none"} failedLastDay=${status?.failedLastDay}`);
  if (await projectExists()) {
    if (REUSE) {
      log(`  reusing ${PROJECT}`);
    } else {
      log(`  ${PROJECT} exists from an earlier run: destroying it for a clean start`);
      await destroy();
    }
  }
  const created = !(await projectExists());
  const dir = writeApp();
  const plan = await cli(["plan", dir], { allowFail: true });
  const hash = plan.json?.hash;
  if (!hash) throw new Error(`plan failed: ${red(plan.stdout.slice(0, 600))}`);
  await cli(["apply", dir, "--confirm", hash, "-m", "Throwaway project to check sign-in mail end to end"]);
  const dep = await new Promise((res) => {
    execFile(TIFFIN, ["deploy", dir, "--json"], { cwd: dir, maxBuffer: 64 << 20, timeout: 600_000 }, (err, stdout) => {
      try {
        res(JSON.parse(stdout));
      } catch {
        res({ ok: false, raw: stdout, err: String(err) });
      }
    });
  });
  const live = dep.deploys?.find((d) => d.status === "live");
  check("deployed", dep.ok && live, live ? `${live.id} ${live.url}` : red(JSON.stringify(dep).slice(0, 300)));
  APP = (live?.url ?? "").replace(/\/+$/, "");
  if (!APP) throw new Error("no app URL");
  // The engine reloads its config after apply: wait for auth to be ready.
  let st = null;
  for (let i = 0; i < 45; i++) {
    st = (await cli(["projects", "get", PROJECT])).json?.status?.["service/auth"];
    const r = await fetch(`${APP}/api/auth/ok`).catch(() => null);
    if (st?.state === "ready" && r?.ok) break;
    await sleep(2000);
  }
  const authOk = check("auth service ready", st?.state === "ready", `${st?.state}${st?.message ? `: ${st.message}` : ""}`);
  if (!authOk) {
    if (/password authentication failed|SASL/.test(st?.message ?? "")) {
      log("  The auth engine can't log in to the project's database through PgBouncer. Seen when a project is re-created under a name used before: PgBouncer keeps the old role's SCRAM keys. Use --project <new name>, or KILL + RESUME the database on PgBouncer's admin console.");
    }
    throw new Error("auth isn't ready; no mail was sent");
  }
  await cli(["email", "suppressions", "delete", PROJECT, TO], { allowFail: true });
  const sup = (await cli(["email", "suppressions", "list", PROJECT], { allowFail: true })).json;
  const supList = Array.isArray(sup) ? sup : (sup?.suppressions ?? sup?.items ?? []);
  check(`${TO} not suppressed`, !supList.some((x) => (x.address ?? "").toLowerCase() === TO), `${supList.length} suppressions`);
  const pre = await authRows();
  check("no users before the run", pre.user === 0, pre.user ? `${pre.user} user(s) already there: ${created ? "the re-created project brought back the database of the one destroyed before (Postgres undo snapshot)" : "left by an earlier run"}; clearing them` : "");
  if (pre.user || pre.verification || pre.organization) await purgeAuth();
  const show = (await cli(["auth", "show", PROJECT])).json;
  check("auth on with email, magic-link, otp", ["email", "magic-link", "otp"].every((m) => show?.methods?.includes(m)), `methods=${show?.methods?.join(",")}`);
  check("email verification required (relay set)", show?.emailVerification?.required === true, `emailVerification=${JSON.stringify(show?.emailVerification)}`);
}

// ---- the flows --------------------------------------------------------------

const PASSWORD = `Pw-${randomBytes(9).toString("base64url")}`;
const NEW_PASSWORD = `Pw2-${randomBytes(9).toString("base64url")}`;

async function flowSignUp() {
  section("a", "Sign up with email + password; sign-in blocked until verified");
  const r = await call("POST", "/api/auth/sign-up/email", { body: { name: "Mail Check", email: TO, password: PASSWORD, callbackURL: "/welcome" }, captcha: true });
  check("sign-up accepted, no session yet", r.status === 200 && r.json?.token == null && r.json?.user?.emailVerified === false, brief(r));
  const m1 = await waitMail(/^Confirm your email for /, "verify (sign-up)");
  check("verification mail in the log", !!m1, m1 ? `${m1.id} "${m1.subject}"` : "nothing within 60s");
  const jar = new Jar();
  const s = await call("POST", "/api/auth/sign-in/email", { body: { email: TO, password: PASSWORD }, captcha: true, jar });
  check("sign-in refused before verifying", s.status === 403 && s.json?.code === "EMAIL_NOT_VERIFIED" && !sessionCookie(jar), brief(s));
  check("refusal text is sensible", /verif/i.test(s.json?.message ?? ""), `message: ${s.json?.message}`);
  const m2 = await waitMail(/^Confirm your email for /, "verify (sign-in attempt)", 30_000);
  check("blocked sign-in sends a fresh verification mail", !!m2, m2 ? `${m2.id}` : "none within 30s");
  return { m1, m2 };
}

async function flowVerify({ m1, m2 }) {
  section("b", "Verification mail: relayed, link works, then sign-in works");
  const d1 = m1 && (await settle(m1.id));
  if (d1) checkMail(d1, { kind: "verify", subject: /^Confirm your email for / });
  const d2 = m2 && (await settle(m2.id));
  if (d2) check("second verification mail relayed", d2.delivery === OK_DELIVERY && OK_STATUS.includes(d2.status), `delivery=${d2.delivery} status=${d2.status}`);
  // The sign-up mail carries the sign-up's callbackURL; the one a blocked
  // sign-in sends has the default ("/"), since sign-in took none.
  const link = authLink(d1, "/verify-email");
  check("verification link found", !!link, link ?? "");
  if (!link) return;
  check("link carries the sign-up's callbackURL", /callbackURL=%2Fwelcome|callbackURL=\/welcome/.test(link), "");
  const link2 = authLink(d2, "/verify-email");
  check("second mail has its own link", !!link2 && link2 !== link, link2 ?? "");
  const jar = new Jar();
  const v = await call("GET", link, { jar });
  check("following the link verifies (redirect to callback)", v.status === 302 && /\/welcome/.test(v.location ?? ""), `HTTP ${v.status} → ${v.location}`);
  const auto = await session(jar);
  check("verifying signs the user in", auto?.user?.email === TO && auto.user.emailVerified === true, auto ? `user=${auto.user.email} verified=${auto.user.emailVerified}` : "no session");
  const jarAgain = new Jar();
  const again = await call("GET", link, { jar: jarAgain });
  check("re-opening the link doesn't sign in again", again.status < 500 && !sessionCookie(jarAgain), `HTTP ${again.status} → ${again.location ?? again.text.slice(0, 80)}; cookies: ${jarAgain.names().join(",") || "none"}`);
  const jar2 = new Jar();
  const s = await call("POST", "/api/auth/sign-in/email", { body: { email: TO, password: PASSWORD }, captcha: true, jar: jar2 });
  check("password sign-in works after verifying", s.status === 200 && !!s.json?.token && !!sessionCookie(jar2), brief(s));
  const who = await session(jar2);
  check("session is the user", who?.user?.email === TO, who ? `user=${who.user.email}` : "none");
}

async function flowReset() {
  section("c", "Password reset");
  const r = await call("POST", "/api/auth/request-password-reset", { body: { email: TO, redirectTo: "/reset-password" }, captcha: true });
  check("reset requested", r.status === 200 && r.json?.status === true, brief(r));
  const m = await waitMail(/^Reset your .* password$/, "reset");
  check("reset mail in the log", !!m, m ? `${m.id} "${m.subject}"` : "nothing within 60s");
  if (!m) return;
  const d = await settle(m.id);
  checkMail(d, { kind: "reset-password", subject: /^Reset your .* password$/ });
  const link = authLink(d, "/reset-password/");
  check("reset link found", !!link, link ?? "");
  if (!link) return;
  const g = await call("GET", link);
  const loc = g.location ? new URL(g.location, APP) : null;
  const token = loc?.searchParams.get("token");
  check("link redirects to the app with a token", g.status === 302 && loc?.origin === APP && loc.pathname === "/reset-password" && !!token, `HTTP ${g.status} → ${g.location}`);
  if (!token) return;
  const p = await call("POST", "/api/auth/reset-password", { body: { newPassword: NEW_PASSWORD, token } });
  check("new password set", p.status === 200 && p.json?.status === true, brief(p));
  const reuse = await call("POST", "/api/auth/reset-password", { body: { newPassword: PASSWORD, token } });
  check("reset token works once", reuse.status >= 400 && reuse.status < 500, brief(reuse));
  const old = await call("POST", "/api/auth/sign-in/email", { body: { email: TO, password: PASSWORD }, captcha: true });
  check("old password refused", old.status === 401, brief(old));
  const jar = new Jar();
  const s = await call("POST", "/api/auth/sign-in/email", { body: { email: TO, password: NEW_PASSWORD }, captcha: true, jar });
  check("new password signs in", s.status === 200 && !!sessionCookie(jar), brief(s));
}

async function flowMagic() {
  section("d", "Magic link");
  const r = await call("POST", "/api/auth/sign-in/magic-link", { body: { email: TO, callbackURL: "/dashboard" }, captcha: true });
  check("magic link requested", r.status === 200 && r.json?.status === true, brief(r));
  const m = await waitMail(/^Sign in to /, "magic-link");
  check("magic-link mail in the log", !!m, m ? `${m.id} "${m.subject}"` : "nothing within 60s");
  if (!m) return;
  const d = await settle(m.id);
  checkMail(d, { kind: "magic-link", subject: /^Sign in to / });
  const link = authLink(d, "/magic-link/verify");
  check("magic link found", !!link, link ?? "");
  if (!link) return;
  const jar = new Jar();
  const v = await call("GET", link, { jar });
  check("following it redirects to the callback", v.status === 302 && /\/dashboard/.test(v.location ?? ""), `HTTP ${v.status} → ${v.location}`);
  const who = await session(jar);
  check("and signs the user in", who?.user?.email === TO, who ? `user=${who.user.email}` : `no session; cookies: ${jar.names().join(",")}`);
  const jarAgain = new Jar();
  const again = await call("GET", link, { jar: jarAgain });
  check("magic link works once", again.status < 500 && !sessionCookie(jarAgain), `HTTP ${again.status} → ${again.location ?? again.text.slice(0, 80)}; cookies: ${jarAgain.names().join(",") || "none"}`);
}

async function flowOtp() {
  section("e", "Email one-time code");
  const r = await call("POST", "/api/auth/email-otp/send-verification-otp", { body: { email: TO, type: "sign-in" }, captcha: true });
  check("code requested", r.status === 200 && r.json?.success === true, brief(r));
  const m = await waitMail(/^\d{6} is your /, "otp");
  check("code mail in the log", !!m, m ? `${m.id} "${m.subject}"` : "nothing within 60s");
  if (!m) return;
  const d = await settle(m.id);
  checkMail(d, { kind: "otp", subject: /^\d{6} is your /, needLink: false });
  const otp = (/^(\d{6}) is your /.exec(d.subject) ?? [])[1];
  check("code is in subject, text and HTML", !!otp && d.text.includes(otp) && d.html.includes(otp), "");
  // A code mail carries no sign-in link: at most the footer's link to the app.
  check("code mail has no sign-in link", (d.links ?? []).every((l) => l === APP || l === `${APP}/`), `links: ${(d.links ?? []).map((l) => red(l)).join(" ") || "none"}`);
  const wrong = String((Number(otp) + 1) % 1_000_000).padStart(6, "0");
  const bad = await call("POST", "/api/auth/sign-in/email-otp", { body: { email: TO, otp: wrong } });
  check("wrong code refused", bad.status === 400 || bad.status === 401 || bad.status === 403, brief(bad));
  const jar = new Jar();
  const s = await call("POST", "/api/auth/sign-in/email-otp", { body: { email: TO, otp }, jar });
  check("right code signs in", s.status === 200 && !!sessionCookie(jar), brief(s));
  const who = await session(jar);
  check("session is the user", who?.user?.email === TO, who ? `user=${who.user.email}` : "none");
}

async function flowNegative() {
  section("f", "Bot check, rate limit, error text");
  const none = await call("POST", "/api/auth/sign-up/email", { body: { name: "x", email: TO, password: PASSWORD } });
  check("sign-up without captcha refused", none.status === 400 && none.json?.code === "CAPTCHA_REQUIRED", brief(none));
  check("…and says how to solve it", /altcha\/challenge/.test(none.json?.message ?? ""), `message: ${none.json?.message}`);
  const junk = await call("POST", "/api/auth/sign-in/email", { body: { email: TO, password: "wrong-password-1" }, captcha: "bm90LWEtcGF5bG9hZA==" });
  check("garbage captcha refused", junk.status === 400 && junk.json?.code === "CAPTCHA_INVALID", brief(junk));
  for (const path of ["/api/auth/sign-in/magic-link", "/api/auth/email-otp/send-verification-otp", "/api/auth/request-password-reset"]) {
    const n = await call("POST", path, { body: { email: TO, type: "sign-in", redirectTo: "/" } });
    check(`${path.replace("/api/auth", "")} without captcha refused (no mail)`, n.status === 400 && n.json?.code === "CAPTCHA_REQUIRED", brief(n));
  }
  // Wait out the sign-in window (3 per 10 s per address), then burst.
  await sleep(11_000);
  await edgeRoom(4);
  const sols = await Promise.all([1, 2, 3, 4, 5].map(() => solveCaptcha()));
  const reused = sols[4];
  const codes = [];
  let limited = null;
  for (let i = 0; i < 4; i++) {
    const r = await call("POST", "/api/auth/sign-in/email", { body: { email: TO, password: `wrong-password-${i}` }, captcha: sols[i], retry429: false });
    codes.push(r.status);
    if (r.status === 429 && !limited) limited = r;
  }
  check("wrong password refused with sensible text", codes[0] === 401, `statuses ${codes.join(",")}`);
  check("rate limit kicks in on the 4th try in 10 s", codes.slice(0, 3).every((c) => c === 401) && codes[3] === 429, `statuses ${codes.join(",")}`);
  if (limited) {
    const after = limited.headers.get("x-retry-after") ?? limited.headers.get("retry-after");
    check("429 says when to retry", !!after, `X-Retry-After: ${after}; body: ${limited.text.slice(0, 160)}`);
    check("429 text is sensible", /too many|slow down|try again/i.test(limited.json?.message ?? limited.text), `message: ${limited.json?.message ?? limited.text.slice(0, 120)}`);
  }
  await sleep(11_000);
  await edgeRoom(2);
  const once = await call("POST", "/api/auth/sign-in/email", { body: { email: TO, password: "wrong-password-x" }, captcha: reused, retry429: false });
  const twice = await call("POST", "/api/auth/sign-in/email", { body: { email: TO, password: "wrong-password-y" }, captcha: reused, retry429: false });
  check("a captcha solution works once", once.status === 401 && twice.status === 400 && twice.json?.code === "CAPTCHA_USED", `first ${brief(once)}; second ${brief(twice)}`);
  // The edge's own limit: sign-in POSTs per address per minute (no captcha,
  // so the engine refuses each at once and nothing is mailed). The engine
  // answers its own 429s in JSON; the edge's are the ones it sends itself.
  let edge = null;
  let n = 0;
  const seenCodes = [];
  for (; n < 20 && !edge; n++) {
    const r = await call("POST", "/api/auth/sign-in/email", { body: { email: TO, password: "x" }, retry429: false, pace: false });
    // The edge's own 429: its page, or for an API call its JSON (code RATE_LIMITED).
    const fromEdge = r.status === 429 && (r.json?.code === "RATE_LIMITED" || /text\/html/.test(r.headers.get("content-type") ?? ""));
    seenCodes.push(fromEdge ? "429(edge)" : String(r.status));
    if (fromEdge) edge = r;
  }
  check("edge limit kicks in on repeated sign-in POSTs", !!edge, `statuses ${seenCodes.join(",")}`);
  if (edge) {
    const ct = edge.headers.get("content-type") ?? "";
    const ra = edge.headers.get("retry-after") ?? edge.headers.get("x-retry-after");
    check("edge 429 answers an API call in JSON", /json/.test(ct), `Content-Type: ${ct}; Retry-After: ${ra ?? "none"}; body starts: ${edge.text.slice(0, 60).replace(/\s+/g, " ")}`);
    check("edge 429 says when to retry", !!ra, `Retry-After: ${ra ?? "none"}`);
  }
  edgePosts.push(...Array(10).fill(Date.now())); // the edge's minute is used up
}

async function flowDns() {
  section("g", "Sender domain (DNS) and the mail count");
  const domain = FROM.split("@")[1];
  const cname = async (n) => (await resolveCname(n).catch(() => []))[0] ?? "";
  const s1 = await cname(`s1._domainkey.${domain}`);
  const s2 = await cname(`s2._domainkey.${domain}`);
  check(`DKIM keys for ${domain} point at SendGrid`, /sendgrid\.net$/.test(s1) && /sendgrid\.net$/.test(s2), `s1 → ${s1 || "none"}; s2 → ${s2 || "none"}`);
  const txt = async (n) => (await resolveTxt(n).catch(() => [])).map((r) => r.join(""));
  const dmarc = (await txt(`_dmarc.${domain}`)).find((t) => t.startsWith("v=DMARC1")) ?? "";
  check(`DMARC for ${domain}`, !!dmarc, dmarc);
  const spf = (await txt(domain)).find((t) => t.startsWith("v=spf1")) ?? "";
  results.push({ flow: current, check: `SPF on ${domain} (SendGrid uses its own return path)`, ok: null, evidence: spf || "none on the apex" });
  const list = await listMail();
  const mine = list.filter((x) => new Date(x.createdAt).getTime() >= startedAt - 5_000 && (x.to ?? []).some((t) => t.toLowerCase().includes(TO)));
  const relayed = mine.filter((x) => x.delivery === "relay" && ["sent", "delivered", "queued"].includes(x.status));
  console.log(`  mail to ${TO} this run: ${mine.length} (${relayed.length} relayed): ${mine.map((x) => `${x.id} ${x.status} "${red(x.subject)}"`).join("; ")}`);
  check("no unexpected mail", mine.length === ours.length, `log ${mine.length}, matched ${ours.length}`);
  return relayed.length;
}

// ---- main ----------------------------------------------------------------

async function main() {
  if (TEARDOWN_ONLY) {
    section("z", `Destroy ${PROJECT}`);
    await destroy();
    process.exitCode = results.some((r) => r.ok === false) ? 1 : 0;
    return;
  }
  altcha = await loadAltcha();
  let sent = 0;
  try {
    await setup();
    if (ONLY.has("a") || ONLY.has("b")) {
      const a = await flowSignUp();
      if (ONLY.has("b")) await flowVerify(a);
    }
    if (ONLY.has("c") || ONLY.has("d") || ONLY.has("e")) {
      // Reset, magic link and code need a confirmed user: make one through a and b if they didn't run.
      if (!ONLY.has("b")) throw new Error("flows c, d and e need b (and a) for a confirmed user");
    }
    if (ONLY.has("c")) await flowReset();
    if (ONLY.has("d")) await flowMagic();
    if (ONLY.has("e")) await flowOtp();
    if (ONLY.has("f")) await flowNegative();
    sent = await flowDns();
  } catch (e) {
    check("run finished", false, String(e?.stack ?? e));
  } finally {
    if (!KEEP) {
      section("z", `Tear down ${PROJECT}`);
      try {
        await destroy();
      } catch (e) {
        check("project destroyed", false, String(e));
      }
    }
  }

  console.log("\n== Summary");
  const flows = { 0: "setup", a: "sign-up", b: "verify", c: "reset", d: "magic link", e: "email code", f: "bot/rate limit", g: "DNS + count", z: "teardown" };
  for (const [id, name] of Object.entries(flows)) {
    const rs = results.filter((r) => r.flow === id && r.ok !== null);
    if (!rs.length) continue;
    const fails = rs.filter((r) => !r.ok);
    console.log(`  ${fails.length ? "FAIL" : "PASS"}  ${id} ${name.padEnd(15)} ${rs.length - fails.length}/${rs.length}${fails.length ? `  ← ${fails.map((f) => f.check).join("; ")}` : ""}`);
  }
  const info = results.filter((r) => r.ok === null);
  for (const r of info) console.log(`  INFO  ${r.flow} ${r.check}: ${r.evidence}`);
  console.log(`  real mails relayed to ${TO}: ${sent}`);
  process.exitCode = results.some((r) => r.ok === false) ? 1 : 0;
}

main();
