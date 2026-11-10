/**
 * `@shiptiffin/astro/health-db`: the health route with `health: { database: true }`.
 * Also checks that Postgres at DATABASE_URL answers, without a database
 * driver: it opens a connection and sends Postgres's SSLRequest (8 bytes),
 * which any Postgres answers with one byte ("S" or "N") before authentication.
 * 503 when it doesn't answer within 2 s. No query runs and no credentials are sent.
 */
import { connect, type Socket } from "node:net";
import { healthBody, headers } from "./health";

export const prerender = false;

/** Where DATABASE_URL points: a TCP host and port, or a Unix socket path. */
export function pgAddress(dsn: string): { host: string; port: number } | { path: string } | undefined {
  let u: URL;
  // "postgres://user@/db?host=/socket" (no host) is valid for libpq but not for URL.
  const NOHOST = "nohost.invalid";
  try {
    u = new URL(dsn.replace(/^(postgres(?:ql)?:\/\/(?:[^/@]*@)?)(?=[/?]|$)/, `$1${NOHOST}`));
  } catch {
    return undefined;
  }
  if (!/^postgres(ql)?:$/.test(u.protocol)) return undefined;
  const port = Number(u.port || u.searchParams.get("port") || 5432);
  const qhost = u.searchParams.get("host");
  const hostname = u.hostname === NOHOST ? "" : decodeURIComponent(u.hostname);
  const host = qhost || hostname;
  if (host.startsWith("/")) return { path: `${host.replace(/\/$/, "")}/.s.PGSQL.${port}` };
  if (!host) return { path: `/var/run/postgresql/.s.PGSQL.${port}` };
  return { host: host.replace(/^\[|\]$/g, ""), port };
}

/** True when a Postgres server answers at the address within timeoutMs. */
export function pgAnswers(addr: { host: string; port: number } | { path: string }, timeoutMs = 2000): Promise<boolean> {
  return new Promise((resolve) => {
    let done = false;
    let sock: Socket;
    const finish = (ok: boolean) => {
      if (done) return;
      done = true;
      clearTimeout(t);
      sock.destroy();
      resolve(ok);
    };
    const t = setTimeout(() => finish(false), timeoutMs);
    sock = "path" in addr ? connect(addr.path) : connect(addr.port, addr.host);
    sock.once("connect", () => sock.write(Buffer.from([0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f])));
    sock.once("data", (d: Buffer) => finish(d[0] === 0x53 || d[0] === 0x4e));
    sock.once("error", () => finish(false));
    sock.once("close", () => finish(false));
  });
}

export async function GET(): Promise<Response> {
  const dsn = typeof process === "undefined" ? undefined : process.env.DATABASE_URL;
  if (!dsn) return new Response(JSON.stringify(healthBody({ ok: false, database: "DATABASE_URL is not set" })), { status: 503, headers });
  const addr = pgAddress(dsn);
  if (!addr) return new Response(JSON.stringify(healthBody({ ok: false, database: "DATABASE_URL is not a postgres:// URL" })), { status: 503, headers });
  const ok = await pgAnswers(addr);
  return new Response(JSON.stringify(healthBody({ ok, database: ok ? "ok" : "unreachable" })), { status: ok ? 200 : 503, headers });
}
