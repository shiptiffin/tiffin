import { describe, expect, test } from "bun:test";
import { rateLimiter } from "./early-access-box";
import {
  RESEND_AFTER_MS,
  confirm,
  decide,
  hashToken,
  parseForm,
  remove,
  signUp,
  type Answers,
  type Deps,
  type Repo,
  type Signup,
} from "./early-access";
import type { Mail } from "./emails";

/** The Postgres repo's rules, in memory. */
function memRepo() {
  const rows = new Map<string, Signup & { hash: string; submissions: number }>();
  let id = 0;
  const repo: Repo = {
    async register(a: Answers, t, now) {
      const row = rows.get(a.email) ?? null;
      const what = decide(row, now);
      if (what === "new") rows.set(a.email, { ...a, id: ++id, status: "pending", confirmSentAt: now, createdAt: now, hash: t.hash, submissions: 1 });
      else {
        row!.submissions++;
        if (row!.status === "pending") Object.assign(row!, { hosting: a.hosting ?? row!.hosting, note: a.note ?? row!.note });
        if (what === "resend") Object.assign(row!, { hash: t.hash, confirmSentAt: now });
      }
      return what;
    },
    async confirm(hash) {
      for (const r of rows.values())
        if (r.hash === hash) {
          const first = r.status === "pending";
          r.status = "confirmed";
          return { signup: r, first };
        }
      return null;
    },
    async remove(hash) {
      for (const [k, r] of rows) if (r.hash === hash) return rows.delete(k);
      return false;
    },
  };
  return { repo, rows };
}

function setup(over: Partial<Deps> = {}) {
  const { repo, rows } = memRepo();
  const sent: Mail[] = [];
  let now = new Date("2026-10-08T12:00:00Z");
  const deps: Deps = {
    repo,
    baseUrl: "https://shiptiffin.com",
    notifyTo: "owner@example.com",
    now: () => now,
    send: async (m) => void sent.push(m),
    ...over,
  };
  return { deps, rows, sent, tick: (ms: number) => (now = new Date(now.getTime() + ms)) };
}

function form(fields: Record<string, string | string[]>): FormData {
  const f = new FormData();
  for (const [k, v] of Object.entries(fields)) for (const x of [v].flat()) f.append(k, x);
  return f;
}

const ok = (fields: Record<string, string | string[]>) => {
  const p = parseForm(form(fields));
  if (!p.ok) throw new Error(JSON.stringify(p.errors));
  return p;
};

const tokenOf = (m: Mail) => m.text.match(/confirm\?t=([A-Za-z0-9_-]{43})/)![1]!;

describe("parseForm", () => {
  test("lower-cases and trims the email, keeps known choices only", () => {
    const p = ok({ email: "  Ada@Example.COM ", projects: "2-5", current: ["vercel", "nope", "vps", "vercel"], hosting: "  a shop  " });
    expect(p.answers).toEqual({ email: "ada@example.com", hosting: "a shop", projects: "2-5", currentHosts: ["vercel", "vps"], note: null });
    expect(p.bot).toBe(false);
  });
  test("refuses a missing or malformed email and over-long answers", () => {
    expect(parseForm(form({ email: "" }))).toEqual({ ok: false, errors: { email: "Enter your email address." } });
    for (const bad of ["ada", "ada@", "ada@example", "a b@example.com", "ada@example.com\nBcc: x@y.z"])
      expect(parseForm(form({ email: bad })).ok).toBe(false);
    const r = parseForm(form({ email: "ada@example.com", note: "x".repeat(1001) }));
    expect(r.ok).toBe(false);
  });
  test("an unknown project count is dropped, the honeypot marks a bot", () => {
    const p = ok({ email: "ada@example.com", projects: "lots", website: "http://spam" });
    expect(p.answers.projects).toBeNull();
    expect(p.bot).toBe(true);
  });
});

describe("signUp", () => {
  test("a new address is stored and gets a confirmation with confirm and remove links", async () => {
    const s = setup();
    const r = await signUp(ok({ email: "ada@example.com", hosting: "a shop" }), s.deps);
    expect(r).toEqual({ kind: "added", mailed: true });
    expect(s.sent).toHaveLength(1);
    const m = s.sent[0]!;
    expect(m.to).toBe("ada@example.com");
    const t = tokenOf(m);
    expect(s.rows.get("ada@example.com")!.hash).toBe(hashToken(t));
    expect(m.text).toContain(`https://shiptiffin.com/early-access/remove?t=${t}`);
    expect(m.html).toContain(`https://shiptiffin.com/early-access/confirm?t=${t}`);
    expect(m.headers!["List-Unsubscribe"]).toBe(`<https://shiptiffin.com/api/early-access/remove?t=${t}>`);
    expect(m.headers!["List-Unsubscribe-Post"]).toBe("List-Unsubscribe=One-Click");
  });

  test("the same address again: quiet for 10 minutes, then a fresh link; the old one stops working", async () => {
    const s = setup();
    await signUp(ok({ email: "ada@example.com" }), s.deps);
    const first = tokenOf(s.sent[0]!);
    expect(await signUp(ok({ email: "ADA@example.com" }), s.deps)).toEqual({ kind: "quiet", mailed: false });
    expect(s.sent).toHaveLength(1);
    s.tick(RESEND_AFTER_MS);
    expect(await signUp(ok({ email: "ada@example.com" }), s.deps)).toEqual({ kind: "resent", mailed: true });
    expect(s.rows.size).toBe(1);
    expect(s.rows.get("ada@example.com")!.submissions).toBe(3);
    expect(await confirm(first, s.deps)).toBe("invalid");
    expect(await confirm(tokenOf(s.sent[1]!), s.deps)).toBe("confirmed");
  });

  test("a confirmed address gets no more mail", async () => {
    const s = setup({ notifyTo: null });
    await signUp(ok({ email: "ada@example.com" }), s.deps);
    await confirm(tokenOf(s.sent[0]!), s.deps);
    s.tick(RESEND_AFTER_MS * 10);
    expect(await signUp(ok({ email: "ada@example.com" }), s.deps)).toEqual({ kind: "quiet", mailed: false });
    expect(s.sent).toHaveLength(1);
  });

  test("a bot is not stored or mailed", async () => {
    const s = setup();
    expect(await signUp(ok({ email: "bot@example.com", website: "x" }), s.deps)).toEqual({ kind: "bot", mailed: false });
    expect(s.rows.size).toBe(0);
    expect(s.sent).toHaveLength(0);
  });

  test("a mail failure keeps the sign-up", async () => {
    const logs: string[] = [];
    const s = setup({ send: async () => Promise.reject(new Error("no relay")), log: (m) => void logs.push(m) });
    expect(await signUp(ok({ email: "ada@example.com" }), s.deps)).toEqual({ kind: "added", mailed: false });
    expect(s.rows.size).toBe(1);
    expect(logs).toEqual(["early access: confirmation email not sent"]);
  });
});

describe("confirm and remove", () => {
  test("confirming tells the owner once, with the answers and a reply-to", async () => {
    const s = setup();
    await signUp(ok({ email: "ada@example.com", hosting: "a <b>shop</b>", projects: "6-20", current: ["fly", "vps"] }), s.deps);
    const t = tokenOf(s.sent[0]!);
    expect(await confirm(t, s.deps)).toBe("confirmed");
    expect(await confirm(t, s.deps)).toBe("already");
    expect(s.sent).toHaveLength(2);
    const owner = s.sent[1]!;
    expect(owner.to).toBe("owner@example.com");
    expect(owner.replyTo).toBe("ada@example.com");
    expect(owner.text).toContain("Projects: 6–20");
    expect(owner.text).toContain("Uses today: Fly, A VPS");
    expect(owner.html).toBeUndefined();
  });

  test("no owner address: no note", async () => {
    const s = setup({ notifyTo: null });
    await signUp(ok({ email: "ada@example.com" }), s.deps);
    await confirm(tokenOf(s.sent[0]!), s.deps);
    expect(s.sent).toHaveLength(1);
  });

  test("bad tokens are refused without touching the store", async () => {
    const s = setup();
    for (const t of ["", "x", "a".repeat(44), "../../etc/passwd"]) expect(await confirm(t, s.deps)).toBe("invalid");
    expect(await remove("nope", s.deps)).toBe(false);
  });

  test("remove deletes the row; the link then does nothing", async () => {
    const s = setup();
    await signUp(ok({ email: "ada@example.com" }), s.deps);
    const t = tokenOf(s.sent[0]!);
    expect(await remove(t, s.deps)).toBe(true);
    expect(s.rows.size).toBe(0);
    expect(await remove(t, s.deps)).toBe(false);
    expect(await confirm(t, s.deps)).toBe("invalid");
  });
});

test("the HTML email escapes what it shows", async () => {
  const s = setup();
  await signUp(ok({ email: "ada@example.com" }), s.deps);
  expect(s.sent[0]!.html).not.toContain("<script");
  expect(s.sent[0]!.html).toContain("Confirm my email");
});

test("rate limiter: five per window per key", () => {
  const allow = rateLimiter(5, 1000);
  for (let i = 0; i < 5; i++) expect(allow("1.2.3.4", 0)).toBe(true);
  expect(allow("1.2.3.4", 500)).toBe(false);
  expect(allow("5.6.7.8", 500)).toBe(true);
  expect(allow("1.2.3.4", 1001)).toBe(true);
});
