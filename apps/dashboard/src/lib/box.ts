import type { Health, StatusReport } from "@/api/client";
import { duration } from "./format";
import { relative } from "./time";

/**
 * Uptime has one meaning in the dashboard: how long the machine has been up
 * (from /v1/box/resources or the metrics overview, both the kernel's count).
 * "up 3 h 14 min". Tiffin's own process is a different fact, said as an event
 * with tiffinStarted, so the two never read as rival uptimes.
 */
export function boxUp(seconds: number | undefined): string | undefined {
  return seconds === undefined ? undefined : `up ${duration(seconds)}`;
}

/** When Tiffin's process last started, from the status report's Go duration: "57 minutes ago". */
export function tiffinStarted(goUptime: string, now = Date.now()): string {
  const m = goUptime.match(/^(?:(\d+)h)?(?:(\d+)m(?!s))?(?:([\d.]+)s)?$/);
  const secs = m ? Number(m[1] ?? 0) * 3600 + Number(m[2] ?? 0) * 60 + Number(m[3] ?? 0) : 0;
  return relative(new Date(now - secs * 1000).toISOString(), now);
}

/** The box's name for the nameplate: its hostname, without Lima's prefix. */
export function boxName(s: StatusReport | undefined): string {
  if (!s) return "tiffin";
  return s.host.hostname.replace(/^lima-/, "") || "tiffin";
}

/** Where it runs, in words: "Mac · Lima", "Linux · arm64". */
export function whereItRuns(s: StatusReport | undefined): string | undefined {
  if (!s) return undefined;
  if (s.host.hostname.startsWith("lima-")) return "Mac · Lima";
  const os = s.host.os === "linux" ? "Linux" : s.host.os === "darwin" ? "macOS" : s.host.os;
  return `${os} · ${s.host.arch}`;
}

/** "Tiffin 0.4.2", or "Tiffin dev" for a development build. */
export function versionLabel(s: StatusReport | undefined): string | undefined {
  if (!s) return undefined;
  return `Tiffin ${s.version.replace(/^v/, "")}`;
}


/** The unauthenticated health report: the running build, its commit and where its source is. */
export const healthQuery = {
  queryKey: ["health"],
  queryFn: () => fetch("/v1/health").then((r) => r.json() as Promise<Health>),
  staleTime: 300_000,
  retry: false,
} as const;

/** Tiffin's source code, at the commit this box runs when the build says which (AGPL-3.0 section 13). */
export const SOURCE = "https://github.com/shiptiffin/tiffin";
export function sourceURL(h: Health | undefined): string {
  return h?.source || SOURCE;
}
