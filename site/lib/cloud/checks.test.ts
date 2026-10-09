import { afterEach, describe, expect, test } from "bun:test";
import { buildOptions, checkToken, suggest, type Call } from "./hetzner";
import { decide, fromBox, type MonitorBox, type Warning } from "./monitor";
import { nameProblem, safeNext } from "./names";
import { isAdmin, missingSecrets } from "./config";
import { handoffAsk, HANDOFF_PENDING_SECONDS, HANDOFF_SOON_SECONDS, heartbeatDecision, parseSignin, reusableCheckout } from "./actions";
import { backoff, drain, MAX_ATTEMPTS, type OutboxRow, type OutboxStore } from "./outbox";

const price = (net: number) => ({ net: net.toFixed(4), gross: (net * 1.19).toFixed(4) });
const st = (name: string, arch: string, locs: Record<string, boolean>, net: number) => ({
  name,
  cores: 2,
  memory: 4,
  disk: 40,
  architecture: arch,
  locations: Object.entries(locs).map(([n, available]) => ({ name: n, available })),
  prices: Object.keys(locs).map((location) => ({ location, price_monthly: price(net) })),
});
const TYPES = [
  st("cx23", "x86", { fsn1: false, nbg1: true, hel1: true }, 5.49),
  st("cax11", "arm", { fsn1: true, nbg1: true, hel1: true }, 5.49),
  st("cx33", "x86", { fsn1: true, nbg1: true }, 9.99),
  st("cpx22", "x86", { ash: true, hil: false, fsn1: true }, 19.49),
  st("ccx13", "x86", { fsn1: true }, 42.99),
];
const LOCS = ["fsn1", "nbg1", "hel1", "ash", "hil"].map((name) => ({ name, city: name.toUpperCase(), country: "DE" }));
const PRICING = {
  currency: "EUR",
  vat_rate: "19.000000",
  volume: { price_per_gb_month: price(0.0572) },
  primary_ips: [{ type: "ipv4", prices: ["fsn1", "nbg1", "hel1", "ash", "hil"].map((location) => ({ location, price_monthly: price(0.5) })) }],
};

describe("Hetzner options", () => {
  test("only our sizes, where they're sold, with stock and the full monthly price", () => {
    const o = buildOptions(TYPES, LOCS, PRICING);
    expect(o.some((x) => x.serverType === "ccx13")).toBe(false);
    expect(o.some((x) => x.serverType === "cpx22" && x.region === "eu")).toBe(false);
    const cx = o.find((x) => x.serverType === "cx23" && x.location === "fsn1")!;
    expect(cx.available).toBe(false);
    expect(cx.monthlyNet).toBe(Math.round((5.49 + 0.5 + 0.0572 * 40) * 100) / 100);
    expect(o.find((x) => x.serverType === "cpx22" && x.location === "hil")!.available).toBe(false);
    expect(o.find((x) => x.serverType === "cax11")!.arch).toBe("arm64");
  });
  test("suggests cx23 where it is in stock, then the fallbacks", () => {
    const o = buildOptions(TYPES, LOCS, PRICING);
    expect(suggest(o)).toMatchObject({ serverType: "cx23", location: "nbg1" });
    const noCx = buildOptions(TYPES.filter((t) => t.name !== "cx23"), LOCS, PRICING);
    expect(suggest(noCx)).toMatchObject({ serverType: "cax11", location: "fsn1" });
  });
});

describe("checkToken", () => {
  const real = globalThis.fetch;
  afterEach(() => {
    globalThis.fetch = real;
  });
  const fake = (answers: Record<string, [number, unknown]>) => {
    const seen: string[] = [];
    globalThis.fetch = (async (url: string, init?: RequestInit) => {
      const path = new URL(url).pathname.replace("/v1", "") + new URL(url).search;
      const key = `${init?.method ?? "GET"} ${path}`;
      seen.push(key);
      const [status, body] = answers[key] ?? [404, {}];
      return new Response(JSON.stringify(body), { status });
    }) as typeof fetch;
    return seen;
  };
  const token = "A".repeat(64);

  test("a good read & write key: options, and every call recorded without the token", async () => {
    const seen = fake({
      "GET /server_types?per_page=50": [200, { server_types: TYPES }],
      "GET /locations": [200, { locations: LOCS }],
      "GET /pricing": [200, { pricing: PRICING }],
      "GET /servers?per_page=1": [200, { servers: [], meta: { pagination: { total_entries: 2 } } }],
      "POST /ssh_keys": [400, { error: { code: "invalid_input" } }],
    });
    const calls: Call[] = [];
    const r = await checkToken(token, (c) => void calls.push(c), "https://api.example/v1");
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.servers).toBe(2);
    expect(r.suggested?.serverType).toBe("cx23");
    expect(seen.filter((k) => !k.startsWith("GET /latest"))).toHaveLength(5); // plus the exchange rate, not a Hetzner call
    expect(calls).toHaveLength(5);
    expect(calls.map((c) => c.path)).toContain("/v1/ssh_keys");
    expect(JSON.stringify(calls)).not.toContain(token);
  });

  test("a read-only key is refused with how to fix it", async () => {
    fake({
      "GET /server_types?per_page=50": [200, { server_types: TYPES }],
      "GET /locations": [200, { locations: LOCS }],
      "GET /pricing": [200, { pricing: PRICING }],
      "GET /servers?per_page=1": [200, { servers: [] }],
      "POST /ssh_keys": [403, { error: { code: "forbidden" } }],
    });
    const r = await checkToken(token, () => {}, "https://api.example/v1");
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.message).toContain("Read & Write");
  });

  test("a wrong key, and something that isn't a key", async () => {
    fake({ "GET /server_types?per_page=50": [401, { error: { code: "unauthorized" } }] });
    const r = await checkToken(token, () => {}, "https://api.example/v1");
    expect(r.ok).toBe(false);
    const seen = fake({});
    expect((await checkToken("not a token!", () => {})).ok).toBe(false);
    expect(seen).toHaveLength(0); // never sent anywhere
  });
});

describe("names", () => {
  test("same rules as the worker", () => {
    for (const ok of ["shop", "my-shop", "a1b", "acme-2026", "pineapple"]) expect(nameProblem(ok)).toBeNull();
    for (const bad of ["", "ab", "Shop", "1shop", "shop-", "-shop", "my--shop", "xn--shop", "sh_op", "shop.app", "www", "admin", "dashboard", "shiptiffin", "paypal-login", "my-shiptiffin", "google", "a".repeat(31)])
      expect(nameProblem(bad)).not.toBeNull();
  });
  test("sign-in only returns to this site", () => {
    expect(safeNext("/start")).toBe("/start");
    expect(safeNext("//evil.example")).toBe("/account");
    expect(safeNext("https://evil.example")).toBe("/account");
    expect(safeNext(undefined)).toBe("/account");
  });
});

describe("monitor", () => {
  const now = new Date("2026-10-08T12:00:00Z");
  const base: MonitorBox = {
    id: "box_1",
    name: "shop",
    email: "sam@example.com",
    status: "active",
    plan_status: "active",
    first_paid_at: new Date("2026-10-01T00:00:00Z"),
    extras_paused_at: null,
    dns_state: "live",
    generation: 1,
    last_heartbeat_at: new Date(now.getTime() - 3_600_000),
    ready_at: new Date("2026-10-01T00:10:00Z"),
    health_failures: 0,
    down_alerted_at: null,
    heartbeat_alerted_at: null,
    created_at: new Date("2026-10-01T00:00:00Z"),
    warning: null,
  };
  test("probes a live, paid box; emails after three failures in a row, once", () => {
    expect(decide(base, now).probe).toBe(true);
    expect(decide({ ...base, health_failures: 1 }, now, false).emails).toEqual([]);
    const third = decide({ ...base, health_failures: 2 }, now, false);
    expect(third.emails).toEqual(["down"]);
    expect(third.patch.down_alerted_at).toEqual(now);
    expect(decide({ ...base, health_failures: 5, down_alerted_at: now }, now, false).emails).toEqual([]);
    const back = decide({ ...base, health_failures: 5, down_alerted_at: now }, now, true);
    expect(back.emails).toEqual(["up"]);
    expect(back.patch).toMatchObject({ health_failures: 0, down_alerted_at: null });
  });
  test("a silent box (no check-in for a day and a half) emails once", () => {
    const silent = { ...base, last_heartbeat_at: new Date(now.getTime() - 37 * 3_600_000) };
    expect(decide(silent, now, true).emails).toEqual(["silent"]);
    expect(decide({ ...silent, heartbeat_alerted_at: now }, now, true).emails).toEqual([]);
  });
  test("72 hours without a check-in parks the address, however well its IP answers HTTPS", () => {
    const quiet = { ...base, last_heartbeat_at: new Date(now.getTime() - 73 * 3_600_000) };
    expect(decide(quiet, now, true)).toMatchObject({ removeDns: { reason: "parked", gen: 1 }, emails: ["parked"] });
    expect(decide({ ...quiet, health_failures: 0, down_alerted_at: null }, now)).toMatchObject({ removeDns: { reason: "parked" } });
    // Unpaid boxes too: parking is about the IP, not billing.
    expect(decide({ ...quiet, plan_status: "canceled", extras_paused_at: new Date(now.getTime() - 86_400_000) }, now).removeDns?.reason).toBe("parked");
    expect(decide({ ...quiet, dns_state: "parked" }, now).removeDns).toBeNull();
    expect(decide({ ...base, last_heartbeat_at: new Date(now.getTime() - 71 * 3_600_000) }, now, false).removeDns).toBeNull();
    // A box that never checked in counts from when it became ready.
    expect(decide({ ...base, last_heartbeat_at: null, ready_at: new Date(now.getTime() - 80 * 3_600_000) }, now).removeDns?.reason).toBe("parked");
  });
  test("unpaid: no monitoring; the address goes after 30 days, and never before a warning got through", () => {
    const day = 86_400_000;
    const sent = (daysAgo: number): Warning => ({ status: "done", doneAt: new Date(now.getTime() - daysAgo * day) });
    const paused = (days: number, warning: Warning | null = null) => ({ ...base, plan_status: "unpaid", extras_paused_at: new Date(now.getTime() - days * day), warning });
    expect(decide(paused(1), now)).toMatchObject({ probe: false, emails: [], removeDns: null });
    expect(decide(paused(24), now).emails).toEqual(["dns_soon"]);
    expect(decide(paused(24, { status: "queued", doneAt: null }), now).emails).toEqual([]);
    expect(decide(paused(31, { status: "queued", doneAt: null }), now)).toMatchObject({ removeDns: null, emails: [] }); // queued, not sent yet: wait
    expect(decide(paused(31), now)).toMatchObject({ removeDns: null, emails: ["dns_soon"] }); // never warned: warn first
    const gone = decide(paused(31, sent(8)), now);
    expect(gone).toMatchObject({ removeDns: { reason: "grace", gen: 1 }, emails: ["dns_removed"] });
    expect(gone.removeDns!.pausedAt).toEqual(paused(31).extras_paused_at);
    expect(decide({ ...paused(31, sent(8)), dns_state: "removed" }, now)).toMatchObject({ removeDns: null, emails: [] });
    // A half-published address counts as published.
    expect(decide({ ...paused(31, sent(8)), dns_state: "pending" }, now).removeDns?.reason).toBe("grace");
  });
  test("a warning that failed doesn't count: the address stays, and the warning is sent again a day later", () => {
    const day = 86_400_000;
    const paused = (days: number, warning: Warning | null) => ({ ...base, plan_status: "unpaid", extras_paused_at: new Date(now.getTime() - days * day), warning });
    const failed = (daysAgo: number): Warning => ({ status: "failed", doneAt: new Date(now.getTime() - daysAgo * day) });
    expect(decide(paused(40, failed(0.5)), now)).toMatchObject({ removeDns: null, emails: [], retryWarning: false });
    expect(decide(paused(40, failed(2)), now)).toMatchObject({ removeDns: null, emails: [], retryWarning: true });
    // Sent late (a day before the grace ends): seven days' notice still, not one.
    const late: Warning = { status: "done", doneAt: new Date(now.getTime() - 1 * day) };
    expect(decide(paused(31, late), now).removeDns).toBeNull();
    expect(decide(paused(38, { status: "done", doneAt: new Date(now.getTime() - 7 * day) }), now).removeDns?.reason).toBe("grace");
  });
  test("released, deleted, unnamed or unpaid-from-the-start boxes are left alone", () => {
    const silent = new Date(now.getTime() - 30 * 86_400_000);
    for (const status of ["released", "deleting", "deleted"]) {
      // Not probed, never parked or warned, whatever its last check-in.
      expect(decide({ ...base, status, last_heartbeat_at: silent }, now)).toMatchObject({ probe: false, emails: [], removeDns: null });
    }
    expect(decide({ ...base, plan_status: "past_due", first_paid_at: null }, now).probe).toBe(false);
  });
  test("a box waiting for its certificate isn't probed", () => {
    expect(decide({ ...base, status: "cert_pending" }, now).probe).toBe(false);
  });
});

describe("check-ins", () => {
  const box = { status: "active", plan_status: "active", first_paid_at: new Date(), extras_paused_at: null, generation: 2, ipv4: "203.0.113.5", ipv6: "2001:db8:1:2::1", dns_state: "live", killed_at: null } as const;
  test("a box being deleted, deleted or released isn't managed: its check-ins count for nothing", () => {
    for (const status of ["deleting", "deleted", "released"]) {
      expect(heartbeatDecision({ ...box, status } as any, { gen: 2 }, "203.0.113.5")).toEqual({ answer: { managed: false, active: false, updates: true }, counts: false, restore: false });
    }
  });
  test("count only from the box's own address, with the current setup's licence", () => {
    expect(heartbeatDecision(box as any, { gen: 2 }, "203.0.113.5")).toMatchObject({ counts: true, answer: { managed: true, active: true } });
    expect(heartbeatDecision(box as any, { gen: 2 }, "2001:db8:1:2:abcd::9").counts).toBe(true); // any address of its /64
    expect(heartbeatDecision(box as any, { gen: 2 }, "::ffff:203.0.113.5").counts).toBe(true);
    const elsewhere = heartbeatDecision(box as any, { gen: 2 }, "198.51.100.66");
    expect(elsewhere).toMatchObject({ counts: false, restore: false, answer: { managed: true } });
    expect(elsewhere.refused).toContain("198.51.100.66");
    expect(heartbeatDecision(box as any, { gen: 2 }, "2001:db8:1:3::1").counts).toBe(false);
    expect(heartbeatDecision(box as any, { gen: 2 }, undefined).counts).toBe(false);
  });
  test("an earlier setup's licence is revoked: unmanaged, never counted", () => {
    const old = heartbeatDecision(box as any, { gen: 1 }, "203.0.113.5");
    expect(old).toMatchObject({ counts: false, restore: false, answer: { managed: false, updates: true } });
    expect(heartbeatDecision(box as any, {}, "203.0.113.5").counts).toBe(false);
  });
  test("a counted check-in brings a parked or removed address back, never a killed one", () => {
    expect(heartbeatDecision({ ...box, dns_state: "parked" } as any, { gen: 2 }, "203.0.113.5").restore).toBe(true);
    expect(heartbeatDecision({ ...box, dns_state: "removed" } as any, { gen: 2 }, "203.0.113.5").restore).toBe(true);
    expect(heartbeatDecision({ ...box, dns_state: "parked", killed_at: new Date() } as any, { gen: 2 }, "203.0.113.5").restore).toBe(false);
    expect(heartbeatDecision({ ...box, dns_state: "parked", plan_status: "canceled" } as any, { gen: 2 }, "203.0.113.5")).toMatchObject({ restore: false, answer: { active: false, updates: false } });
    expect(heartbeatDecision({ ...box, dns_state: "parked" } as any, { gen: 2 }, "198.51.100.66").restore).toBe(false);
  });
  test("a half-published address comes back at a counted check-in too", () => {
    expect(heartbeatDecision({ ...box, dns_state: "pending" } as any, { gen: 2 }, "203.0.113.5").restore).toBe(true);
  });
  test("the hand-off: a fresh sign-in link when it counts from readiness, or when asked; none after the owner signed in", () => {
    const h = 3_600_000;
    const ready = new Date("2026-10-08T12:00:00Z");
    const b = (over: object = {}) => ({ status: "active", handoff_closed_at: null, signin_requested_at: null, signin_expires_at: new Date(ready.getTime() + 24 * h - 5 * 60_000), ready_at: ready, ...over }) as any;
    // Made at setup, ready minutes later: that link is good.
    expect(handoffAsk(b(), "pending")).toEqual({ signin: false, checkInSeconds: HANDOFF_PENDING_SECONDS });
    // The certificate took two hours: a new link, so its 24 hours start at readiness.
    expect(handoffAsk(b({ signin_expires_at: new Date(ready.getTime() + 22 * h) }), "pending")).toEqual({ signin: true, checkInSeconds: HANDOFF_SOON_SECONDS });
    // Setup never got one (its last steps failed): ask.
    expect(handoffAsk(b({ signin_expires_at: null }), "pending").signin).toBe(true);
    // Expired unused: only when the customer asks.
    expect(handoffAsk(b({ signin_expires_at: new Date(ready.getTime() - h), ready_at: new Date(ready.getTime() - 30 * h) }), "pending").signin).toBe(false);
    expect(handoffAsk(b({ signin_requested_at: new Date() }), "pending").signin).toBe(true);
    // Waiting for the certificate: check in soon, no link yet.
    expect(handoffAsk(b({ status: "cert_pending", ready_at: null }), "pending")).toEqual({ signin: false, checkInSeconds: HANDOFF_SOON_SECONDS });
    // The owner signed in (or the customer said they'd sign in on the box), or an older box: nothing.
    expect(handoffAsk(b({ signin_requested_at: new Date() }), "done")).toEqual({ signin: false });
    expect(handoffAsk(b({ signin_requested_at: new Date(), handoff_closed_at: new Date() }), "pending")).toEqual({ signin: false });
    expect(handoffAsk(b({ signin_requested_at: new Date() }), undefined)).toEqual({ signin: false });
  });
  test("a sign-in link from the box: its shape, and the box's 24 hours at most", () => {
    const now = new Date("2026-10-08T12:00:00Z");
    const at = (hours: number) => new Date(now.getTime() + hours * 3_600_000).toISOString();
    expect(parseSignin({ code: "tfl_abcdefghijklmnop2345", expiresAt: at(24) }, now)?.code).toBe("tfl_abcdefghijklmnop2345");
    expect(parseSignin({ code: "tfl_abcdefghijklmnop2345", expiresAt: at(25) }, now)).toBeNull();
    expect(parseSignin({ code: "tfl_abcdefghijklmnop2345", expiresAt: at(-1) }, now)).toBeNull();
    expect(parseSignin({ code: "tfn_ownertoken12345678", expiresAt: at(1) }, now)).toBeNull();
    expect(parseSignin({ code: "tfl_<script>", expiresAt: at(1) }, now)).toBeNull();
    expect(parseSignin("tfl_abcdefghijklmnop2345", now)).toBeNull();
  });
  test("address matching", () => {
    expect(fromBox("203.0.113.5", "203.0.113.5", null)).toBe(true);
    expect(fromBox("203.0.113.50", "203.0.113.5", null)).toBe(false);
    expect(fromBox("2001:0db8:0001:0002:0000:0000:0000:0009", null, "2001:db8:1:2::1")).toBe(true);
    expect(fromBox("not an ip", "203.0.113.5", "2001:db8::1")).toBe(false);
  });
});

describe("admins", () => {
  const env = { CLOUD_ADMIN_USER_IDS: "usr_owner, usr_two" };
  test("an enrolled account id with a verified email, nothing else", () => {
    expect(isAdmin({ id: "usr_owner", emailVerified: true }, env)).toBe(true);
    expect(isAdmin({ id: "usr_owner", emailVerified: false }, env)).toBe(false);
    expect(isAdmin({ id: "usr_other", emailVerified: true }, env)).toBe(false);
    expect(isAdmin(null, env)).toBe(false);
    expect(isAdmin({ id: "usr_owner", emailVerified: true }, {})).toBe(false);
  });
});

describe("checkout", () => {
  const now = new Date("2026-10-08T12:00:00Z");
  test("a stored session is handed out again until it is about to expire", () => {
    expect(reusableCheckout({ checkout_url: "https://checkout.stripe.com/c/1", checkout_expires_at: new Date(now.getTime() + 20 * 60_000) }, now)).toBe(true);
    expect(reusableCheckout({ checkout_url: "https://checkout.stripe.com/c/1", checkout_expires_at: new Date(now.getTime() + 60_000) }, now)).toBe(false);
    expect(reusableCheckout({ checkout_url: null, checkout_expires_at: null }, now)).toBe(false);
  });
});

describe("outbox", () => {
  class MemOutbox implements OutboxStore {
    rows: (OutboxRow & { status: string; next?: Date; error?: string })[] = [];
    async claim(limit: number) {
      return this.rows.filter((r) => r.status === "queued").slice(0, limit).map((r) => ({ ...r }));
    }
    async done(id: number) {
      Object.assign(this.rows.find((r) => r.id === id)!, { status: "done" });
    }
    async retry(id: number, attempts: number, next: Date, error: string) {
      Object.assign(this.rows.find((r) => r.id === id)!, { attempts, next, error });
    }
    async fail(id: number, attempts: number, error: string) {
      Object.assign(this.rows.find((r) => r.id === id)!, { status: "failed", attempts, error });
    }
    async drop(id: number, error: string) {
      Object.assign(this.rows.find((r) => r.id === id)!, { status: "dropped", error });
    }
    async queue(box_id: string, kind: string, key: string, params: Record<string, unknown>) {
      if (this.rows.some((r) => r.box_id === box_id && r.kind === kind && r.key === key)) return false;
      this.rows.push({ ...row(this.rows.length + 100, kind, params), box_id, key });
      return true;
    }
    async requeueStripe() {
      const failed = this.rows.filter((r) => r.status === "failed" && r.kind.startsWith("stripe_"));
      for (const r of failed) r.status = "queued";
      return failed.length;
    }
  }
  const row = (id: number, kind: string, params: Record<string, unknown> = {}) => ({
    id, box_id: "box_1", kind, key: "", params, attempts: 0, email: "sam@example.com", name: "shop",
    last_heartbeat_at: null, extras_paused_at: null, kill_reason: null, stripe_subscription_id: "sub_1", status: "queued", box_status: "active" as string | null,
    created_at: new Date("2026-10-08T12:00:00Z"),
  });
  test("sends, retries with backoff while the mail server is down, gives up after ten tries", async () => {
    const st = new MemOutbox();
    st.rows.push(row(1, "ready"), row(2, "setup_failed", { error: "apt failed" }));
    const sent: string[] = [];
    let up = false;
    const send = async (m: { subject: string; text: string }) => (up ? (sent.push(m.subject + "|" + m.text), { ok: true as const }) : { ok: false as const, error: "connection refused" });
    const now = new Date("2026-10-08T12:00:00Z");
    expect(await drain(st, { send, now })).toEqual({ done: 0, retried: 2, failed: 0, dropped: 0 });
    expect(st.rows[0]).toMatchObject({ status: "queued", attempts: 1, error: "connection refused" });
    expect(st.rows[0]!.next!.getTime()).toBe(backoff(1, now).getTime());
    up = true;
    expect(await drain(st, { send, now })).toEqual({ done: 2, retried: 0, failed: 0, dropped: 0 });
    expect(sent[0]).toContain("ready");
    expect(sent[1]).toContain("apt failed");
    const st2 = new MemOutbox();
    st2.rows.push({ ...row(3, "down"), attempts: MAX_ATTEMPTS - 1 });
    up = false;
    expect(await drain(st2, { send, now })).toEqual({ done: 0, retried: 0, failed: 1, dropped: 0 });
  });
  test("a box being deleted or deleted gets no email about a running box, only its deleted email", async () => {
    for (const box_status of ["deleting", "deleted"]) {
      const st = new MemOutbox();
      const kinds = ["extras_paused", "dns_soon", "dns_removed", "parked", "silent", "down", "payment_failed"];
      kinds.forEach((k, i) => st.rows.push({ ...row(i + 1, k), box_status }));
      st.rows.push({ ...row(20, "deleted", { dataDeleted: false }), box_status: "deleted", first_paid_at: new Date("2026-10-09T00:40:00Z"), founding: true, deleted_at: new Date("2026-10-09T00:57:00Z") });
      st.rows.push({ ...row(21, "refunded"), box_status });
      const sent: string[] = [];
      const send = async (m: { subject: string; text: string }) => (sent.push(m.subject + "|" + m.text), { ok: true as const });
      expect(await drain(st, { send, now: new Date("2026-10-09T01:00:00Z") })).toMatchObject({ done: 2, dropped: kinds.length });
      expect(st.rows.filter((r) => r.status === "dropped").map((r) => r.kind)).toEqual(kinds);
      expect(sent[0]).toStartWith("shop's server is deleted|");
      expect(sent[0]).toContain("last charged $12 on 9 Oct");
      expect(sent[0]).toContain("money-back guarantee");
    }
  });
  test("Stripe actions run through the same retries", async () => {
    const st = new MemOutbox();
    st.rows.push(row(4, "stripe_cancel_refund", { subscription: "sub_2", why: "duplicate" }));
    let tries = 0;
    const stripe = async () => {
      if (++tries === 1) throw new Error("Stripe answered 500");
    };
    expect(await drain(st, { stripe })).toMatchObject({ retried: 1 });
    expect(await drain(st, { stripe })).toMatchObject({ done: 1 });
    expect(backoff(30).getTime() - Date.now()).toBeLessThanOrEqual(6 * 3_600_000 + 1000);
  });
  test("a Stripe cancel and refund is never given up on, and the admin hears once when it's over an hour stuck", async () => {
    const st = new MemOutbox();
    st.rows.push(row(5, "stripe_cancel_refund", { subscription: "sub_dup", why: "duplicate" }));
    const stripe = async () => {
      throw new Error("Stripe answered 500");
    };
    const mail: { to: string; subject: string; text: string }[] = [];
    const send = async (m: { to: string; subject: string; text: string }) => (mail.push(m), { ok: true as const });
    const start = new Date("2026-10-08T12:00:00Z");
    for (let i = 0; i < 3 * MAX_ATTEMPTS; i++) {
      const now = new Date(start.getTime() + i * 10 * 60_000);
      await drain(st, { stripe, send, now, admin: "ops@example.com" });
      const r = st.rows.find((x) => x.id === 5)!;
      expect(r.status).toBe("queued");
      expect(r.next!.getTime() - now.getTime()).toBeLessThanOrEqual(60 * 60_000);
    }
    expect(st.rows.find((x) => x.id === 5)!.attempts).toBe(3 * MAX_ATTEMPTS);
    expect(mail.length).toBe(1);
    expect(mail[0]!.to).toBe("ops@example.com");
    expect(mail[0]!.text).toContain("sub_dup");
    // One given up on by an older release is picked up again.
    Object.assign(st.rows.find((x) => x.id === 5)!, { status: "failed" });
    let ran = 0;
    await drain(st, { stripe: async () => void ran++, send, admin: "ops@example.com" });
    expect(ran).toBe(1);
    expect(st.rows.find((x) => x.id === 5)!.status).toBe("done");
  });
});

test("sign-up stays closed until every secret is there, refuses live Stripe keys, and wants public keys only", () => {
  const all = {
    DATABASE_URL: "postgres://x",
    STRIPE_SECRET_KEY: "sk_test_x",
    STRIPE_WEBHOOK_SECRET: "whsec_x",
    CLOUD_SEAL_PUBLIC: "j0DFrbaPJWJK5bIU6nZ6bslNgp09e14a0bpvPiE4KF8=",
    CLOUD_LICENCE_PUBLIC: "A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg=",
  };
  expect(missingSecrets(all)).toEqual([]);
  expect(missingSecrets({ ...all, STRIPE_SECRET_KEY: undefined })).toEqual(["STRIPE_SECRET_KEY"]);
  expect(missingSecrets({ ...all, STRIPE_SECRET_KEY: "sk_live_x" })[0]).toContain("live");
  expect(missingSecrets({ ...all, CLOUD_SEAL_PUBLIC: "short" })).toEqual(["CLOUD_SEAL_PUBLIC"]);
  expect(missingSecrets({ ...all, CLOUD_LICENCE_PUBLIC: undefined })).toEqual(["CLOUD_LICENCE_PUBLIC"]);
});

test("usdRate: 1 for dollars, the daily rate for euros, null when no rate comes back", async () => {
  const { usdRate } = await import("./hetzner");
  expect(await usdRate("USD")).toBe(1);
  const ok = (async () => new Response(JSON.stringify({ rates: { USD: 1.12 } }))) as unknown as typeof fetch;
  expect(await usdRate("EUR", ok)).toBe(1.12);
  const down = (async () => { throw new Error("offline"); }) as unknown as typeof fetch;
  expect(await usdRate("GBP", down)).toBeNull();
  expect(await usdRate("EUR", down)).toBe(1.12); // kept from before
});
