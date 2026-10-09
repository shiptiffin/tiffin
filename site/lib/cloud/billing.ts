// What Stripe's webhooks do to a box. Stripe is the source of truth: every
// event only says "look again", and the handler, holding the box's row lock,
// retrieves the subscription from Stripe and applies its status as it is
// now. So events arriving late, twice or out of order (Stripe's timestamps
// have one-second precision) can't leave an old state behind, and paying an
// old invoice of a cancelled subscription changes nothing. Each event is
// handled once (its id is recorded in the same transaction as its writes).
//
// A box is bound to one subscription. A second live subscription for the
// same box (two Checkout tabs) is cancelled at once and its first invoice
// refunded; events of any other subscription are ignored. Once the bound
// subscription has ended, a new one for the box (Renew) takes its place.
//
// Billing only ever switches the managed extras (automatic updates,
// monitoring alerts, the shiptiffin.app address after a grace period). The
// customer's server and apps are theirs and are never touched.

/** Days the shiptiffin.app address stays after the extras pause. */
export const DNS_GRACE_DAYS = 30;

/** Subscription statuses that are not over (the box's bound subscription can't be replaced). */
export const LIVE = new Set(["active", "trialing", "past_due", "unpaid", "paused"]);
/** Ended: Renew starts a new subscription for the box. */
export const ENDED = new Set(["canceled", "incomplete_expired"]);
/**
 * Box statuses we no longer manage: being deleted, deleted (for good), or
 * released (the customer stopped the managed service). Their subscription
 * was ended on purpose, so its end pauses nothing and sends no email.
 */
export const UNMANAGED = new Set<string>(["deleting", "deleted", "released"]);

/**
 * Whether the managed extras are on: active or trialing; past_due too (Stripe
 * is still retrying) but only once a first payment went through, so a box
 * never runs on a payment that never happened.
 */
export function extrasOn(planStatus: string, firstPaid: boolean): boolean {
  if (planStatus === "active" || planStatus === "trialing") return firstPaid;
  return planStatus === "past_due" && firstPaid;
}

export type BoxBilling = {
  id: string;
  userId: string;
  email: string;
  name: string | null;
  status: string;
  planStatus: string;
  firstPaidAt: Date | null;
  extrasPausedAt: Date | null;
  dnsState: string;
  generation: number;
  stripeCustomerId: string | null;
  stripeSubscriptionId: string | null;
  /** Whether the founding price applies now: the bound subscription's discount is running. The claim on the offer is in cloud_founding_claims. */
  founding: boolean;
};

export type BillingPatch = Partial<
  Pick<BoxBilling, "status" | "planStatus" | "firstPaidAt" | "extrasPausedAt" | "stripeCustomerId" | "stripeSubscriptionId" | "founding">
> & {
  cancelAtPeriodEnd?: boolean;
  currentPeriodEnd?: Date | null;
  refundedAt?: Date;
  /** The latest paid invoice: what the customer was last charged, and when. */
  lastCharge?: { cents: number; currency: string; at: Date };
  /** When the subscription ended (Stripe's ended_at, else canceled_at). */
  planEndedAt?: Date;
};

export interface BillingRepo {
  transaction<T>(fn: (r: BillingRepo) => Promise<T>): Promise<T>;
  /** Records the event; false when it was handled before. */
  firstTime(id: string, type: string, created: Date): Promise<boolean>;
  /** Finds and locks the box (for the rest of the transaction): by id, else by subscription. */
  lockBox(q: { boxId?: string | null; subscriptionId?: string | null }): Promise<BoxBilling | null>;
  update(boxId: string, patch: BillingPatch): Promise<void>;
  claimFounding(boxId: string): Promise<void>;
  saveCustomer(userId: string, email: string, customerId: string): Promise<void>;
  enqueue(boxId: string, kind: "dns_set" | "dns_remove", args?: Record<string, unknown>): Promise<void>;
  /** Queues an email or a Stripe action once per (box, kind, key); true the first time. */
  outbox(boxId: string, kind: string, key: string, params?: Record<string, unknown>): Promise<boolean>;
  log(boxId: string, what: string, subscriptionId: string | null, detail?: Record<string, unknown>): Promise<void>;
}

/** The Stripe calls billing needs (the real client, or a fake in tests). */
export interface BillingStripe {
  getSubscription(id: string): Promise<SubscriptionNow | null>;
  getInvoice(id: string): Promise<any>;
  invoiceForCharge(charge: any): Promise<string | null>;
}

export type SubscriptionNow = {
  id: string;
  status: string;
  customer: string | { id: string };
  metadata?: Record<string, string>;
  cancel_at_period_end?: boolean;
  /** When a scheduled cancellation ends it (flexible billing mode: how the portal's "cancel at period end" shows). */
  cancel_at?: number | null;
  current_period_end?: number;
  items?: { data?: { current_period_end?: number }[] };
  latest_invoice?: string | { id: string; status: string; billing_reason?: string; amount_paid?: number; currency?: string; status_transitions?: { paid_at?: number | null } } | null;
  canceled_at?: number | null;
  ended_at?: number | null;
  /** Its discounts (expanded): the founding coupon is the only one we give. */
  discounts?: (string | { end?: number | null })[];
};

/**
 * Whether the subscription is at the founding price now: it has a discount
 * that hasn't ended. A renewal has none, and the founding coupon ends after
 * 24 months. Undefined when Stripe didn't say.
 */
export function foundingNow(sub: SubscriptionNow, now: Date): boolean | undefined {
  if (!Array.isArray(sub.discounts)) return undefined;
  return sub.discounts.some((d) => typeof d === "string" || d.end == null || d.end * 1000 > now.getTime());
}

/** What the subscription says about money, for the account page and emails: the last charge, and when it ended. */
export function moneyFacts(sub: SubscriptionNow): Pick<BillingPatch, "lastCharge" | "planEndedAt"> {
  const out: Pick<BillingPatch, "lastCharge" | "planEndedAt"> = {};
  const inv = sub.latest_invoice;
  const paidAt = inv && typeof inv === "object" ? inv.status_transitions?.paid_at : null;
  if (inv && typeof inv === "object" && inv.status === "paid" && (inv.amount_paid ?? 0) > 0 && typeof paidAt === "number") {
    out.lastCharge = { cents: inv.amount_paid!, currency: inv.currency ?? "usd", at: new Date(paidAt * 1000) };
  }
  const end = ENDED.has(sub.status) ? (sub.ended_at ?? sub.canceled_at) : null;
  if (typeof end === "number") out.planEndedAt = new Date(end * 1000);
  return out;
}

export type StripeEvent = { id: string; type: string; created: number; data: { object: any } };

const idOf = (s: unknown): string | null => (typeof s === "string" ? s : s && typeof s === "object" && "id" in s ? String((s as any).id) : null);

/** The subscription an invoice belongs to (old and new API shapes). */
export function invoiceSubscription(inv: any): string | null {
  return idOf(inv?.subscription) ?? idOf(inv?.parent?.subscription_details?.subscription) ?? null;
}

function invoiceBox(inv: any): string | null {
  return inv?.parent?.subscription_details?.metadata?.box_id ?? inv?.subscription_details?.metadata?.box_id ?? null;
}

/** The end of the current period (old and new API shapes). */
function periodEnd(sub: SubscriptionNow): Date | null {
  const t = sub.current_period_end ?? sub.items?.data?.[0]?.current_period_end;
  return typeof t === "number" ? new Date(t * 1000) : null;
}

/**
 * Whether the subscription is set to end, and when it ends or renews. With
 * flexible billing mode (the default since API version 2025-09-30.clover) a
 * cancellation at the end of the period can show only as cancel_at, with
 * cancel_at_period_end false: the customer portal's does.
 */
export function ending(sub: Pick<SubscriptionNow, "cancel_at_period_end" | "cancel_at" | "current_period_end" | "items">): { cancelAtPeriodEnd: boolean; currentPeriodEnd: Date | null } {
  const end = periodEnd(sub as SubscriptionNow);
  const cancelAt = typeof sub.cancel_at === "number" ? new Date(sub.cancel_at * 1000) : null;
  return {
    cancelAtPeriodEnd: Boolean(sub.cancel_at_period_end) || cancelAt != null,
    currentPeriodEnd: cancelAt && (!end || cancelAt < end) ? cancelAt : end,
  };
}

/** Whether the subscription's latest invoice is paid: its first payment went through (card only, so no delayed methods). */
function latestPaid(sub: SubscriptionNow): boolean {
  const inv = sub.latest_invoice;
  return Boolean(inv && typeof inv === "object" && inv.status === "paid");
}

/** Handles one verified event. Emails and Stripe actions it asks for go to the outbox, sent once it committed. */
export async function handleEvent(repo: BillingRepo, stripe: BillingStripe, ev: StripeEvent, now = new Date()): Promise<{ handled: boolean }> {
  return repo.transaction(async (r) => {
    if (!(await r.firstTime(ev.id, ev.type, new Date(ev.created * 1000)))) return { handled: false };
    const o = ev.data.object;
    let subId: string | null = null;
    let boxHint: string | null = null;
    let refundedInvoice: any = null;
    switch (ev.type) {
      case "checkout.session.completed":
      case "checkout.session.async_payment_succeeded":
      case "checkout.session.async_payment_failed":
        if (o.mode !== "subscription") return { handled: true };
        subId = idOf(o.subscription);
        boxHint = o.metadata?.box_id ?? o.client_reference_id ?? null;
        break;
      case "customer.subscription.created":
      case "customer.subscription.updated":
      case "customer.subscription.deleted":
      case "customer.subscription.paused":
      case "customer.subscription.resumed":
        subId = idOf(o.id);
        boxHint = o.metadata?.box_id ?? null;
        break;
      case "invoice.paid":
      case "invoice.payment_succeeded":
      case "invoice.payment_failed":
        subId = invoiceSubscription(o);
        boxHint = invoiceBox(o);
        break;
      case "charge.refunded": {
        const invId = await stripe.invoiceForCharge(o);
        if (!invId) return { handled: true };
        refundedInvoice = await stripe.getInvoice(invId);
        subId = invoiceSubscription(refundedInvoice);
        boxHint = invoiceBox(refundedInvoice);
        break;
      }
      default:
        return { handled: true };
    }
    if (!subId) return { handled: true };
    const box = await r.lockBox({ boxId: boxHint, subscriptionId: subId });
    if (!box) return { handled: true };
    // Under the box's lock: the subscription as it is now.
    const sub = await stripe.getSubscription(subId);
    if (!sub || (sub.metadata?.box_id && sub.metadata.box_id !== box.id)) return { handled: true };

    const patch: BillingPatch = {};
    if (box.stripeSubscriptionId && box.stripeSubscriptionId !== sub.id) {
      const bound = await stripe.getSubscription(box.stripeSubscriptionId);
      if (bound && LIVE.has(bound.status)) {
        // A second subscription for a box that has one: cancel it now and
        // refund its first invoice (the outbox does it, retrying until done).
        if (LIVE.has(sub.status) || sub.status === "incomplete") {
          if (await r.outbox(box.id, "stripe_cancel_refund", `duplicate:${sub.id}`, { subscription: sub.id, why: "duplicate" })) {
            await r.log(box.id, "duplicate subscription: cancelling it and refunding its first invoice", sub.id, { bound: bound.id });
            await r.outbox(box.id, "duplicate_refunded", sub.id);
          }
        }
        return { handled: true };
      }
      // The bound subscription is over: a live new one replaces it (Renew); anything else is ignored.
      if (!LIVE.has(sub.status)) return { handled: true };
      patch.stripeSubscriptionId = sub.id;
      await r.log(box.id, "renewed: a new subscription replaces the ended one", sub.id, { previous: box.stripeSubscriptionId });
    } else if (!box.stripeSubscriptionId) {
      patch.stripeSubscriptionId = sub.id;
    }

    const customer = idOf(sub.customer);
    if (customer && customer !== box.stripeCustomerId) {
      patch.stripeCustomerId = customer;
      await r.saveCustomer(box.userId, box.email, customer);
    }
    if (ev.type === "checkout.session.completed") {
      const discounted = Number(o.total_details?.amount_discount ?? 0) > 0 || (Array.isArray(o.discounts) && o.discounts.length > 0);
      if (discounted && !box.founding) {
        patch.founding = true;
        await r.claimFounding(box.id);
      }
    }
    const founding = foundingNow(sub, now);
    if (founding !== undefined && founding !== (patch.founding ?? box.founding)) patch.founding = founding;

    const firstPaid = Boolean(box.firstPaidAt) || (latestPaid(sub) && (sub.status === "active" || sub.status === "trialing"));
    if (firstPaid && !box.firstPaidAt) patch.firstPaidAt = now;
    const on = extrasOn(sub.status, firstPaid);
    patch.planStatus = sub.status;
    Object.assign(patch, ending(sub), moneyFacts(sub));

    // A box stopped or deleted on purpose: record what Stripe says; its
    // subscription's end pauses nothing and sends no email.
    if (!UNMANAGED.has(box.status)) {
      if (on && box.status === "awaiting_payment") {
        patch.status = "paid";
        await r.outbox(box.id, "paid", "");
      }
      if (on && box.extrasPausedAt) {
        patch.extrasPausedAt = null;
        // Back on: the address returns once the box checks in from its own address (the worker checks).
        if ((box.dnsState === "removed" || box.dnsState === "parked") && (box.status === "active" || box.status === "cert_pending")) {
          await r.enqueue(box.id, "dns_set", { reason: "renewed", gen: box.generation });
        }
        await r.outbox(box.id, "extras_resumed", `${sub.id}:${now.toISOString().slice(0, 10)}`);
      }
      // Paused only once the box was paid for: an abandoned first payment pauses nothing.
      if (!on && !box.extrasPausedAt && (box.firstPaidAt || firstPaid)) {
        patch.extrasPausedAt = now;
        await r.outbox(box.id, "extras_paused", sub.id, { until: new Date(now.getTime() + DNS_GRACE_DAYS * 86_400_000).toISOString() });
      }
      if (ev.type === "invoice.payment_failed") await r.outbox(box.id, "payment_failed", String(o.id));
    }

    // A full refund of the first payment (ours, or made in Stripe's dashboard) ends the subscription.
    if (refundedInvoice && o.refunded === true && refundedInvoice.billing_reason === "subscription_create") {
      patch.refundedAt = now;
      await r.log(box.id, "first payment refunded", sub.id, { charge: o.id, amount: o.amount_refunded });
      if (LIVE.has(sub.status)) await r.outbox(box.id, "stripe_cancel", `refunded:${sub.id}`, { subscription: sub.id });
    }
    await r.update(box.id, patch);
    return { handled: true };
  });
}
