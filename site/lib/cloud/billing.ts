// What Stripe's webhooks do to a box. Stripe is the source of truth for
// billing; this keeps our copy in step. Each event is handled once (its id
// is recorded in the same transaction as its writes, so a retried delivery
// changes nothing), and an older event never overwrites a newer status.
//
// Billing only ever switches the managed extras (automatic updates,
// monitoring alerts, the shiptiffin.app address after a grace period). The
// customer's server and apps are theirs and are never touched.

export const EXTRAS_ON = new Set(["active", "trialing", "past_due"]);
/** Days the shiptiffin.app address stays after the extras pause. */
export const DNS_GRACE_DAYS = 30;

export type BoxBilling = {
  id: string;
  userId: string;
  email: string;
  name: string | null;
  status: string;
  planStatus: string;
  planStatusAt: Date | null;
  extrasPausedAt: Date | null;
  dnsState: string;
  stripeCustomerId: string | null;
  stripeSubscriptionId: string | null;
  founding: boolean;
};

export type BillingPatch = Partial<
  Pick<BoxBilling, "status" | "planStatus" | "planStatusAt" | "extrasPausedAt" | "stripeCustomerId" | "stripeSubscriptionId" | "founding">
> & { cancelAtPeriodEnd?: boolean; currentPeriodEnd?: Date | null; checkoutSessionId?: string };

export interface BillingRepo {
  transaction<T>(fn: (r: BillingRepo) => Promise<T>): Promise<T>;
  /** Records the event; false when it was handled before. */
  firstTime(id: string, type: string, created: Date): Promise<boolean>;
  findBox(q: { boxId?: string | null; subscriptionId?: string | null }): Promise<BoxBilling | null>;
  update(boxId: string, patch: BillingPatch): Promise<void>;
  claimFounding(boxId: string): Promise<void>;
  saveCustomer(userId: string, email: string, customerId: string): Promise<void>;
  enqueue(boxId: string, kind: "dns_set" | "dns_remove", args?: Record<string, unknown>): Promise<void>;
  /** True the first time (box, kind, key) is asked: an email goes once. */
  emailOnce(boxId: string, kind: string, key: string): Promise<boolean>;
}

/** An email to send once the transaction committed. */
export type Notice = { box: BoxBilling; kind: "paid" | "extras_paused" | "extras_resumed" | "payment_failed"; until?: Date };

export type StripeEvent = { id: string; type: string; created: number; data: { object: any } };

const subId = (s: unknown): string | null => (typeof s === "string" ? s : s && typeof s === "object" && "id" in s ? String((s as any).id) : null);

/** The subscription an invoice belongs to (old and new API shapes). */
export function invoiceSubscription(inv: any): string | null {
  return subId(inv?.subscription) ?? subId(inv?.parent?.subscription_details?.subscription) ?? null;
}

/** The end of the current period (old and new API shapes). */
function periodEnd(sub: any): Date | null {
  const t = sub?.current_period_end ?? sub?.items?.data?.[0]?.current_period_end;
  return typeof t === "number" ? new Date(t * 1000) : null;
}

/** The box changes that follow a new plan status, with the notices they send. */
async function applyStatus(r: BillingRepo, box: BoxBilling, status: string, at: Date, extra: BillingPatch, notices: Notice[]) {
  if (box.planStatusAt && at < box.planStatusAt) return; // an older event: what we have is newer
  const patch: BillingPatch = { ...extra, planStatus: status, planStatusAt: at };
  const on = EXTRAS_ON.has(status);
  if (on && box.status === "awaiting_payment") patch.status = "paid";
  if (on && box.extrasPausedAt) {
    patch.extrasPausedAt = null;
    if (box.dnsState === "removed" && box.status === "active") await r.enqueue(box.id, "dns_set");
    if (await r.emailOnce(box.id, "extras_resumed", at.toISOString().slice(0, 10))) notices.push({ box, kind: "extras_resumed" });
  }
  // Paused only once the box was paid for: an abandoned first payment pauses nothing.
  if (!on && !box.extrasPausedAt && box.status !== "awaiting_payment") {
    patch.extrasPausedAt = at;
    const until = new Date(at.getTime() + DNS_GRACE_DAYS * 86_400_000);
    if (await r.emailOnce(box.id, "extras_paused", at.toISOString())) notices.push({ box, kind: "extras_paused", until });
  }
  await r.update(box.id, patch);
}

/** Handles one verified event. Returns the emails to send after it committed. */
export async function handleEvent(repo: BillingRepo, ev: StripeEvent): Promise<{ handled: boolean; notices: Notice[] }> {
  return repo.transaction(async (r) => {
    const at = new Date(ev.created * 1000);
    if (!(await r.firstTime(ev.id, ev.type, at))) return { handled: false, notices: [] };
    const o = ev.data.object;
    const notices: Notice[] = [];
    switch (ev.type) {
      case "checkout.session.completed": {
        if (o.mode !== "subscription") break;
        const box = await r.findBox({ boxId: o.metadata?.box_id ?? o.client_reference_id });
        if (!box) break;
        const customer = subId(o.customer);
        const subscription = subId(o.subscription);
        const discounted = Number(o.total_details?.amount_discount ?? 0) > 0 || (Array.isArray(o.discounts) && o.discounts.length > 0);
        const extra: BillingPatch = { stripeCustomerId: customer, stripeSubscriptionId: subscription, checkoutSessionId: o.id };
        if (discounted && !box.founding) {
          extra.founding = true;
          await r.claimFounding(box.id);
        }
        if (customer) await r.saveCustomer(box.userId, box.email, customer);
        const paid = o.payment_status === "paid" || o.payment_status === "no_payment_required";
        if (paid) {
          if (box.status === "awaiting_payment" && (await r.emailOnce(box.id, "paid", ""))) notices.push({ box, kind: "paid" });
          await applyStatus(r, box, "active", at, extra, notices);
        } else await r.update(box.id, extra);
        break;
      }
      case "customer.subscription.created":
      case "customer.subscription.updated":
      case "customer.subscription.deleted": {
        const box = await r.findBox({ boxId: o.metadata?.box_id, subscriptionId: o.id });
        if (!box) break;
        const status = ev.type === "customer.subscription.deleted" ? "canceled" : String(o.status);
        await applyStatus(r, box, status, at, {
          stripeSubscriptionId: box.stripeSubscriptionId ?? o.id,
          stripeCustomerId: box.stripeCustomerId ?? subId(o.customer),
          cancelAtPeriodEnd: Boolean(o.cancel_at_period_end),
          currentPeriodEnd: periodEnd(o),
        }, notices);
        break;
      }
      case "invoice.paid": {
        const sub = invoiceSubscription(o);
        const box = await r.findBox({ subscriptionId: sub, boxId: o.parent?.subscription_details?.metadata?.box_id ?? o.subscription_details?.metadata?.box_id });
        if (!box) break;
        if (!EXTRAS_ON.has(box.planStatus) || box.status === "awaiting_payment") await applyStatus(r, box, "active", at, {}, notices);
        break;
      }
      case "invoice.payment_failed": {
        const box = await r.findBox({ subscriptionId: invoiceSubscription(o), boxId: o.parent?.subscription_details?.metadata?.box_id });
        if (!box) break;
        if (await r.emailOnce(box.id, "payment_failed", String(o.id))) notices.push({ box, kind: "payment_failed" });
        break;
      }
    }
    return { handled: true, notices };
  });
}
