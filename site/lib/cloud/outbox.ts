// The outbox: emails and Stripe actions that a change asked for, carried out
// after it committed, once each (box, kind, key), retried with backoff until
// they are done. An email fails for good after MAX_ATTEMPTS (/admin shows
// it). A Stripe action (a duplicate subscription's cancel and refund) never
// does: money is at stake, so it is retried for as long as it takes (at most
// an hour apart), and once one is over an hour old the admin is emailed
// (CLOUD_ABUSE_NOTIFY, else EARLY_ACCESS_NOTIFY), once per action.
// Billing, the monitor and the provisioner (ready, setup failed) all write
// here; the website drains it after each webhook and on every monitor run.
import { deliver, render, stuckMail as stuck, type Mail } from "./emails";
import type { MoneyRow } from "./money";

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
  /** The box's status now (null if the row's box is gone). */
  box_status: string | null;
  /** For the "deleted" email: when, and the money (lib/cloud/money.ts). */
  deleted_at?: Date | null;
} & Partial<MoneyRow> & {
  created_at: Date;
};

/**
 * Emails about a box that runs: none of them is sent once the box is being
 * deleted or deleted (its one "deleted" email says what happened). The
 * provisioner drops the queued ones when it marks the box deleted
 * (internal/cloud/store.go, runningKinds); the drain skips any left.
 */
export const RUNNING_ONLY = new Set(["ready", "attention", "server_off", "extras_paused", "extras_resumed", "payment_failed", "dns_soon", "dns_removed", "parked", "silent", "down", "up"]);
const GONE = new Set(["deleting", "deleted"]);

export interface OutboxStore {
  /** Claims up to limit due rows (pushing their next attempt out, so two drains don't both take one). */
  claim(limit: number): Promise<OutboxRow[]>;
  done(id: number): Promise<void>;
  retry(id: number, attempts: number, next: Date, error: string): Promise<void>;
  fail(id: number, attempts: number, error: string): Promise<void>;
  /** Not sent, on purpose (status dropped). */
  drop(id: number, why: string): Promise<void>;
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
export function stuckMail(to: string, row: Pick<OutboxRow, "box_id">, params: Record<string, any>): Promise<Mail> {
  const s = (v: unknown) => (v === undefined || v === null ? "?" : String(v));
  return stuck(to, { box: row.box_id, kind: s(params.kind), key: s(params.key), subscription: s(params.subscription), since: s(params.since), attempts: s(params.attempts), error: s(params.error) });
}

export async function drain(
  store: OutboxStore,
  opts: { stripe?: StripeAction; send?: (m: Mail) => ReturnType<typeof deliver>; limit?: number; now?: Date; admin?: string } = {},
): Promise<{ done: number; retried: number; failed: number; dropped: number }> {
  const send = opts.send ?? deliver;
  const admin = opts.admin ?? (process.env.CLOUD_ABUSE_NOTIFY?.trim() || process.env.EARLY_ACCESS_NOTIFY?.trim() || "");
  const now = opts.now ?? new Date();
  const out = { done: 0, retried: 0, failed: 0, dropped: 0 };
  await store.requeueStripe();
  for (const row of await store.claim(opts.limit ?? 25)) {
    if (RUNNING_ONLY.has(row.kind) && row.box_status && GONE.has(row.box_status)) {
      await store.drop(row.id, "the box was deleted");
      out.dropped++;
      continue;
    }
    const attempts = row.attempts + 1;
    let error: string | null = null;
    try {
      if (isStripe(row.kind)) {
        if (!opts.stripe) throw new Error("Stripe isn't set up");
        await opts.stripe(row);
      } else if (row.kind === "admin_stuck") {
        if (!admin) throw new Error("no admin address (CLOUD_ABUSE_NOTIFY)");
        const r = await send(await stuckMail(admin, row, row.params ?? {}));
        if (!r.ok) error = r.error;
      } else {
        const m = await render(row.kind, { ...row, id: row.box_id }, row.params ?? {}, opts.now);
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
