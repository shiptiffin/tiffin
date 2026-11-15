import { describe, expect, test } from "bun:test";
import { createSessionDriver, memorySessionDriver } from "../src/session-driver";
import { fakeStore } from "./fake-redis";

describe("Valkey session driver", () => {
  test("round trip under VALKEY_PREFIX + sess:", async () => {
    const { fake, store } = fakeStore("p_shop:");
    const d = createSessionDriver(store, { ttl: 3600 });
    const value = '[["Map",1],[[2,3]],"count",1]'; // a devalue string: must come back as the same string
    await d.setItem("abc", value);
    expect([...fake.data.keys()]).toEqual(["p_shop:sess:abc"]);
    expect(await d.getItem("abc")).toBe(value);
    await d.removeItem("abc");
    expect(await d.getItem("abc")).toBeNull();
  });
  test("the TTL is set on write and renewed on read (sliding)", async () => {
    const { fake, store } = fakeStore();
    const d = createSessionDriver(store, { ttl: 100 });
    await d.setItem("s", "x");
    fake.advance(90_000);
    expect(await d.getItem("s")).toBe("x"); // GETEX renews
    fake.advance(90_000);
    expect(await d.getItem("s")).toBe("x");
    fake.advance(101_000);
    expect(await d.getItem("s")).toBeNull();
    expect(fake.log).toContain("GETEX p_test:sess:s");
  });
  test("ttl 0: no expiry; custom namespace", async () => {
    const { fake, store } = fakeStore();
    const d = createSessionDriver(store, { ttl: 0, namespace: "s2:" });
    await d.setItem("a", "1");
    expect(fake.data.get("p_test:s2:a")?.exp).toBe(0);
    expect(await d.getItem("a")).toBe("1");
    expect(fake.log.at(-1)).toBe("GET p_test:s2:a");
  });
  test("falls back to GET + EXPIRE where GETEX is unknown", async () => {
    const { fake, store } = fakeStore();
    const send = fake.send;
    fake.send = (c, a) => (c.toUpperCase() === "GETEX" ? Promise.reject(new Error("ERR unknown command 'GETEX'")) : send(c, a));
    const d = createSessionDriver(store, { ttl: 50 });
    await d.setItem("a", "1");
    expect(await d.getItem("a")).toBe("1");
    expect(await d.getItem("a")).toBe("1");
    expect(fake.log.filter((l) => l.startsWith("EXPIRE"))).toHaveLength(2);
  });
  test("Valkey errors reach Astro (a session must not silently reset)", async () => {
    const { fake, store } = fakeStore();
    const d = createSessionDriver(store);
    fake.down = true;
    await expect(d.getItem("a")).rejects.toThrow();
  });
  test("memory fallback", async () => {
    const d = memorySessionDriver({ ttl: 10 });
    await d.setItem("a", "1");
    expect(await d.getItem("a")).toBe("1");
    await d.removeItem("a");
    expect(await d.getItem("a")).toBeNull();
  });
});
