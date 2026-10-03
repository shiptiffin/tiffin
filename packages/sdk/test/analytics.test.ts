import { describe, expect, test } from "bun:test";
import { clientIP, track } from "../src/analytics";

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
