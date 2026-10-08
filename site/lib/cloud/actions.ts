// What the control plane's pages and routes do, server side. Every function
// checks the box belongs to the account before touching it.
import { ENDED, extrasOn, handleEvent } from "./billing";
import { foundingCoupon, sealPublic } from "./config";
import * as q from "./db";
import { deliver, SITE } from "./emails";
import { checkToken, EU_LOCATIONS, EU_TYPES, family, RESIZE_TYPES, US_LOCATIONS, US_TYPES } from "./hetzner";
import type { Licence } from "./licence";
import { checkoutUrl } from "./checkout";
import { boxDomain, dashboardUrl, nameProblem } from "./names";
import { decide, fromBox, probe, type MonitorBox } from "./monitor";
import { drain } from "./outbox";
import { fingerprint, seal, tokenAAD } from "./seal";
import type { Account } from "./session";
import { FOUNDING_LIMIT, StripeError, stripeClient } from "./stripe";

export class ActionError extends Error {
  constructor(
    message: string,
    readonly status = 400,
  ) {
    super(message);
  }
}

export const stripe = () => stripeClient(process.env.STRIPE_SECRET_KEY!.trim());

const onFor = (b: q.BoxRow) => extrasOn(b.plan_status, Boolean(b.first_paid_at));

/** Carries out what the outbox holds that is due: emails, and Stripe's cancel and refund actions. */
export async function drainOutbox() {
  const s = process.env.STRIPE_SECRET_KEY?.trim() ? stripe() : null;
  return drain(q.pgOutbox(), {
    stripe: s
      ? async (row) => {
          const sub = String(row.params.subscription ?? "");
          if (!sub) throw new Error("no subscription");
          if (row.kind === "stripe_cancel") {
            try {
              await s.cancelNow(sub);
            } catch (e) {
              if (!(e instanceof StripeError && (e.status === 404 || /canceled/i.test(e.message)))) throw e;
            }
            return;
          }
          if (row.kind !== "stripe_cancel_refund") throw new Error(`unknown action ${row.kind}`);
          const r = await s.cancelAndRefund(sub, `${row.box_id}:${row.key}`, { box_id: row.box_id, reason: String(row.params.why ?? "") });
          await q.db()`insert into cloud_billing_log (box_id, what, subscription_id, detail) values (${row.box_id}, ${"cancelled and refunded (" + String(row.params.why ?? "") + ")"}, ${sub}, ${q.db().json(r as any)})`;
          if (row.params.why === "moneyback") await q.db()`update cloud_boxes set refunded_at = coalesce(refunded_at, now()) where id = ${row.box_id}`;
        }
      : undefined,
  });
}

// ---- pay ----

export { reusableCheckout } from "./checkout";

/** Box statuses Renew covers: every box we still manage once its subscription ended. */
const RENEWABLE = new Set<q.BoxStatus>(["paid", "provisioning", "cert_pending", "active", "failed"]);

/** Whether a box's ended subscription can be renewed (pure; the account page shows the button by it). */
export function renewable(b: Pick<q.BoxRow, "status" | "plan_status" | "first_paid_at" | "refunded_at">): boolean {
  return RENEWABLE.has(b.status) && ENDED.has(b.plan_status) && Boolean(b.first_paid_at) && !b.refunded_at;
}

/**
 * The Checkout page for a box: a new box, or (renew) one of the account's
 * boxes whose subscription ended, whatever stage it reached (paid and
 * waiting for Hetzner, set up, or a setup that failed). The new
 * subscription carries the same box id and replaces the ended one. One
 * session per box: stored, and handed out again until it expires, so two
 * tabs pay for one subscription; the request is saved before Stripe is
 * called, so a retry after any failure sends the very same request
 * (checkout.ts). Cards only (plus Link when STRIPE_CHECKOUT_LINK=1): no
 * payment method that confirms days later, so a paid box is a box whose
 * first payment went through.
 */
export async function startCheckout(acct: Account, base = SITE, renew?: string): Promise<string> {
  let box: q.BoxRow;
  if (renew) {
    const b = await q.boxFor(renew, acct.id);
    if (!b || !renewable(b)) {
      throw new ActionError(b && !ENDED.has(b.plan_status) && b.plan_status !== "none" ? "This box's subscription hasn't ended: update your card under Billing instead." : "This box can't be renewed.");
    }
    box = b;
  } else box = await q.pendingBox(acct.id, acct.email);
  const s = stripe();
  return checkoutUrl(q.pgCheckout(), s, box.id, async (b) => {
    const price = await s.monthlyPrice();
    const coupon = renew ? null : foundingCoupon();
    const founding = coupon != null && (await q.foundingCount()) < FOUNDING_LIMIT && (await s.couponOpen(coupon));
    const customer = b.stripe_customer_id ?? (await q.customerFor(acct.id));
    const params: Record<string, unknown> = {
      mode: "subscription",
      line_items: [{ price, quantity: 1 }],
      payment_method_types: process.env.STRIPE_CHECKOUT_LINK === "1" ? ["card", "link"] : ["card"],
      expires_at: Math.floor(Date.now() / 1000) + 30 * 60,
      success_url: renew ? `${base}/account?renewed=${b.id}` : `${base}/start?box=${b.id}&session_id={CHECKOUT_SESSION_ID}`,
      cancel_url: renew ? `${base}/account` : `${base}/start?canceled=1`,
      client_reference_id: b.id,
      metadata: { box_id: b.id },
      subscription_data: { metadata: { box_id: b.id } },
      ...(customer ? { customer } : { customer_email: acct.email }),
      ...(founding ? { discounts: [{ coupon }] } : {}),
    };
    return { params, discounted: founding };
  });
}

/** Back from Checkout: confirm with Stripe at once rather than wait for the webhook. */
export async function confirmCheckout(acct: Account, boxId: string, sessionId: string): Promise<void> {
  const box = await q.boxFor(boxId, acct.id);
  if (!box || !/^cs_[A-Za-z0-9_]+$/.test(sessionId)) return;
  try {
    const s = stripe();
    const cs = await s.getCheckout(sessionId);
    if ((cs.metadata?.box_id ?? cs.client_reference_id) !== box.id || cs.status !== "complete") return;
    await handleEvent(q.pgBilling(), s, { id: `return:${cs.id}`, type: "checkout.session.completed", created: cs.created ?? Math.floor(Date.now() / 1000), data: { object: cs } });
    await drainOutbox();
  } catch (e) {
    console.error("confirm checkout", e instanceof Error ? e.message : e);
  }
}

export async function billingPortal(acct: Account, base = SITE): Promise<string> {
  const customer = await q.portalCustomer(acct.id);
  if (!customer) throw new ActionError("There's no billing account yet: pay for a box first.");
  return (await stripe().portal(customer, `${base}/account`)).url;
}

// ---- connect Hetzner and create ----

const TYPES = new Set<string>([...EU_TYPES, ...US_TYPES]);
const LOCATIONS = new Set<string>([...EU_LOCATIONS, ...US_LOCATIONS]);

async function ownBox(acct: Account, id: string) {
  const box = await q.boxFor(id, acct.id);
  if (!box) throw new ActionError("No such box.", 404);
  return box;
}

/** Checks a pasted key against Hetzner; every call it makes is recorded on the box. */
export async function checkKey(acct: Account, boxId: string, token: string) {
  const box = await ownBox(acct, boxId);
  if (box.status === "awaiting_payment" || box.status === "released") throw new ActionError("Pay for the box first.");
  return checkToken(token, (c) => q.recordCall(box.id, "check", c));
}

export type CreateInput = { token: string; name: string; serverType: string; location: string; keepKey: boolean };

export async function createBox(acct: Account, boxId: string, input: CreateInput): Promise<void> {
  const box = await ownBox(acct, boxId);
  if (box.status !== "paid" && box.status !== "failed") throw new ActionError(box.status === "awaiting_payment" ? "Pay for the box first." : "This box is already set up.");
  if (box.first_paid_at && ENDED.has(box.plan_status)) throw new ActionError("This box's subscription has ended: renew it in your account first (same box, same name).");
  if (!box.first_paid_at || !onFor(box)) throw new ActionError("The first payment for this box hasn't gone through yet.");
  if (box.dns_state === "killed") throw new ActionError("This box was turned off after an abuse report. Write to hello@shiptiffin.com.");
  const name = String(input.name ?? "").trim().toLowerCase();
  const why = nameProblem(name);
  if (why) throw new ActionError(why);
  if (box.name && box.name !== name) throw new ActionError(`This box is called ${box.name}; its name can't change after a first try.`);
  if (!TYPES.has(input.serverType) || !LOCATIONS.has(input.location)) throw new ActionError("Pick one of the sizes and places listed.");
  const token = String(input.token ?? "").trim();
  if (!/^[A-Za-z0-9]{20,128}$/.test(token)) throw new ActionError("Paste your Hetzner token again.");
  if (await q.jobsBusy(box.id)) throw new ActionError("Setup or its clean-up is still running. Try again in a minute.", 409);
  const sealed = seal(sealPublic(), token, tokenAAD(box.id));
  try {
    await q.db().begin(async (tx) => {
      const rows = await tx`update cloud_boxes set name = ${name}, server_type = ${input.serverType}, location = ${input.location},
        token_fingerprint = ${fingerprint(token)}, status = 'provisioning', updated_at = now()
        where id = ${box.id} and status in ('paid', 'failed') returning id`;
      if (rows.length === 0) throw new ActionError("This box changed meanwhile; reload the page.", 409);
      await q.enqueue(box.id, "provision", { name, serverType: input.serverType, location: input.location, keepKey: Boolean(input.keepKey) }, sealed, tx);
    });
  } catch (e: any) {
    if (e?.code === "23505") throw new ActionError(`${boxDomain(name)} is taken. Try another name.`, 409);
    throw e;
  }
}

/**
 * "Open your dashboard". The first time, the one-time sign-in link the box
 * made at setup (it works once, and the box refuses it after 24 hours); we
 * forget it as we hand it out. After that, the box's own sign-in page.
 */
export async function openDashboard(acct: Account, boxId: string): Promise<string> {
  const box = await ownBox(acct, boxId);
  if (!box.name || box.status !== "active") throw new ActionError("The box isn't ready yet.");
  const url = dashboardUrl(box.name);
  const [r] = await q.db()`with old as (select id, signin_code from cloud_boxes where id = ${box.id} and signin_code is not null and signin_expires_at > now() for update)
    update cloud_boxes b set signin_code = null, signin_expires_at = null from old where b.id = old.id returning old.signin_code`;
  const code = r?.signin_code as string | undefined;
  return code && /^tfl_[a-z0-9]+$/.test(code) ? `${url}/login#${code}` : `${url}/login`;
}

// ---- account actions ----

export type BoxAction =
  | { action: "forget-key" }
  | { action: "keep-key"; token: string }
  | { action: "forget-signin" }
  | { action: "resize"; serverType: string; token?: string; keepKey?: boolean }
  | { action: "cancel" }
  | { action: "resume" }
  | { action: "release"; confirm: string }
  | { action: "delete-server"; confirm: string; token: string; deleteData?: boolean };

export async function boxAction(acct: Account, boxId: string, a: BoxAction): Promise<string> {
  const box = await ownBox(acct, boxId);
  const s = q.db();
  switch (a.action) {
    case "forget-key":
      await s`update cloud_boxes set token_sealed = null, token_kept_at = null, updated_at = now() where id = ${box.id}`;
      return "Your Hetzner key is forgotten. Only its fingerprint stays.";
    case "keep-key": {
      const r = await checkToken(a.token, (c) => q.recordCall(box.id, "check", c));
      if (!r.ok) throw new ActionError(r.message);
      const token = a.token.trim();
      await s`update cloud_boxes set token_sealed = ${seal(sealPublic(), token, tokenAAD(box.id))}, token_kept_at = now(), token_fingerprint = ${fingerprint(token)}, updated_at = now() where id = ${box.id}`;
      return "Your Hetzner key is kept, sealed. Remove it any time.";
    }
    case "forget-signin":
      await s`update cloud_boxes set signin_code = null, signin_expires_at = null where id = ${box.id}`;
      return "Done. We no longer hold a sign-in link for your box; sign in on the box itself.";
    case "resize": {
      if (box.status !== "active") throw new ActionError("Only a running box can be resized.");
      if (!onFor(box)) throw new ActionError("One-click resize is part of the subscription. Resize in the Hetzner console instead.");
      if (!(RESIZE_TYPES as readonly string[]).includes(a.serverType) || a.serverType === box.server_type || family(a.serverType) !== family(box.server_type ?? "")) {
        throw new ActionError("Pick another size of the same kind (Hetzner can't move a server between ARM and x86).");
      }
      if (await q.jobsBusy(box.id)) throw new ActionError("Something is already running for this box.", 409);
      const token = a.token?.trim();
      if (token) {
        if (!/^[A-Za-z0-9]{20,128}$/.test(token)) throw new ActionError("Paste your Hetzner token again.");
        await q.enqueue(box.id, "resize", { serverType: a.serverType, keepKey: Boolean(a.keepKey) }, seal(sealPublic(), token, tokenAAD(box.id)));
      } else if (box.token_sealed) {
        await q.enqueue(box.id, "resize", { serverType: a.serverType, useStored: true }, null);
      } else throw new ActionError("Paste your Hetzner key to resize (we forgot it after setup, as you asked).");
      return "Resizing: the box is offline for about 2 minutes.";
    }
    case "cancel":
    case "resume": {
      if (!box.stripe_subscription_id) throw new ActionError("This box has no subscription.");
      await stripe().cancelAtPeriodEnd(box.stripe_subscription_id, a.action === "cancel");
      await s`update cloud_boxes set cancel_at_period_end = ${a.action === "cancel"}, updated_at = now() where id = ${box.id}`;
      return a.action === "cancel"
        ? "Cancelled at the end of this period. Your server and apps keep running after that; updates and the extras stop, and the address stays 30 days."
        : "The subscription continues.";
    }
    case "release":
    case "delete-server": {
      if (!box.name || a.confirm?.trim() !== box.name) throw new ActionError(`Type the box's name (${box.name ?? ""}) to confirm.`);
      if (box.status === "deleting" || box.status === "released") throw new ActionError("This box is already being deleted or released.");
      if (await q.jobsBusy(box.id)) throw new ActionError("Something is already running for this box.", 409);
      const token = a.action === "delete-server" ? a.token?.trim() : undefined;
      if (a.action === "delete-server" && (!token || !/^[A-Za-z0-9]{20,128}$/.test(token))) throw new ActionError("Paste a Hetzner key to delete the server: we only delete it with your key, now.");
      if (box.stripe_subscription_id && !ENDED.has(box.plan_status)) {
        try {
          await stripe().cancelNow(box.stripe_subscription_id);
        } catch (e) {
          if (!(e instanceof StripeError && e.status === 404)) throw e;
        }
      }
      if (a.action === "delete-server") {
        await s.begin(async (tx) => {
          await tx`update cloud_boxes set status = 'deleting', updated_at = now() where id = ${box.id}`;
          await q.enqueue(box.id, "delete_server", { deleteData: Boolean(a.deleteData) }, seal(sealPublic(), token!, tokenAAD(box.id)), tx);
        });
        return "Deleting: first the address, then the server in your Hetzner project.";
      }
      await s.begin(async (tx) => {
        await tx`update cloud_boxes set status = 'released', released_at = now(), token_sealed = null, token_kept_at = null,
          signin_code = null, signin_expires_at = null, updated_at = now() where id = ${box.id}`;
        if (box.dns_state === "live") await q.enqueue(box.id, "dns_remove", { reason: "released", gen: Number(box.generation) }, null, tx);
      });
      return "Released. The server is untouched and yours; it no longer gets updates from us.";
    }
  }
}

// ---- box check-in ----

export type HeartbeatAnswer = { managed: boolean; active: boolean; updates: boolean; message?: string };

/**
 * What a check-in means (pure). A check-in counts only with the licence of
 * the box's current installation (each setup gets the next generation;
 * earlier ones are revoked), sent from the box's own address (its IPv4, or
 * its IPv6 /64). Only a check-in that counts keeps the address from being
 * parked, and brings back one that went.
 */
export function heartbeatDecision(
  box: Pick<q.BoxRow, "status" | "plan_status" | "first_paid_at" | "extras_paused_at" | "generation" | "ipv4" | "ipv6" | "dns_state" | "killed_at"> | null,
  l: Pick<Licence, "gen">,
  ip: string | null | undefined,
): { answer: HeartbeatAnswer; counts: boolean; refused?: string; restore: boolean } {
  if (!box || box.status === "released" || box.status === "deleting") return { answer: { managed: false, active: false, updates: true }, counts: false, restore: false };
  if ((l.gen ?? 0) !== Number(box.generation)) {
    return {
      answer: { managed: false, active: false, updates: true, message: "This box's ShipTiffin licence was replaced by a newer setup; this copy is no longer managed." },
      counts: false,
      refused: "licence of an earlier setup",
      restore: false,
    };
  }
  const on = extrasOn(box.plan_status, Boolean(box.first_paid_at)) && !box.extras_paused_at && (box.status === "active" || box.status === "cert_pending");
  const answer: HeartbeatAnswer = {
    managed: true,
    active: on,
    updates: on,
    message: on ? undefined : "Automatic updates are paused: this box's ShipTiffin subscription is not active. Your apps keep running. Renew at shiptiffin.com/account.",
  };
  if (!fromBox(ip, box.ipv4, box.ipv6)) return { answer, counts: false, refused: `sent from ${ip ?? "an unknown address"}, not the box's own`, restore: false };
  return { answer, counts: true, restore: on && !box.killed_at && (box.dns_state === "removed" || box.dns_state === "parked") };
}

export async function heartbeat(l: Licence, ip: string | null | undefined, report: { version?: unknown; failing?: unknown }): Promise<HeartbeatAnswer> {
  const box = await q.boxById(l.box);
  const d = heartbeatDecision(box, l, ip);
  if (!box) return d.answer;
  if (!d.counts) {
    if (d.refused) await q.db()`update cloud_boxes set heartbeat_refused_at = now(), heartbeat_refused_why = ${d.refused.slice(0, 200)} where id = ${box.id}`;
    return d.answer;
  }
  const failing = Array.isArray(report.failing) ? report.failing.filter((x): x is string => typeof x === "string").slice(0, 30).map((x) => x.slice(0, 40)) : [];
  const version = typeof report.version === "string" ? report.version.slice(0, 40) : null;
  await q.db()`update cloud_boxes set last_heartbeat_at = now(), last_heartbeat_ip = ${ip ?? null}, last_version = ${version}, failing = ${failing},
    heartbeat_alerted_at = null where id = ${box.id}`;
  if (d.restore && !(await q.jobsBusy(box.id))) await q.enqueue(box.id, "dns_set", { reason: "heartbeat", gen: Number(box.generation) }, null);
  return d.answer;
}

// ---- monitor ----

export async function runMonitor(now = new Date()): Promise<{ checked: number; queued: number }> {
  const boxes = await q.db()<Omit<MonitorBox, "warned">[]>`select id, name, email, status, plan_status, first_paid_at, extras_paused_at, dns_state, generation::int as generation,
    last_heartbeat_at, ready_at, health_failures, down_alerted_at, heartbeat_alerted_at, created_at from cloud_boxes where status in ('active', 'cert_pending')`;
  let queued = 0;
  await Promise.all(
    boxes.map(async (row) => {
      // The grace period's warning, keyed by when the extras paused: settled once sent (or given up on).
      const b: MonitorBox = { ...row, warned: row.extras_paused_at ? await q.outboxSettled(row.id, "dns_soon", row.extras_paused_at.toISOString()) : null };
      let d = decide(b, now);
      if (d.probe && b.name) d = decide(b, now, await probe(`${dashboardUrl(b.name)}/v1/health`));
      const p = d.patch;
      if (Object.keys(p).length) await q.db()`update cloud_boxes set ${q.db()(p as any)} where id = ${b.id}`;
      if (d.removeDns && !(await q.jobsBusy(b.id))) {
        await q.enqueue(b.id, "dns_remove", { reason: d.removeDns.reason, gen: d.removeDns.gen, pausedAt: d.removeDns.pausedAt?.toISOString() }, null);
      }
      const paused = b.extras_paused_at?.toISOString() ?? "";
      for (const kind of d.emails) {
        const key =
          kind === "down" || kind === "up" ? (b.down_alerted_at ?? now).toISOString() : kind === "silent" || kind === "parked" ? String(b.last_heartbeat_at?.toISOString() ?? "never") : paused;
        if (await q.outbox(b.id, kind, key, kind === "down" ? { since: now.toISOString() } : {})) queued++;
      }
    }),
  );
  return { checked: boxes.length, queued };
}

// ---- abuse and admin ----

export async function reportAbuse(target: string, email: string | null, details: string): Promise<void> {
  const t = target.trim().toLowerCase().slice(0, 300);
  const m = /([a-z0-9-]+)\.shiptiffin\.app/.exec(t);
  const [box] = m ? await q.db()`select id from cloud_boxes where name = ${m[1]!}` : [];
  await q.db()`insert into cloud_abuse_reports (target, box_id, reporter_email, details) values (${t}, ${(box?.id as string) ?? null}, ${email?.slice(0, 254) || null}, ${details.slice(0, 4000)})`;
  const notify = process.env.CLOUD_ABUSE_NOTIFY?.trim() || process.env.EARLY_ACCESS_NOTIFY?.trim();
  if (notify) await deliver({ to: notify, subject: `Abuse report: ${t}`, text: `Target: ${t}\nBox: ${box?.id ?? "unknown"}\nFrom: ${email ?? "anonymous"}\n\n${details}\n\nAct on it: ${SITE}/admin` });
}

export type AdminInput = { action: "kill" | "restore" | "report" | "refund"; boxId?: string; reason?: string; reportId?: number; status?: string };

export async function adminAction(a: AdminInput) {
  const s = q.db();
  if (a.action === "report" && a.reportId && (a.status === "acted" || a.status === "dismissed" || a.status === "new")) {
    await s`update cloud_abuse_reports set status = ${a.status} where id = ${a.reportId}`;
    return "Report updated.";
  }
  const box = a.boxId ? await q.boxById(a.boxId) : null;
  if (!box) throw new ActionError("No such box.", 404);
  if (a.action === "kill") {
    const reason = (a.reason ?? "").trim().slice(0, 500) || "abuse report";
    await s.begin(async (tx) => {
      await tx`update cloud_boxes set killed_at = now(), kill_reason = ${reason}, dns_state = 'killed', updated_at = now() where id = ${box.id}`;
      await q.enqueue(box.id, "dns_remove", { reason: "kill" }, null, tx);
      await q.outbox(box.id, "killed", new Date().toISOString(), { reason }, tx);
    });
    await drainOutbox();
    return `Removing ${box.name ? boxDomain(box.name) : box.id} from DNS.`;
  }
  if (a.action === "restore") {
    // Back to parked: the address returns at the box's next check-in from its own address.
    await s.begin(async (tx) => {
      await tx`update cloud_boxes set killed_at = null, kill_reason = null, dns_state = 'parked', dns_changed_at = now(), updated_at = now() where id = ${box.id}`;
      if (box.status === "active" || box.status === "cert_pending") await q.enqueue(box.id, "dns_set", { reason: "admin_restore", gen: Number(box.generation) }, null, tx);
    });
    return "Restoring: the address comes back once the box checks in from its own address (within about six hours).";
  }
  if (a.action === "refund") {
    // The 14-day money-back guarantee: refund the first payment in full and end the subscription. Once per box.
    if (!box.stripe_subscription_id) throw new ActionError("This box has no subscription.");
    const first = await s.begin(async (tx) => {
      const queued = await q.outbox(box.id, "stripe_cancel_refund", "moneyback", { subscription: box.stripe_subscription_id, why: "moneyback" }, tx);
      if (queued) await q.outbox(box.id, "refunded", "", {}, tx);
      return queued;
    });
    await drainOutbox();
    return first ? "Refunding the first payment and ending the subscription. The customer is emailed." : "Already refunded (or under way): see the billing log.";
  }
  throw new ActionError("Unknown action.");
}
