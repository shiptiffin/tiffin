// The control plane's tables in the website's Postgres. The cloud worker
// (Go) creates them (internal/cloud/schema.sql) and runs the jobs queued
// here; this file is the website's half.
import { randomBytes } from "node:crypto";
import type postgres from "postgres";
import { sql } from "../early-access-pg";
import type { BillingPatch, BillingRepo, BoxBilling } from "./billing";
import type { OutboxRow, OutboxStore } from "./outbox";
import type { Call } from "./hetzner";

type Sql = postgres.Sql;
type Tx = postgres.TransactionSql;

export function db(): Sql {
  const s = sql();
  if (!s) throw new Error("no DATABASE_URL");
  return s;
}

let tablesSeen = false;
/** Whether the worker has created the tables yet. */
export async function tablesReady(): Promise<boolean> {
  if (tablesSeen) return true;
  const s = sql();
  if (!s) return false;
  try {
    const [r] = await s`select to_regclass('public.cloud_jobs') is not null and to_regclass('public.cloud_outbox') is not null as ok`;
    tablesSeen = Boolean(r?.ok);
  } catch {
    return false;
  }
  return tablesSeen;
}

export function newBoxId(): string {
  const a = "abcdefghijklmnopqrstuvwxyz234567";
  const b = randomBytes(14);
  let s = "box_";
  for (const x of b) s += a[x % 32];
  return s;
}

export type BoxStatus = "awaiting_payment" | "paid" | "provisioning" | "cert_pending" | "active" | "failed" | "deleting" | "released";

export type BoxRow = {
  id: string;
  user_id: string;
  email: string;
  name: string | null;
  status: BoxStatus;
  plan_status: string;
  cancel_at_period_end: boolean;
  current_period_end: Date | null;
  first_paid_at: Date | null;
  extras_paused_at: Date | null;
  stripe_customer_id: string | null;
  stripe_subscription_id: string | null;
  checkout_session_id: string | null;
  checkout_url: string | null;
  checkout_expires_at: Date | null;
  refunded_at: Date | null;
  founding: boolean;
  server_type: string | null;
  location: string | null;
  ipv4: string | null;
  ipv6: string | null;
  generation: number;
  token_fingerprint: string | null;
  token_sealed: string | null;
  token_kept_at: Date | null;
  signin_code: string | null;
  signin_expires_at: Date | null;
  dns_state: "none" | "live" | "removed" | "parked" | "killed";
  last_heartbeat_at: Date | null;
  last_heartbeat_ip: string | null;
  heartbeat_refused_at: Date | null;
  heartbeat_refused_why: string | null;
  last_version: string | null;
  failing: string[];
  health_failures: number;
  health_checked_at: Date | null;
  ready_at: Date | null;
  down_alerted_at: Date | null;
  heartbeat_alerted_at: Date | null;
  killed_at: Date | null;
  kill_reason: string | null;
  released_at: Date | null;
  created_at: Date;
};

export type JobRow = {
  id: number;
  box_id: string;
  kind: string;
  status: "queued" | "running" | "done" | "failed";
  steps: { at: string; text: string }[];
  error: string | null;
  created_at: Date;
  finished_at: Date | null;
};

export type CallRow = { at: Date; method: string; path: string; status: number | null; ms: number | null; error: string | null; purpose: string };

export const boxesFor = (userId: string) =>
  db()<BoxRow[]>`select * from cloud_boxes where user_id = ${userId} order by created_at`;

export async function boxFor(id: string, userId: string): Promise<BoxRow | null> {
  const [b] = await db()<BoxRow[]>`select * from cloud_boxes where id = ${id} and user_id = ${userId}`;
  return b ?? null;
}

export async function boxById(id: string): Promise<BoxRow | null> {
  const [b] = await db()<BoxRow[]>`select * from cloud_boxes where id = ${id}`;
  return b ?? null;
}

/** A box waiting for its first payment, made fresh or reused (one per account: a unique index settles two tabs). */
export async function pendingBox(userId: string, email: string): Promise<BoxRow> {
  const s = db();
  await s`insert into cloud_boxes (id, user_id, email) values (${newBoxId()}, ${userId}, ${email.toLowerCase()}) on conflict do nothing`;
  const [b] = await s<BoxRow[]>`select * from cloud_boxes where user_id = ${userId} and status = 'awaiting_payment'`;
  return b!;
}

export async function customerFor(userId: string): Promise<string | null> {
  const [c] = await db()`select stripe_customer_id from cloud_customers where user_id = ${userId}`;
  return (c?.stripe_customer_id as string | undefined) ?? null;
}

export async function foundingCount(): Promise<number> {
  const [r] = await db()`select count(*)::int as n from cloud_founding_claims`;
  return Number(r?.n ?? 0);
}

export async function nameTaken(name: string): Promise<boolean> {
  const [r] = await db()`select 1 from cloud_boxes where name = ${name}`;
  return Boolean(r);
}

/** Queues a job (a sealed token travels with it until the job ends, two hours at most). */
export async function enqueue(boxId: string, kind: string, args: Record<string, unknown>, tokenSealed: string | null, tx?: Tx): Promise<number> {
  const s = tx ?? db();
  const [j] = await s`insert into cloud_jobs (box_id, kind, args, token_sealed, token_expires_at)
    values (${boxId}, ${kind}, ${s.json(args as any)}, ${tokenSealed}, ${tokenSealed ? new Date(Date.now() + 2 * 3_600_000) : null}) returning id`;
  return Number(j!.id);
}

export async function latestJob(boxId: string, kinds?: string[]): Promise<JobRow | null> {
  const s = db();
  const [j] = kinds
    ? await s<JobRow[]>`select id, box_id, kind, status, steps, error, created_at, finished_at from cloud_jobs where box_id = ${boxId} and kind = any(${kinds}) order by id desc limit 1`
    : await s<JobRow[]>`select id, box_id, kind, status, steps, error, created_at, finished_at from cloud_jobs where box_id = ${boxId} order by id desc limit 1`;
  return j ?? null;
}

export async function jobsBusy(boxId: string): Promise<boolean> {
  const [r] = await db()`select 1 from cloud_jobs where box_id = ${boxId} and status in ('queued', 'running') limit 1`;
  return Boolean(r);
}

export const callsFor = (boxId: string, limit = 200) =>
  db()<CallRow[]>`select at, method, path, status, ms, error, purpose from cloud_hetzner_calls where box_id = ${boxId} order by at desc, id desc limit ${limit}`;

export async function recordCall(boxId: string, purpose: string, c: Call): Promise<void> {
  try {
    await db()`insert into cloud_hetzner_calls (box_id, purpose, at, method, path, status, ms, error)
      values (${boxId}, ${purpose}, ${c.at}, ${c.method}, ${c.path}, ${c.status}, ${c.ms}, ${c.error})`;
  } catch (e) {
    console.error("record a Hetzner call", e instanceof Error ? e.message : e);
  }
}

/** Stripe customer id and box id per account, for the billing portal. */
export async function portalCustomer(userId: string): Promise<string | null> {
  const [r] = await db()`select coalesce((select stripe_customer_id from cloud_customers where user_id = ${userId}),
    (select stripe_customer_id from cloud_boxes where user_id = ${userId} and stripe_customer_id is not null order by created_at desc limit 1)) as c`;
  return (r?.c as string | null) ?? null;
}

/** Queues an email or Stripe action once per (box, kind, key); true the first time. */
export async function outbox(boxId: string, kind: string, key: string, params: Record<string, unknown> = {}, tx?: Tx | Sql): Promise<boolean> {
  const s = tx ?? db();
  const rows = await s`insert into cloud_outbox (box_id, kind, key, params) values (${boxId}, ${kind}, ${key}, ${s.json(params as any)}) on conflict do nothing returning 1`;
  return rows.length > 0;
}

/** Whether an outbox row is settled (sent, or given up on): the address doesn't go before its warning did. */
export async function outboxSettled(boxId: string, kind: string, key: string): Promise<boolean | null> {
  const [r] = await db()`select status from cloud_outbox where box_id = ${boxId} and kind = ${kind} and key = ${key}`;
  return r ? r.status !== "queued" : null;
}

export function pgOutbox(): OutboxStore {
  const s = db();
  return {
    async claim(limit) {
      return s<OutboxRow[]>`with due as (select id from cloud_outbox where status = 'queued' and next_attempt_at <= now() order by id limit ${limit} for update skip locked),
        claimed as (update cloud_outbox o set next_attempt_at = now() + interval '10 minutes' from due where o.id = due.id returning o.*)
        select c.id::int as id, c.box_id, c.kind, c.key, c.params, c.attempts, coalesce(b.email, '') as email, b.name, b.last_heartbeat_at, b.extras_paused_at,
          b.kill_reason, b.stripe_subscription_id from claimed c left join cloud_boxes b on b.id = c.box_id order by c.id`;
    },
    async done(id) {
      await s`update cloud_outbox set status = 'done', done_at = now(), attempts = attempts + 1, last_error = null where id = ${id}`;
    },
    async retry(id, attempts, next, error) {
      await s`update cloud_outbox set attempts = ${attempts}, next_attempt_at = ${next}, last_error = ${error} where id = ${id}`;
    },
    async fail(id, attempts, error) {
      await s`update cloud_outbox set status = 'failed', attempts = ${attempts}, last_error = ${error}, done_at = now() where id = ${id}`;
    },
  };
}

// ---- billing ----

type BillingRow = BoxRow;

const toBilling = (b: BillingRow): BoxBilling => ({
  id: b.id,
  userId: b.user_id,
  email: b.email,
  name: b.name,
  status: b.status,
  planStatus: b.plan_status,
  firstPaidAt: b.first_paid_at,
  extrasPausedAt: b.extras_paused_at,
  dnsState: b.dns_state,
  generation: Number(b.generation),
  stripeCustomerId: b.stripe_customer_id,
  stripeSubscriptionId: b.stripe_subscription_id,
  founding: b.founding,
});

function pgBillingTx(tx: Tx | Sql): BillingRepo {
  const repo: BillingRepo = {
    transaction: async (fn) => fn(repo),
    async firstTime(id, type, created) {
      const rows = await tx`insert into cloud_stripe_events (id, type, created) values (${id}, ${type}, ${created}) on conflict do nothing returning 1`;
      return rows.length > 0;
    },
    async lockBox({ boxId, subscriptionId }) {
      // Locked for the rest of the event, so two events for one box run one after the other.
      const [b] = await tx<BillingRow[]>`select * from cloud_boxes
        where (${boxId ?? null}::text is not null and id = ${boxId ?? null}) or (${subscriptionId ?? null}::text is not null and stripe_subscription_id = ${subscriptionId ?? null})
        order by (id = ${boxId ?? ""}) desc limit 1 for update`;
      return b ? toBilling(b) : null;
    },
    async update(boxId, p: BillingPatch) {
      const set: Record<string, unknown> = {};
      if (p.status !== undefined) set.status = p.status;
      if (p.planStatus !== undefined) set.plan_status = p.planStatus;
      if (p.firstPaidAt !== undefined) set.first_paid_at = p.firstPaidAt;
      if (p.extrasPausedAt !== undefined) set.extras_paused_at = p.extrasPausedAt;
      if (p.stripeCustomerId !== undefined) set.stripe_customer_id = p.stripeCustomerId;
      if (p.stripeSubscriptionId !== undefined) set.stripe_subscription_id = p.stripeSubscriptionId;
      if (p.founding !== undefined) set.founding = p.founding;
      if (p.cancelAtPeriodEnd !== undefined) set.cancel_at_period_end = p.cancelAtPeriodEnd;
      if (p.currentPeriodEnd !== undefined) set.current_period_end = p.currentPeriodEnd;
      if (p.refundedAt !== undefined) set.refunded_at = p.refundedAt;
      if (Object.keys(set).length === 0) return;
      set.updated_at = new Date();
      await tx`update cloud_boxes set ${tx(set as any)} where id = ${boxId}`;
    },
    async claimFounding(boxId) {
      await tx`insert into cloud_founding_claims (box_id) values (${boxId}) on conflict do nothing`;
    },
    async saveCustomer(userId, email, customerId) {
      await tx`insert into cloud_customers (user_id, email, stripe_customer_id) values (${userId}, ${email}, ${customerId})
        on conflict (user_id) do update set stripe_customer_id = excluded.stripe_customer_id, email = excluded.email`;
    },
    async enqueue(boxId, kind, args = {}) {
      await tx`insert into cloud_jobs (box_id, kind, args) values (${boxId}, ${kind}, ${tx.json(args as any)})`;
    },
    outbox: (boxId, kind, key, params) => outbox(boxId, kind, key, params, tx),
    async log(boxId, what, subscriptionId, detail = {}) {
      await tx`insert into cloud_billing_log (box_id, what, subscription_id, detail) values (${boxId}, ${what}, ${subscriptionId}, ${tx.json(detail as any)})`;
    },
  };
  return repo;
}

/** The billing repo: each event in its own transaction. */
export function pgBilling(): BillingRepo {
  const s = db();
  const outer = pgBillingTx(s);
  return { ...outer, transaction: (fn) => s.begin((tx) => fn(pgBillingTx(tx))) as any };
}
