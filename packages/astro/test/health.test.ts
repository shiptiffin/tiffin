import { describe, expect, test } from "bun:test";
import { createServer } from "node:net";
import { GET } from "../src/health";
import { pgAddress, pgAnswers } from "../src/health-db";
import { shutdownEntrySource } from "../src/shutdown";

describe("health", () => {
  test("200 JSON, no-store", async () => {
    const r = GET();
    expect(r.status).toBe(200);
    expect(r.headers.get("cache-control")).toBe("no-store");
    expect((await r.json()).ok).toBe(true);
  });
  test("DATABASE_URL addresses", () => {
    expect(pgAddress("postgres://u:p@db.internal:6543/x")).toEqual({ host: "db.internal", port: 6543 });
    expect(pgAddress("postgresql://u@localhost/x")).toEqual({ host: "localhost", port: 5432 });
    expect(pgAddress("postgres://u@/x?host=/run/pg")).toEqual({ path: "/run/pg/.s.PGSQL.5432" });
    expect(pgAddress("mysql://x")).toBeUndefined();
    expect(pgAddress("nope")).toBeUndefined();
  });
  test("pgAnswers: SSLRequest answered by 'N' is up; silence or a closed port is down", async () => {
    const pg = createServer((s) => s.once("data", () => s.write("N"))).listen(0, "127.0.0.1");
    const mute = createServer(() => {}).listen(0, "127.0.0.1");
    await new Promise((r) => setTimeout(r, 20));
    const port = (srv: typeof pg) => (srv.address() as { port: number }).port;
    expect(await pgAnswers({ host: "127.0.0.1", port: port(pg) })).toBe(true);
    expect(await pgAnswers({ host: "127.0.0.1", port: port(mute) }, 200)).toBe(false);
    pg.close();
    mute.close();
    expect(await pgAnswers({ host: "127.0.0.1", port: 1 }, 500)).toBe(false);
  });
});

describe("shutdown entry", () => {
  test("imports the adapter entry with autostart off and re-exports it", () => {
    const src = shutdownEntrySource(12);
    expect(src).toStartWith("// Written by @shiptiffin/astro");
    expect(src).toContain('await import("./astro-entry.mjs")');
    expect(src).toContain("const timeoutMs = 12000;");
    for (const e of ["handler", "startServer", "options"]) expect(src).toContain(`export const ${e} = app.${e};`);
  });
});
