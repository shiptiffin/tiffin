// Fixed-rate load with oha (--latency-correction, so a slow server cannot
// slow the generator down and hide its own latency).
import { execFileSync } from "node:child_process";
import { host, origin, type Target } from "./net.ts";

export type LoadSpec = {
  name: string;
  path: string; // a path, or a regex of paths with `regex: true`
  rate: number; // requests per second
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  regex?: boolean;
  forwarder?: boolean; // through Lima's port forwarder instead of the VM's address
};

export type LoadRow = { name: string; path: string; via: string; rate: number; rps: number; ok: number; p50: number; p95: number; p99: number; max: number; codes: string; errors: string };

export function oha(t: Target, s: LoadSpec, seconds: number, warmup = false): LoadRow {
  const direct = !!t.directIp && !s.forwarder;
  const port = new URL(t.base).port;
  const args = [
    "-z", `${seconds}s`, "-q", String(s.rate), "--latency-correction", "-c", "64", "-w",
    "--no-tui", "--output-format", "json", "--insecure", "-m", s.method ?? "GET",
    "--connect-to", `${new URL(t.base).hostname}:${port}:${direct ? t.directIp : "127.0.0.1"}:${direct ? 8443 : port}`,
  ];
  for (const [k, v] of Object.entries(s.headers ?? {})) args.push("-H", `${k}: ${v}`);
  if (s.body !== undefined) args.push("-d", s.body);
  if (s.regex) args.push("--rand-regex-url");
  args.push(t.base + s.path); // oha takes dots in a regex URL literally
  const raw = execFileSync("oha", args, { encoding: "utf8", maxBuffer: 256 << 20 });
  const j = JSON.parse(raw) as {
    summary: { successRate: number; requestsPerSec: number };
    latencyPercentiles: Record<string, number>;
    summary2?: unknown;
    statusCodeDistribution: Record<string, number>;
    errorDistribution: Record<string, number>;
  };
  const ms = (k: string) => Math.round((j.latencyPercentiles[k] ?? NaN) * 10000) / 10;
  const total = Object.values(j.statusCodeDistribution).reduce((a, b) => a + b, 0);
  const good = Object.entries(j.statusCodeDistribution).filter(([c]) => c.startsWith("2")).reduce((a, [, n]) => a + n, 0);
  const row: LoadRow = {
    name: s.name,
    path: s.path,
    via: direct ? "vzNAT" : "forwarder",
    rate: s.rate,
    rps: Math.round(j.summary.requestsPerSec * 10) / 10,
    ok: total ? Math.round((good / total) * 1000) / 10 : 0,
    p50: ms("p50"),
    p95: ms("p95"),
    p99: ms("p99"),
    max: ms("p99.99"),
    codes: Object.entries(j.statusCodeDistribution).map(([c, n]) => `${c}×${n}`).join(" "),
    errors: Object.entries(j.errorDistribution).map(([e, n]) => `${e}×${n}`).join("; "),
  };
  console.log(`  ${warmup ? "(warm-up) " : ""}${s.name} @${s.rate}/s via ${row.via}: ${row.rps} req/s, p50 ${row.p50} p95 ${row.p95} p99 ${row.p99} ms, 2xx ${row.ok}% ${row.errors}`);
  return row;
}

/** The fixed load profile. Keep it unchanged between runs you compare. */
export function profile(t: Target, o: { chunk: string; actionId?: string }): LoadSpec[] {
  const img = { accept: "image/avif,image/webp,*/*" };
  const specs: LoadSpec[] = [
    { name: "static chunk", path: o.chunk, rate: 1000 },
    { name: "home (static)", path: "/", rate: 400 },
    { name: "home (static)", path: "/", rate: 400, forwarder: true },
    { name: "blog (ISR)", path: "/blog/hello-box", rate: 400 },
    { name: "products (PPR)", path: "/products", rate: 200, headers: { cookie: "cart=2" } },
    { name: "dashboard (dynamic)", path: "/dashboard?delay=0", rate: 50, headers: { cookie: t.cookie } },
    { name: "api/items (JSON)", path: "/api/items", rate: 400 },
    { name: "next/image warm", path: "/_next/image?url=%2Fhero.jpg&w=828&q=75", rate: 300, headers: img },
    // A new source URL every request: each one is decoded, resized and encoded.
    { name: "next/image cold", path: "/_next/image\\?url=%2Fapi%2Fswatch%2F[0-9]{12}&w=640&q=75", rate: 10, headers: img, regex: true },
  ];
  if (o.actionId) {
    specs.push({
      name: "server action POST",
      path: "/dashboard",
      rate: 100,
      method: "POST",
      body: "[1]",
      headers: { "next-action": o.actionId, "content-type": "text/plain;charset=UTF-8", accept: "text/x-component", origin: origin(t), cookie: t.cookie },
    });
  }
  return specs;
}

export { host };
