import { describe, expect, test } from "bun:test";
import { attribution, clientIP, revenue, track } from "../src/analytics";

describe("track", () => {
  test("does nothing without configuration", async () => {
    let called = false;
    const ok = await track("Signup", {}, { endpoint: "", key: "", fetch: (async () => { called = true; return new Response(); }) as unknown as typeof fetch });
    expect(ok).toBe(false);
    expect(called).toBe(false);
  });

  test("sends the event with the request's visitor details", async () => {
    let seen: { url: string; init: RequestInit } | undefined;
    const fake = (async (url: string, init: RequestInit) => {
      seen = { url, init };
      return new Response(JSON.stringify({ ok: true }), { status: 202 });
    }) as unknown as typeof fetch;
    const request = new Request("https://shop.example.com/checkout?x=1", {
      headers: { "x-forwarded-for": "203.0.113.7", "user-agent": "Mozilla/5.0 Test", referer: "https://www.google.com/" },
    });
    const ok = await track("Purchase", { amount: 49, currency: "usd" }, { request, endpoint: "http://127.0.0.1:7091/", key: "tak_x", fetch: fake });
    expect(ok).toBe(true);
    expect(seen!.url).toBe("http://127.0.0.1:7091/track");
    expect((seen!.init.headers as Record<string, string>).authorization).toBe("Bearer tak_x");
    const body = JSON.parse(String(seen!.init.body));
    expect(body).toEqual({
      name: "Purchase",
      props: { amount: 49, currency: "usd" },
      url: "https://shop.example.com/checkout?x=1",
      ip: "203.0.113.7",
      ua: "Mozilla/5.0 Test",
      referrer: "https://www.google.com/",
    });
  });

  test("never throws when the box is unreachable", async () => {
    const fail = (async () => { throw new Error("ECONNREFUSED"); }) as unknown as typeof fetch;
    expect(await track("x", undefined, { endpoint: "http://127.0.0.1:1", key: "k", fetch: fail })).toBe(false);
  });

  test("dropped events report false", async () => {
    const dropped = (async () => new Response(JSON.stringify({ dropped: "bot" }), { status: 202 })) as unknown as typeof fetch;
    expect(await track("x", undefined, { endpoint: "http://h", key: "k", fetch: dropped })).toBe(false);
  });

  test("clientIP takes the edge's entry", () => {
    expect(clientIP(new Request("http://x", { headers: { "x-forwarded-for": "1.1.1.1, 2.2.2.2" } }))).toBe("2.2.2.2");
    expect(clientIP(new Request("http://x"))).toBeUndefined();
  });
});

describe("revenue", () => {
  const ok = (seen: { url?: string; body?: Record<string, unknown> }, reply: unknown = { ok: true, matched: "reference" }, status = 202) =>
    (async (url: string, init: RequestInit) => {
      seen.url = url;
      seen.body = JSON.parse(String(init.body));
      return new Response(JSON.stringify(reply), { status });
    }) as unknown as typeof fetch;

  test("does nothing without configuration", async () => {
    const r = await revenue({ id: "o1", amount: 4900, currency: "usd" }, { endpoint: "", key: "" });
    expect(r.ok).toBe(false);
    expect(r.error).toContain("not configured");
  });

  test("sends the payment with the request's visitor", async () => {
    const seen: { url?: string; body?: Record<string, unknown> } = {};
    const request = new Request("https://shop.example.com/checkout", {
      headers: { "x-forwarded-for": "203.0.113.7", "user-agent": "Mozilla/5.0 Test" },
    });
    const r = await revenue(
      { id: "o1", amount: 4900, currency: "USD", user: "u_1", at: new Date("2026-10-25T12:00:00Z") },
      { request, endpoint: "http://127.0.0.1:7091/", key: "tak_x", fetch: ok(seen) },
    );
    expect(r).toEqual({ ok: true, matched: "reference" });
    expect(seen.url).toBe("http://127.0.0.1:7091/revenue");
    expect(seen.body).toEqual({ id: "o1", amount: 4900, currency: "usd", user: "u_1", at: "2026-10-25T12:00:00.000Z", ip: "203.0.113.7", ua: "Mozilla/5.0 Test" });
  });

  test("refunds name the payment they refund", async () => {
    const seen: { body?: Record<string, unknown> } = {};
    await revenue({ id: "r1", amount: 4900, currency: "eur", refund: "o1" }, { endpoint: "http://h", key: "k", fetch: ok(seen) });
    expect(seen.body).toEqual({ id: "r1", amount: 4900, currency: "eur", refund: "o1" });
  });

  test("refuses amounts that are not whole minor units", async () => {
    let called = false;
    const f = (async () => { called = true; return new Response(); }) as unknown as typeof fetch;
    const r = await revenue({ id: "o1", amount: 19.99, currency: "usd" }, { endpoint: "http://h", key: "k", fetch: f });
    expect(r.ok).toBe(false);
    expect(r.error).toContain("smallest unit");
    expect(called).toBe(false);
  });

  test("ignored while Stripe is connected", async () => {
    const r = await revenue({ id: "o1", amount: 100, currency: "usd" }, { endpoint: "http://h", key: "k", fetch: ok({}, { ignored: "stripe connected" }) });
    expect(r).toEqual({ ok: false, ignored: "stripe connected" });
  });

  test("reports the box's refusal and never throws", async () => {
    const off = await revenue({ id: "o1", amount: 100, currency: "usd" }, { endpoint: "http://h", key: "k", fetch: ok({}, { error: "revenue needs marketing attribution" }, 409) });
    expect(off).toEqual({ ok: false, error: "revenue needs marketing attribution" });
    const fail = (async () => { throw new Error("ECONNREFUSED"); }) as unknown as typeof fetch;
    const down = await revenue({ id: "o1", amount: 100, currency: "usd" }, { endpoint: "http://h", key: "k", fetch: fail });
    expect(down.ok).toBe(false);
    expect(down.error).toContain("ECONNREFUSED");
  });
});

describe("attribution", () => {
  test("asks the collector with the app's key and returns the account's source", async () => {
    let url = "";
    let auth = "";
    const fetch = (async (u: string, init: RequestInit) => {
      url = u;
      auth = new Headers(init.headers).get("authorization") ?? "";
      return Response.json({ attribution: { userId: "u 1", at: "2026-10-25T12:00:00Z", matched: "visit", visit: { source: "Hacker News", utmCampaign: "launch" } } });
    }) as unknown as typeof globalThis.fetch;
    const a = await attribution("u 1", { endpoint: "http://10.0.0.1:7091/", key: "k", fetch });
    expect(url).toBe("http://10.0.0.1:7091/attribution?user=u%201");
    expect(auth).toBe("Bearer k");
    expect(a?.visit.source).toBe("Hacker News");
  });

  test("null when off, unconfigured or unreachable", async () => {
    const off = (async () => Response.json({ attribution: null, off: true })) as unknown as typeof globalThis.fetch;
    expect(await attribution("u", { endpoint: "http://x", key: "k", fetch: off })).toBeNull();
    expect(await attribution("u", { endpoint: "", key: "" })).toBeNull();
    const down = (async () => {
      throw new Error("refused");
    }) as unknown as typeof globalThis.fetch;
    expect(await attribution("u", { endpoint: "http://x", key: "k", fetch: down })).toBeNull();
  });
});
