import { afterEach, describe, expect, test } from "bun:test";
import { buildOptions, checkToken, suggest, type Call } from "./hetzner";
import { decide, type MonitorBox } from "./monitor";
import { nameProblem, safeNext } from "./names";
import { missingSecrets } from "./config";

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
    expect(seen).toHaveLength(5);
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
    extras_paused_at: null,
    dns_state: "live",
    last_heartbeat_at: new Date(now.getTime() - 3_600_000),
    health_failures: 0,
    down_alerted_at: null,
    heartbeat_alerted_at: null,
    created_at: new Date("2026-10-01T00:00:00Z"),
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
  test("unpaid: no monitoring; the address goes after 30 days, with a warning a week before", () => {
    const paused = (days: number) => ({ ...base, plan_status: "unpaid", extras_paused_at: new Date(now.getTime() - days * 86_400_000) });
    expect(decide(paused(1), now)).toMatchObject({ probe: false, emails: [], removeDns: false });
    expect(decide(paused(24), now).emails).toEqual(["dns_soon"]);
    expect(decide(paused(31), now)).toMatchObject({ removeDns: true, emails: ["dns_removed"] });
    expect(decide({ ...paused(31), dns_state: "removed" }, now)).toMatchObject({ removeDns: false, emails: [] });
  });
  test("a box quiet and down for a week has its address parked, once", () => {
    const gone = { ...base, health_failures: 2000, last_heartbeat_at: new Date(now.getTime() - 8 * 86_400_000), down_alerted_at: new Date(now.getTime() - 8 * 86_400_000) };
    expect(decide(gone, now, false)).toMatchObject({ removeDns: true, emails: ["parked"] });
    expect(decide({ ...gone, dns_state: "removed" }, now)).toMatchObject({ removeDns: false, probe: false });
    expect(decide({ ...gone, last_heartbeat_at: new Date(now.getTime() - 3_600_000) }, now, false).removeDns).toBe(false);
  });
  test("released or unnamed boxes are left alone", () => {
    expect(decide({ ...base, status: "released" }, now)).toMatchObject({ probe: false, emails: [] });
  });
});

test("sign-up stays closed until every secret is there, and refuses live Stripe keys", () => {
  const all = {
    DATABASE_URL: "postgres://x",
    STRIPE_SECRET_KEY: "sk_test_x",
    STRIPE_WEBHOOK_SECRET: "whsec_x",
    CLOUD_KEK: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
    CLOUD_LICENCE_KEY: "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
  };
  expect(missingSecrets(all)).toEqual([]);
  expect(missingSecrets({ ...all, STRIPE_SECRET_KEY: undefined })).toEqual(["STRIPE_SECRET_KEY"]);
  expect(missingSecrets({ ...all, STRIPE_SECRET_KEY: "sk_live_x" })[0]).toContain("live");
  expect(missingSecrets({ ...all, CLOUD_KEK: "short" })).toEqual(["CLOUD_KEK"]);
});
