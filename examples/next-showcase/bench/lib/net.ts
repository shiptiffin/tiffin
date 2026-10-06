// HTTP from this computer to the box: every *.localhost name goes to the
// forwarded port on 127.0.0.1 (the way a browser reaches a local box), or
// to the VM's own address when a direct IP is given.
import zlib from "node:zlib";
import { Agent, buildConnector, request } from "undici";

export type Target = {
  base: string; // https://next-showcase.tiffin.localhost:<port>
  directIp?: string; // the VM's vzNAT address (the edge listens on 8443)
  cookie: string; // the dashboard session cookie
};

export function agent(t: Target, direct = false): Agent {
  const connect = buildConnector({ rejectUnauthorized: false, allowH2: false } as buildConnector.BuildOptions);
  return new Agent({
    connect: (opts, cb) =>
      connect(
        direct && t.directIp
          ? { ...opts, hostname: t.directIp, port: "8443", servername: opts.hostname }
          : { ...opts, hostname: "127.0.0.1", servername: opts.hostname },
        cb,
      ),
  });
}

export type Res = { status: number; headers: Record<string, string>; body: Buffer; ms: number };

export async function get(t: Target, path: string, init: { method?: string; headers?: Record<string, string>; body?: string; direct?: boolean } = {}): Promise<Res> {
  const started = performance.now();
  const r = await request(path.startsWith("http") ? path : t.base + path, {
    method: (init.method ?? "GET") as "GET",
    headers: { "accept-encoding": "identity", ...init.headers },
    body: init.body,
    dispatcher: agent(t, init.direct),
  });
  const body = Buffer.from(await r.body.arrayBuffer());
  const headers: Record<string, string> = {};
  for (const [k, v] of Object.entries(r.headers)) headers[k] = Array.isArray(v) ? v.join(", ") : String(v ?? "");
  return { status: r.statusCode, headers, body, ms: performance.now() - started };
}

/**
 * Reads a response as it arrives, decompressing it the way a browser does:
 * [ms since the request, text] per decoded chunk.
 */
export async function chunks(t: Target, path: string, headers: Record<string, string> = {}): Promise<{ status: number; headers: Record<string, string>; parts: [number, string][] }> {
  const started = performance.now();
  const r = await request(t.base + path, { headers, dispatcher: agent(t) });
  const hs: Record<string, string> = {};
  for (const [k, v] of Object.entries(r.headers)) hs[k] = Array.isArray(v) ? v.join(", ") : String(v ?? "");
  const enc = hs["content-encoding"] ?? "";
  const z = enc === "gzip" ? zlib.createGunzip({ flush: zlib.constants.Z_SYNC_FLUSH })
    : enc === "br" ? zlib.createBrotliDecompress()
    : enc === "zstd" ? zlib.createZstdDecompress()
    : null;
  const parts: [number, string][] = [];
  const dec = new TextDecoder();
  if (!z) {
    for await (const c of r.body) parts.push([performance.now() - started, dec.decode(c as Buffer, { stream: true })]);
  } else {
    z.on("data", (c: Buffer) => parts.push([performance.now() - started, dec.decode(c, { stream: true })]));
    const done = new Promise((res, rej) => { z.on("end", res); z.on("error", rej); });
    for await (const c of r.body) z.write(c as Buffer);
    z.end();
    await done;
  }
  return { status: r.statusCode, headers: hs, parts };
}

/** The Accept-Encoding Chrome sends. */
export const BROWSER_AE = "gzip, deflate, br, zstd";

export const origin = (t: Target) => new URL(t.base).origin;
export const host = (t: Target) => new URL(t.base).host;
