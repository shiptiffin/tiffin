// The monitor: a cron every 5 minutes (tiffin.config.ts). For each managed
// box it checks the dashboard answers over HTTPS and that the box checked in
// within a day and a half, and emails the customer when either stops (and
// when the box is back). It also ends the shiptiffin.app grace period of
// boxes whose subscription ended. Decisions are pure (decide) so they can be
// tested; run() does the I/O.
import { DNS_GRACE_DAYS, EXTRAS_ON } from "./billing";

export const DOWN_AFTER = 3; // failed checks in a row (15 minutes) before an email
export const SILENT_AFTER_MS = 36 * 3_600_000;
export const WARN_BEFORE_DAYS = 7;

export type MonitorBox = {
  id: string;
  name: string | null;
  email: string;
  status: string;
  plan_status: string;
  extras_paused_at: Date | null;
  dns_state: string;
  last_heartbeat_at: Date | null;
  health_failures: number;
  down_alerted_at: Date | null;
  heartbeat_alerted_at: Date | null;
  created_at: Date;
};

export type Decision = {
  probe: boolean; // check the dashboard over HTTPS
  patch: Partial<Pick<MonitorBox, "health_failures" | "down_alerted_at" | "heartbeat_alerted_at">> & { health_checked_at?: Date };
  emails: ("down" | "up" | "silent" | "dns_soon" | "dns_removed")[];
  removeDns: boolean;
};

const DAY = 86_400_000;

/** What to do for one box. probeOk is the HTTPS check's result (undefined: not checked). */
export function decide(b: MonitorBox, now: Date, probeOk?: boolean): Decision {
  const d: Decision = { probe: false, patch: {}, emails: [], removeDns: false };
  if (b.status !== "active" || !b.name) return d;
  if (b.extras_paused_at && !EXTRAS_ON.has(b.plan_status)) {
    // Extras paused: no monitoring, and the address goes after the grace period.
    const end = b.extras_paused_at.getTime() + DNS_GRACE_DAYS * DAY;
    if (b.dns_state === "live" && now.getTime() >= end) {
      d.removeDns = true;
      d.emails.push("dns_removed");
    } else if (b.dns_state === "live" && now.getTime() >= end - WARN_BEFORE_DAYS * DAY) {
      d.emails.push("dns_soon");
    }
    return d;
  }
  if (!EXTRAS_ON.has(b.plan_status)) return d;
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
  const last = (b.last_heartbeat_at ?? b.created_at).getTime();
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
