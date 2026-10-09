// The monitor: a cron every 5 minutes (tiffin.config.ts). For each managed
// box it checks the dashboard answers over HTTPS and that the box checked in
// lately, and emails the customer when either stops (and when the box is
// back). It parks the shiptiffin.app address of a box that hasn't checked in
// for 72 hours, whatever its HTTPS answers (a server deleted in the Hetzner
// console leaves an IP that someone else may get, and serve 200 from), and
// ends the grace period of boxes whose subscription ended. Decisions are pure
// (decide) so they can be tested; actions.ts does the I/O. The worker checks
// every removal's reason again before it acts.
import { DNS_GRACE_DAYS, extrasOn } from "./billing";

export const DOWN_AFTER = 3; // failed checks in a row (15 minutes) before an email
export const SILENT_AFTER_MS = 36 * 3_600_000;
export const WARN_BEFORE_DAYS = 7;
/** Without a check-in (current licence, from the box's own address) for this long, the address is parked. */
export const PARK_AFTER_MS = 72 * 3_600_000;

export type MonitorBox = {
  id: string;
  name: string | null;
  email: string;
  status: string;
  plan_status: string;
  first_paid_at: Date | null;
  extras_paused_at: Date | null;
  dns_state: string;
  generation: number;
  last_heartbeat_at: Date | null;
  ready_at: Date | null;
  health_failures: number;
  down_alerted_at: Date | null;
  heartbeat_alerted_at: Date | null;
  created_at: Date;
  /** The "address goes soon" email: null when never queued; done means the mail server accepted it. */
  warning: Warning | null;
};

export type Warning = { status: "queued" | "done" | "failed" | "dropped"; doneAt: Date | null };

/** After a warning failed for good, we try it again this long after (and keep the address meanwhile). */
export const WARNING_RETRY_MS = 86_400_000;

export type Email = "down" | "up" | "silent" | "dns_soon" | "dns_removed" | "parked";

export type Decision = {
  probe: boolean; // check the dashboard over HTTPS
  patch: Partial<Pick<MonitorBox, "health_failures" | "down_alerted_at" | "heartbeat_alerted_at">> & { health_checked_at?: Date };
  emails: Email[];
  removeDns: null | { reason: "parked" | "grace"; gen: number; pausedAt?: Date };
  /** Send the failed "address goes soon" warning again. */
  retryWarning: boolean;
};

/** The address counts as published while live, or while records are being (or were half) published. */
const published = (dns: string) => dns === "live" || dns === "pending";

const DAY = 86_400_000;

/** What to do for one box. probeOk is the HTTPS check's result (undefined: not checked). */
export function decide(b: MonitorBox, now: Date, probeOk?: boolean): Decision {
  const d: Decision = { probe: false, patch: {}, emails: [], removeDns: null, retryWarning: false };
  if ((b.status !== "active" && b.status !== "cert_pending") || !b.name) return d;
  const gen = Number(b.generation);

  // Parking: by check-ins alone, paid or not.
  const last = (b.last_heartbeat_at ?? b.ready_at ?? b.created_at).getTime();
  if (published(b.dns_state) && now.getTime() - last > PARK_AFTER_MS) {
    d.removeDns = { reason: "parked", gen };
    d.emails.push("parked");
    return d;
  }

  const on = extrasOn(b.plan_status, Boolean(b.first_paid_at));
  if (b.extras_paused_at && !on) {
    // Extras paused: no monitoring, and the address goes after the grace
    // period, never before a warning reached the customer: the "goes soon"
    // email accepted by the mail server at least WARN_BEFORE_DAYS earlier.
    // A warning that failed doesn't count; it is sent again a day later,
    // and the address stays until one gets through.
    const end = b.extras_paused_at.getTime() + DNS_GRACE_DAYS * DAY;
    if (!published(b.dns_state)) return d;
    const w = b.warning;
    if (now.getTime() >= end - WARN_BEFORE_DAYS * DAY && w === null) d.emails.push("dns_soon");
    if (w?.status === "failed" && now.getTime() - (w.doneAt?.getTime() ?? 0) >= WARNING_RETRY_MS) d.retryWarning = true;
    if (w?.status === "done" && w.doneAt && now.getTime() >= Math.max(end, w.doneAt.getTime() + WARN_BEFORE_DAYS * DAY)) {
      d.removeDns = { reason: "grace", gen, pausedAt: b.extras_paused_at };
      d.emails.push("dns_removed");
    }
    return d;
  }
  if (!on || b.status !== "active") return d;
  if (b.dns_state === "live") {
    d.probe = probeOk === undefined;
    if (probeOk !== undefined) {
      d.patch.health_checked_at = now;
      if (probeOk) {
        d.patch.health_failures = 0;
        if (b.down_alerted_at) {
          d.emails.push("up");
          d.patch.down_alerted_at = null;
        }
      } else {
        const n = b.health_failures + 1;
        d.patch.health_failures = n;
        if (n >= DOWN_AFTER && !b.down_alerted_at) {
          d.emails.push("down");
          d.patch.down_alerted_at = now;
        }
      }
    }
  }
  if (now.getTime() - last > SILENT_AFTER_MS && !b.heartbeat_alerted_at) {
    d.emails.push("silent");
    d.patch.heartbeat_alerted_at = now;
  }
  return d;
}

/** GET <dashboard>/v1/health: true on 200 within 10 seconds. */
export async function probe(url: string): Promise<boolean> {
  try {
    const res = await fetch(url, { signal: AbortSignal.timeout(10_000), redirect: "manual", headers: { "User-Agent": "shiptiffin-monitor" } });
    return res.status === 200;
  } catch {
    return false;
  }
}

// ---- check-ins ----

/** IPv4 as is; IPv6 as its /64 (a server's outgoing address may be any of its /64). */
function ipKey(ip: string): string | null {
  let s = ip.trim().toLowerCase();
  if (s.startsWith("[") && s.endsWith("]")) s = s.slice(1, -1);
  if (s.startsWith("::ffff:") && /^\d+\.\d+\.\d+\.\d+$/.test(s.slice(7))) s = s.slice(7);
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(s)) return s.split(".").every((p) => Number(p) <= 255) ? `4:${s.split(".").map(Number).join(".")}` : null;
  if (!s.includes(":") || s.includes("%")) return null;
  const [head, tail, extra] = s.split("::");
  if (extra !== undefined) return null;
  const h = head ? head.split(":") : [];
  const t = tail !== undefined && tail ? tail.split(":") : [];
  const fill = tail === undefined ? 0 : 8 - h.length - t.length;
  if (fill < 0 || (tail === undefined && h.length !== 8)) return null;
  const all = [...h, ...Array(fill).fill("0"), ...t];
  if (all.length !== 8 || !all.every((g) => /^[0-9a-f]{1,4}$/.test(g))) return null;
  return `6:${all.slice(0, 4).map((g) => parseInt(g, 16)).join(":")}`;
}

/** Whether a check-in came from the box's own address (its IPv4, or its IPv6 /64). */
export function fromBox(ip: string | null | undefined, ipv4: string | null, ipv6: string | null): boolean {
  const k = ip ? ipKey(ip) : null;
  if (!k) return false;
  return (ipv4 != null && ipKey(ipv4) === k) || (ipv6 != null && ipKey(ipv6) === k);
}
