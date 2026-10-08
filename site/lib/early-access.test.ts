import { describe, expect, test } from "bun:test";
import { rateLimiter } from "./early-access-box";
import {
  DETAILS_FOR_MS,
  RESEND_AFTER_MS,
  addDetails,
  confirm,
  decide,
  errorCode,
  hashToken,
  normaliseLink,
  parseDetails,
  parseForm,
  remove,
  signUp,
  type Answers,
  type Deps,
  type Details,
  type InviteRequest,
  type Repo,
} from "./early-access";
import type { Mail } from "./emails";

/** The Postgres repo's rules, in memory. */
function memRepo() {
  type Row = InviteRequest & { hash: string; submissions: number; dHash: string | null; dExpires: Date | null };
  const rows = new Map<string, Row>();
  let id = 0;
  const merge = (row: Row, d: Partial<Details>) => {
    for (const [k, v] of Object.entries(d) as [keyof Details, unknown][])
      if (Array.isArray(v) ? v.length : v != null) (row as Record<string, unknown>)[k] = v;
  };
  const repo: Repo = {
    async register(a: Answers, t, details, now) {
      const row = rows.get(a.email) ?? null;
      const what = decide(row, now);
      if (what === "new")
        rows.set(a.email, {
          ...a,
          id: ++id,
          status: "requested",
          emailConfirmed: false,
          confirmSentAt: now,
          createdAt: now,
          hash: t.hash,
          submissions: 1,
          dHash: details.hash,
          dExpires: details.expiresAt,
        });
      else {
        row!.submissions++;
        if (!row!.emailConfirmed) {
          merge(row!, a);
          Object.assign(row!, { dHash: details.hash, dExpires: details.expiresAt });
        }
        if (what === "resend") Object.assign(row!, { hash: t.hash, confirmSentAt: now });
      }
      return what;
    },
    async addDetails(hash, d, now) {
      for (const r of rows.values())
        if (r.dHash === hash && r.dExpires! > now) {
          merge(r, d);
          r.dHash = null;
          return true;
        }
      return false;
    },
    async confirm(hash) {
      for (const r of rows.values())
        if (r.hash === hash) {
          const first = !r.emailConfirmed;
          r.emailConfirmed = true;
          return { request: r, first };
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

const EMPTY = { hostFirst: null, projects: null, tools: [], spend: null, agents: [], github: null, x: null, linkedin: null, site: null, note: null };

describe("parseForm", () => {
  test("step 1: lower-cases and trims the email, keeps a known role", () => {
    const p = ok({ email: "  Ada@Example.COM ", name: "  Ada   Lovelace ", role: "indie" });
    expect(p.answers).toEqual({ email: "ada@example.com", name: "Ada Lovelace", role: "indie", ...EMPTY });
    expect(p.bot).toBe(false);
  });
  test("the whole form at once (no JavaScript): every answer, known choices only", () => {
    const p = ok({
      email: "ada@example.com",
      role: "wizard",
      projects: "2-5",
      tools: ["vercel", "nope", "supabase", "vercel"],
      spend: "25-100",
      agents: ["claude-code", "none", "cursor"],
      hostFirst: "  a shop  ",
      github: "@ada",
      x: "https://twitter.com/ada_l?s=20",
      linkedin: "www.linkedin.com/in/ada-lovelace/",
      site: "ada.dev",
      note: "hi",
    });
    expect(p.answers).toEqual({
      email: "ada@example.com",
      name: null,
      role: null,
      hostFirst: "a shop",
      projects: "2-5",
      tools: ["vercel", "supabase"],
      spend: "25-100",
      agents: ["claude-code", "cursor"],
      github: "https://github.com/ada",
      x: "https://x.com/ada_l",
      linkedin: "https://www.linkedin.com/in/ada-lovelace",
      site: "https://ada.dev",
      note: "hi",
    });
  });
  test("\"Not yet\" on its own is kept", () => {
    expect(ok({ email: "ada@example.com", agents: "none" }).answers.agents).toEqual(["none"]);
  });
  test("refuses a missing or malformed email and over-long answers", () => {
    expect(parseForm(form({ email: "" }))).toEqual({ ok: false, errors: { email: "Enter your email address." } });
    for (const bad of ["ada", "ada@", "ada@example", "a b@example.com", "ada@example.com\nBcc: x@y.z"])
      expect(parseForm(form({ email: bad })).ok).toBe(false);
    const r = parseForm(form({ email: "ada@example.com", note: "x".repeat(1001) }));
    expect(r.ok).toBe(false);
    if (!r.ok) expect(errorCode(r.errors)).toBe("long");
    const n = parseForm(form({ email: "ada@example.com", name: "x".repeat(81) }));
    expect(n.ok ? null : n.errors.name).toContain("under 80");
  });
  test("a bad link names the field and says what to enter", () => {
    const r = parseForm(form({ email: "ada@example.com", github: "not a user!", site: "javascript:alert(1)" }));
    expect(r.ok).toBe(false);
    if (r.ok) return;
    expect(r.errors.github).toContain("GitHub username");
    expect(r.errors.site).toContain("web address");
    expect(errorCode(r.errors)).toBe("invalid");
  });
  test("an unknown project count is dropped, the honeypot marks a bot", () => {
    const p = ok({ email: "ada@example.com", projects: "lots", url: "http://spam" });
    expect(p.answers.projects).toBeNull();
    expect(p.bot).toBe(true);
  });
});

describe("normaliseLink", () => {
  const cases: [Parameters<typeof normaliseLink>[0], string, string | null][] = [
    ["github", "ada", "https://github.com/ada"],
    ["github", "https://github.com/ada/tiffin", "https://github.com/ada"],
    ["github", "github.com/-ada", null],
    ["github", "https://gitlab.com/ada", null],
    ["x", "@ada_l", "https://x.com/ada_l"],
    ["x", "x.com/ada", "https://x.com/ada"],
    ["x", "@this-is-not-valid", null],
    ["linkedin", "https://linkedin.com/in/ada", "https://www.linkedin.com/in/ada"],
    ["linkedin", "linkedin.com/company/shiptiffin/about", "https://www.linkedin.com/company/shiptiffin"],
    ["linkedin", "ada", null],
    ["site", "ada.dev", "https://ada.dev"],
    ["site", "http://ada.dev/blog/", "http://ada.dev/blog/"],
    ["site", "https://user:pw@ada.dev", null],
    ["site", "localhost:3000", null],
    ["site", "ftp://ada.dev", null],
    ["site", "", ""],
  ];
  for (const [kind, raw, want] of cases) test(`${kind}: ${raw || "(empty)"}`, () => expect(normaliseLink(kind, raw)).toBe(want));
});

describe("signUp", () => {
  test("a new address is stored and gets a confirmation with confirm and remove links", async () => {
    const s = setup();
    const r = await signUp(ok({ email: "ada@example.com", name: "Ada Lovelace", hostFirst: "a shop" }), s.deps);
    expect(r).toMatchObject({ kind: "added", mailed: true });
    expect(s.sent).toHaveLength(1);
    const m = s.sent[0]!;
    expect(m.to).toBe("ada@example.com");
    const t = tokenOf(m);
    expect(s.rows.get("ada@example.com")!.hash).toBe(hashToken(t));
    expect(m.text).toContain(`https://shiptiffin.com/early-access/remove?t=${t}`);
    expect(m.html).toContain(`https://shiptiffin.com/early-access/confirm?t=${t}`);
    expect(m.headers!["List-Unsubscribe"]).toBe(`<https://shiptiffin.com/api/early-access/remove?t=${t}>`);
    expect(m.headers!["List-Unsubscribe-Post"]).toBe("List-Unsubscribe=One-Click");
    expect(m.text).toStartWith("Hello Ada,");
    expect(m.text).toContain("25% off your first year");
  });

  test("the same address again: quiet for 10 minutes, then a fresh link; the old one stops working", async () => {
    const s = setup();
    await signUp(ok({ email: "ada@example.com" }), s.deps);
    const first = tokenOf(s.sent[0]!);
    expect(await signUp(ok({ email: "ADA@example.com" }), s.deps)).toMatchObject({ kind: "quiet", mailed: false });
    expect(s.sent).toHaveLength(1);
    s.tick(RESEND_AFTER_MS);
    expect(await signUp(ok({ email: "ada@example.com" }), s.deps)).toMatchObject({ kind: "resent", mailed: true });
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
    expect(await signUp(ok({ email: "ada@example.com" }), s.deps)).toMatchObject({ kind: "quiet", mailed: false });
    expect(s.sent).toHaveLength(1);
  });

  test("a bot is not stored or mailed", async () => {
    const s = setup();
    const r = await signUp(ok({ email: "bot@example.com", url: "x" }), s.deps);
    expect(r).toMatchObject({ kind: "bot", mailed: false });
    expect(r.details).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(s.rows.size).toBe(0);
    expect(s.sent).toHaveLength(0);
  });

  test("a mail failure keeps the sign-up", async () => {
    const logs: string[] = [];
    const s = setup({ send: async () => Promise.reject(new Error("no relay")), log: (m) => void logs.push(m) });
    expect(await signUp(ok({ email: "ada@example.com" }), s.deps)).toMatchObject({ kind: "added", mailed: false });
    expect(s.rows.size).toBe(1);
    expect(logs).toEqual(["invite request: confirmation email not sent"]);
  });
});

describe("confirm and remove", () => {
  test("confirming tells the owner once, with the answers and a reply-to", async () => {
    const s = setup();
    await signUp(
      ok({ email: "ada@example.com", name: "Ada", role: "agency", hostFirst: "a <b>shop</b>", projects: "6-20", tools: ["fly", "vps"], github: "ada" }),
      s.deps,
    );
    const t = tokenOf(s.sent[0]!);
    expect(await confirm(t, s.deps)).toBe("confirmed");
    expect(await confirm(t, s.deps)).toBe("already");
    expect(s.sent).toHaveLength(2);
    const owner = s.sent[1]!;
    expect(owner.to).toBe("owner@example.com");
    expect(owner.replyTo).toBe("ada@example.com");
    expect(owner.text).toContain("Projects: 6–20");
    expect(owner.text).toContain("Uses today: Fly, A VPS");
    expect(owner.text).toContain("Builds: Agency or freelancer");
    expect(owner.text).toContain("GitHub: https://github.com/ada");
    expect(owner.subject).toBe("Invite request: Ada");
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

describe("step 2: tell us more", () => {
  const details = (fields: Record<string, string | string[]>) => {
    const p = parseDetails(form(fields));
    if (!p.ok) throw new Error(JSON.stringify(p.errors));
    return p;
  };

  test("adds to the request step 1 made, once", async () => {
    const s = setup();
    const r = await signUp(ok({ email: "ada@example.com", role: "indie" }), s.deps);
    expect(await addDetails(r.details, details({ projects: "2-5", tools: ["vercel", "supabase"], spend: "25-100", agents: "claude-code", x: "@ada" }), s.deps)).toBe("saved");
    const row = s.rows.get("ada@example.com")!;
    expect(row).toMatchObject({ role: "indie", projects: "2-5", tools: ["vercel", "supabase"], spend: "25-100", agents: ["claude-code"], x: "https://x.com/ada" });
    expect(await addDetails(r.details, details({ note: "again" }), s.deps)).toBe("dropped");
    expect(row.note).toBeNull();
  });

  test("the token works for an hour", async () => {
    const s = setup();
    const r = await signUp(ok({ email: "ada@example.com" }), s.deps);
    s.tick(DETAILS_FOR_MS);
    expect(await addDetails(r.details, details({ note: "late" }), s.deps)).toBe("dropped");
  });

  test("an address confirmed before can't have its answers changed by someone else", async () => {
    const s = setup();
    await signUp(ok({ email: "ada@example.com", hostFirst: "a shop" }), s.deps);
    await confirm(tokenOf(s.sent[0]!), s.deps);
    const r = await signUp(ok({ email: "ada@example.com", hostFirst: "spam" }), s.deps);
    expect(r.details).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(await addDetails(r.details, details({ note: "spam" }), s.deps)).toBe("dropped");
    expect(s.rows.get("ada@example.com")).toMatchObject({ hostFirst: "a shop", note: null });
  });

  test("bad links are refused before anything is stored; junk tokens do nothing", async () => {
    const p = parseDetails(form({ linkedin: "ada" }));
    expect(p.ok).toBe(false);
    const s = setup();
    expect(await addDetails("nope", details({}), s.deps)).toBe("dropped");
  });
});
