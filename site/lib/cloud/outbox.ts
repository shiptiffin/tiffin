// The outbox: emails and Stripe actions that a change asked for, carried out
// after it committed, once each (box, kind, key), retried with backoff until
// they are done (or fail for good after MAX_ATTEMPTS, which /admin shows).
// Billing, the monitor and the cloud worker (ready, setup failed) all write
// here; the website drains it after each webhook and on every monitor run.
import { deliver, render, type Mail } from "./emails";

export type OutboxRow = {
  id: number;
  box_id: string;
  kind: string;
  key: string;
  params: Record<string, any>;
  attempts: number;
  email: string;
  name: string | null;
  last_heartbeat_at: Date | null;
  extras_paused_at: Date | null;
  kill_reason: string | null;
  stripe_subscription_id: string | null;
};

export interface OutboxStore {
  /** Claims up to limit due rows (pushing their next attempt out, so two drains don't both take one). */
  claim(limit: number): Promise<OutboxRow[]>;
  done(id: number): Promise<void>;
  retry(id: number, attempts: number, next: Date, error: string): Promise<void>;
  fail(id: number, attempts: number, error: string): Promise<void>;
}

/** A Stripe action's effect, for actions whose kind starts with stripe_. */
export type StripeAction = (row: OutboxRow) => Promise<void>;

export const MAX_ATTEMPTS = 10;

/** 1, 2, 4 … minutes, at most 6 hours apart. */
export function backoff(attempts: number, now = new Date()): Date {
  return new Date(now.getTime() + Math.min(6 * 60, 2 ** Math.max(0, attempts - 1)) * 60_000);
}

export async function drain(
  store: OutboxStore,
  opts: { stripe?: StripeAction; send?: (m: Mail) => ReturnType<typeof deliver>; limit?: number; now?: Date } = {},
): Promise<{ done: number; retried: number; failed: number }> {
  const send = opts.send ?? deliver;
  const out = { done: 0, retried: 0, failed: 0 };
  for (const row of await store.claim(opts.limit ?? 25)) {
    const attempts = row.attempts + 1;
    let error: string | null = null;
    try {
      if (row.kind.startsWith("stripe_")) {
        if (!opts.stripe) throw new Error("Stripe isn't set up");
        await opts.stripe(row);
      } else {
        const m = render(row.kind, { email: row.email, name: row.name, last_heartbeat_at: row.last_heartbeat_at, extras_paused_at: row.extras_paused_at, kill_reason: row.kill_reason }, row.params ?? {}, opts.now);
        if (!m) throw new Error(`no email called ${row.kind}`);
        const r = await send(m);
        if (!r.ok) error = r.error;
      }
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
    if (error === null) {
      await store.done(row.id);
      out.done++;
    } else if (attempts >= MAX_ATTEMPTS) {
      await store.fail(row.id, attempts, error.slice(0, 500));
      out.failed++;
    } else {
      await store.retry(row.id, attempts, backoff(attempts, opts.now), error.slice(0, 500));
      out.retried++;
    }
  }
  return out;
}
