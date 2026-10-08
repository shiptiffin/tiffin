// Wires the early-access list to what the box gives the app: Postgres
// (DATABASE_URL), mail (SMTP_URL, EMAIL_FROM) and analytics. Server only.
import { send } from "@shiptiffin/sdk/email";
import type { Deps } from "./early-access";
import { pgRepo, sql } from "./early-access-pg";

export const SITE = "https://shiptiffin.com";

/** The site's address for links in emails: the request's own in development, else shiptiffin.com. */
export function baseUrl(request: Request): string {
  if (process.env.NODE_ENV !== "production") return new URL(request.url).origin;
  return (process.env.SITE_URL || SITE).replace(/\/+$/, "");
}

/** "ShipTiffin <website@box>": the box's sender for the project, with our name on it. */
function from(): string | undefined {
  const addr = process.env.EMAIL_FROM?.match(/<([^>]+)>/)?.[1] ?? process.env.EMAIL_FROM;
  return addr ? `ShipTiffin <${addr}>` : undefined;
}

/** null when the project has no database: the form then says to email us. */
export function boxDeps(request: Request): Deps | null {
  const db = sql();
  if (!db) return null;
  return {
    repo: pgRepo(db),
    baseUrl: baseUrl(request),
    notifyTo: process.env.EARLY_ACCESS_NOTIFY?.trim() || null,
    log: (msg, err) => console.error(msg, err instanceof Error ? err.message : err),
    send: async (m) => {
      if (!process.env.SMTP_URL) throw new Error("no SMTP_URL: the project has no email service");
      const r = await send({ ...m, from: from() });
      if (r.accepted.length === 0) throw new Error(`refused: ${r.response}`);
    },
  };
}

/**
 * At most `max` sign-ups from one address per window, per instance. A box
 * runs one instance of this site, so that is the whole limit; the
 * per-address email cooldown (RESEND_AFTER_MS) holds across instances.
 */
export function rateLimiter(max: number, windowMs: number) {
  const hits = new Map<string, number[]>();
  return (key: string, now = Date.now()): boolean => {
    const recent = (hits.get(key) ?? []).filter((t) => now - t < windowMs);
    if (recent.length >= max) {
      hits.set(key, recent);
      return false;
    }
    recent.push(now);
    hits.set(key, recent);
    if (hits.size > 10_000) for (const [k, v] of hits) if (v.every((t) => now - t >= windowMs)) hits.delete(k);
    return true;
  };
}
