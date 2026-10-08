// The outbox: emails and Stripe actions that a change asked for, carried out
// after it committed, once each (box, kind, key), retried with backoff until
// they are done. An email fails for good after MAX_ATTEMPTS (/admin shows
// it). A Stripe action (a duplicate subscription's cancel and refund) never
// does: money is at stake, so it is retried for as long as it takes (at most
// an hour apart), and once one is over an hour old the admin is emailed
// (CLOUD_ABUSE_NOTIFY, else EARLY_ACCESS_NOTIFY), once per action.
// Billing, the monitor and the provisioner (ready, setup failed) all write
// here; the website drains it after each webhook and on every monitor run.
import { deliver, render, SITE, type Mail } from "./emails";

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
  created_at: Date;
};

export interface OutboxStore {
  /** Claims up to limit due rows (pushing their next attempt out, so two drains don't both take one). */
  claim(limit: number): Promise<OutboxRow[]>;
  done(id: number): Promise<void>;
  retry(id: number, attempts: number, next: Date, error: string): Promise<void>;
  fail(id: number, attempts: number, error: string): Promise<void>;
  /** Queues a row once per (box, kind, key); true the first time. */
  queue(boxId: string, kind: string, key: string, params: Record<string, unknown>): Promise<boolean>;
  /** Puts Stripe actions given up on (before they were retried for good) back in the queue. */
  requeueStripe(): Promise<number>;
}

/** A Stripe action's effect, for actions whose kind starts with stripe_. */
export type StripeAction = (row: OutboxRow) => Promise<void>;

export const MAX_ATTEMPTS = 10;

/** A Stripe action still not done this long after it was queued is reported to the admin. */
export const STUCK_AFTER_MS = 60 * 60_000;

/** 1, 2, 4 … minutes, at most cap minutes (6 hours) apart. */
export function backoff(attempts: number, now = new Date(), cap = 6 * 60): Date {
  return new Date(now.getTime() + Math.min(cap, 2 ** Math.max(0, attempts - 1)) * 60_000);
}

const isStripe = (kind: string) => kind.startsWith("stripe_");

/** The admin's alert about a Stripe action that is stuck. */
export function stuckMail(to: string, row: Pick<OutboxRow, "box_id">, params: Record<string, any>): Mail {
  return {
    to,
    subject: `Stuck: ${params.kind} for ${row.box_id}`,
    text: `A Stripe action has not gone through for over an hour, and is still being retried.\n\nBox: ${row.box_id}\nAction: ${params.kind} (${params.key})\nSubscription: ${params.subscription ?? "?"}\nQueued: ${params.since}\nAttempts so far: ${params.attempts}\nLast error: ${params.error}\n\nA customer may be paying twice until it does. Look in Stripe, and at ${SITE}/admin.`,
  };
}

export async function drain(
  store: OutboxStore,
  opts: { stripe?: StripeAction; send?: (m: Mail) => ReturnType<typeof deliver>; limit?: number; now?: Date; admin?: string } = {},
): Promise<{ done: number; retried: number; failed: number }> {
  const send = opts.send ?? deliver;
  const admin = opts.admin ?? (process.env.CLOUD_ABUSE_NOTIFY?.trim() || process.env.EARLY_ACCESS_NOTIFY?.trim() || "");
  const now = opts.now ?? new Date();
  const out = { done: 0, retried: 0, failed: 0 };
  await store.requeueStripe();
  for (const row of await store.claim(opts.limit ?? 25)) {
    const attempts = row.attempts + 1;
    let error: string | null = null;
    try {
      if (isStripe(row.kind)) {
        if (!opts.stripe) throw new Error("Stripe isn't set up");
        await opts.stripe(row);
      } else if (row.kind === "admin_stuck") {
        if (!admin) throw new Error("no admin address (CLOUD_ABUSE_NOTIFY)");
        const r = await send(stuckMail(admin, row, row.params ?? {}));
        if (!r.ok) error = r.error;
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
    } else if (isStripe(row.kind)) {
      // Never given up on: a customer may be paying twice until it's done.
      await store.retry(row.id, attempts, backoff(attempts, now, 60), error.slice(0, 500));
      out.retried++;
      if (now.getTime() - new Date(row.created_at).getTime() >= STUCK_AFTER_MS) {
        await store.queue(row.box_id, "admin_stuck", String(row.id), {
          kind: row.kind, key: row.key, subscription: row.params?.subscription ?? null, since: new Date(row.created_at).toISOString(), attempts, error: error.slice(0, 500),
        });
      }
    } else if (attempts >= MAX_ATTEMPTS) {
      await store.fail(row.id, attempts, error.slice(0, 500));
      out.failed++;
    } else {
      await store.retry(row.id, attempts, backoff(attempts, now), error.slice(0, 500));
      out.retried++;
    }
  }
  return out;
}
