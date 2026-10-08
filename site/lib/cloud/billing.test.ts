import { beforeEach, describe, expect, test } from "bun:test";
import { extrasOn, handleEvent, invoiceSubscription, type BillingPatch, type BillingRepo, type BillingStripe, type BoxBilling, type StripeEvent, type SubscriptionNow } from "./billing";
import { formEncode, paymentIntentOf, signStripePayload, verifyStripeSignature } from "./stripe";

// Replays of Stripe's webhooks against an in-memory repo (what the Postgres
// one does) and a fake Stripe that answers what the subscription is now.

type MemBox = BoxBilling & { cancelAtPeriodEnd?: boolean; currentPeriodEnd?: Date | null; refundedAt?: Date };

class Mem implements BillingRepo {
  boxes = new Map<string, MemBox>();
  events = new Set<string>();
  founding = new Set<string>();
  customers = new Map<string, string>();
  jobs: { boxId: string; kind: string; args?: any }[] = [];
  out = new Map<string, Record<string, unknown>>();
  logs: { boxId: string; what: string; sub: string | null }[] = [];
  async transaction<T>(fn: (r: BillingRepo) => Promise<T>): Promise<T> {
    const snap = structuredClone({ boxes: [...this.boxes], events: [...this.events], founding: [...this.founding], jobs: this.jobs, out: [...this.out], logs: this.logs });
    try {
      return await fn(this);
    } catch (e) {
      this.boxes = new Map(snap.boxes);
      this.events = new Set(snap.events);
      this.founding = new Set(snap.founding);
      this.jobs = snap.jobs;
      this.out = new Map(snap.out);
      this.logs = snap.logs;
      throw e;
    }
  }
  async firstTime(id: string) {
    if (this.events.has(id)) return false;
    this.events.add(id);
    return true;
  }
  async lockBox({ boxId, subscriptionId }: { boxId?: string | null; subscriptionId?: string | null }) {
    if (boxId && this.boxes.has(boxId)) return { ...this.boxes.get(boxId)! };
    for (const b of this.boxes.values()) if (subscriptionId && b.stripeSubscriptionId === subscriptionId) return { ...b };
    return null;
  }
  async update(id: string, p: BillingPatch) {
    Object.assign(this.boxes.get(id)!, p);
  }
  async claimFounding(id: string) {
    this.founding.add(id);
  }
  async saveCustomer(userId: string, _email: string, c: string) {
    this.customers.set(userId, c);
  }
  async enqueue(boxId: string, kind: string, args?: unknown) {
    this.jobs.push({ boxId, kind, args });
  }
  async outbox(boxId: string, kind: string, key: string, params: Record<string, unknown> = {}) {
    const k = `${boxId}|${kind}|${key}`;
    if (this.out.has(k)) return false;
    this.out.set(k, params);
    return true;
  }
  async log(boxId: string, what: string, sub: string | null) {
    this.logs.push({ boxId, what, sub });
  }
  kinds(boxId = "box_1") {
    return [...this.out.keys()].filter((k) => k.startsWith(boxId + "|")).map((k) => k.split("|")[1]);
  }
}

// Stripe as it is now: subscriptions by id, invoices, charges.
class FakeStripe implements BillingStripe {
  subs = new Map<string, SubscriptionNow>();
  invoices = new Map<string, any>();
  calls = 0;
  set(id: string, status: string, latest: "paid" | "open" | null = "paid", over: Partial<SubscriptionNow> = {}) {
    this.subs.set(id, { id, status, customer: "cus_1", metadata: { box_id: "box_1" }, latest_invoice: latest && { id: `in_${id}`, status: latest, billing_reason: "subscription_create" }, ...over });
  }
  async getSubscription(id: string) {
    this.calls++;
    const s = this.subs.get(id);
    return s ? structuredClone(s) : null;
  }
  async getInvoice(id: string) {
    return this.invoices.get(id);
  }
  async invoiceForCharge(ch: any) {
    return ch.invoice ?? null;
  }
}

const box = (over: Partial<BoxBilling> = {}): MemBox => ({
  id: "box_1",
  userId: "u1",
  email: "sam@example.com",
  name: null,
  status: "awaiting_payment",
  planStatus: "none",
  firstPaidAt: null,
  extrasPausedAt: null,
  dnsState: "none",
  generation: 1,
  stripeCustomerId: null,
  stripeSubscriptionId: null,
  founding: false,
  ...over,
});

let t = 1_791_400_000;
const ev = (type: string, object: any, id = `evt_${++t}`, created = t): StripeEvent => ({ id, type, created, data: { object } });

const completed = (over: any = {}, id?: string) =>
  ev(
    "checkout.session.completed",
    {
      id: "cs_1",
      mode: "subscription",
      payment_status: "paid",
      status: "complete",
      metadata: { box_id: "box_1" },
      client_reference_id: "box_1",
      customer: "cus_1",
      subscription: "sub_1",
      total_details: { amount_discount: 700 },
      ...over,
    },
    id,
  );

let repo: Mem;
let stripe: FakeStripe;
const run = (e: StripeEvent) => handleEvent(repo, stripe, e);
const b1 = () => repo.boxes.get("box_1")!;

beforeEach(() => {
  repo = new Mem();
  stripe = new FakeStripe();
  repo.boxes.set("box_1", box());
});

describe("first payment", () => {
  test("a paid checkout marks the box paid, records the founding price and the customer", async () => {
    stripe.set("sub_1", "active");
    expect((await run(completed())).handled).toBe(true);
    expect(b1()).toMatchObject({ status: "paid", planStatus: "active", stripeSubscriptionId: "sub_1", founding: true, stripeCustomerId: "cus_1" });
    expect(b1().firstPaidAt).toBeInstanceOf(Date);
    expect(repo.founding.has("box_1")).toBe(true);
    expect(repo.customers.get("u1")).toBe("cus_1");
    expect(repo.kinds()).toEqual(["paid"]);
  });

  test("the same event twice changes nothing the second time", async () => {
    stripe.set("sub_1", "active");
    const e = completed();
    await run(e);
    b1().status = "provisioning";
    expect((await run(e)).handled).toBe(false);
    expect(b1().status).toBe("provisioning");
  });

  test("the return page and the webhook both confirming queue one email", async () => {
    stripe.set("sub_1", "active");
    await run(completed({}, "return:cs_1"));
    await run(completed());
    expect(repo.kinds()).toEqual(["paid"]);
  });

  test("a payment that hasn't gone through (incomplete, or active with the first invoice still open) sets nothing up", async () => {
    stripe.set("sub_1", "incomplete", "open");
    await run(completed({ payment_status: "unpaid" }));
    expect(b1()).toMatchObject({ status: "awaiting_payment", planStatus: "incomplete", firstPaidAt: null });
    // A delayed method can make Stripe call it active before the money arrives: still not paid.
    stripe.set("sub_1", "active", "open");
    await run(ev("customer.subscription.updated", { id: "sub_1", status: "active", metadata: { box_id: "box_1" } }));
    expect(b1().status).toBe("awaiting_payment");
    expect(extrasOn(b1().planStatus, Boolean(b1().firstPaidAt))).toBe(false);
    // ... and if that payment then fails, nothing was ever on, so nothing pauses.
    stripe.set("sub_1", "past_due", "open");
    await run(ev("invoice.payment_failed", { id: "in_sub_1", subscription: "sub_1" }));
    expect(b1()).toMatchObject({ status: "awaiting_payment", extrasPausedAt: null });
    // The money arrives.
    stripe.set("sub_1", "active", "paid");
    await run(ev("invoice.paid", { id: "in_sub_1", subscription: "sub_1" }));
    expect(b1().status).toBe("paid");
    expect(b1().firstPaidAt).toBeInstanceOf(Date);
  });

  test("an abandoned first payment pauses nothing", async () => {
    stripe.set("sub_x", "incomplete_expired", "open");
    await run(ev("customer.subscription.updated", { id: "sub_x", status: "incomplete_expired", metadata: { box_id: "box_1" } }));
    expect(b1()).toMatchObject({ extrasPausedAt: null, status: "awaiting_payment" });
  });

  test("full price: no founding claim", async () => {
    stripe.set("sub_1", "active");
    await run(completed({ total_details: { amount_discount: 0 } }));
    expect(repo.founding.size).toBe(0);
  });

  test("a one-off payment, an unknown box, or a subscription of another box is ignored", async () => {
    stripe.set("sub_1", "active");
    await run(completed({ mode: "payment" }));
    await run(completed({ metadata: { box_id: "box_nope" }, client_reference_id: "box_nope", subscription: "sub_9" }));
    stripe.set("sub_7", "active", "paid", { metadata: { box_id: "box_2" } });
    await run(completed({ subscription: "sub_7" }));
    expect(b1().status).toBe("awaiting_payment");
  });
});

describe("one subscription per box", () => {
  test("a second paid checkout for the same box is cancelled and refunded, and changes nothing else", async () => {
    stripe.set("sub_1", "active");
    stripe.set("sub_2", "active");
    await run(completed());
    await run(completed({ id: "cs_2", subscription: "sub_2" }));
    expect(b1().stripeSubscriptionId).toBe("sub_1");
    expect(repo.out.get("box_1|stripe_cancel_refund|duplicate:sub_2")).toEqual({ subscription: "sub_2", why: "duplicate" });
    expect(repo.kinds()).toContain("duplicate_refunded");
    expect(repo.logs[0]?.sub).toBe("sub_2");
    // Its later events don't touch the box: cancelling the duplicate can't pause the real one.
    stripe.set("sub_2", "canceled");
    await run(ev("customer.subscription.deleted", { id: "sub_2", status: "canceled", metadata: { box_id: "box_1" } }));
    expect(b1()).toMatchObject({ planStatus: "active", extrasPausedAt: null, stripeSubscriptionId: "sub_1" });
    // And a replay of the duplicate's completion queues nothing more.
    await run(completed({ id: "cs_2", subscription: "sub_2" }));
    expect([...repo.out.keys()].filter((k) => k.includes("stripe_cancel_refund"))).toHaveLength(1);
  });
});

describe("authoritative status", () => {
  beforeEach(async () => {
    stripe.set("sub_1", "active");
    await run(completed());
    Object.assign(b1(), { status: "active", name: "shop", dnsState: "live" });
  });

  test("a stale event can't bring back an older status (same second, any order)", async () => {
    stripe.set("sub_1", "canceled");
    await run(ev("customer.subscription.deleted", { id: "sub_1", status: "canceled" }, "evt_del", t));
    // A delayed "active" from before, with the same timestamp: Stripe says canceled.
    await run(ev("customer.subscription.updated", { id: "sub_1", status: "active" }, "evt_old", t));
    expect(b1().planStatus).toBe("canceled");
    expect(b1().extrasPausedAt).toBeInstanceOf(Date);
  });

  test("paying an old invoice after cancellation doesn't resurrect the extras", async () => {
    stripe.set("sub_1", "canceled");
    await run(ev("customer.subscription.deleted", { id: "sub_1", status: "canceled" }));
    const paused = b1().extrasPausedAt;
    await run(ev("invoice.paid", { id: "in_old", subscription: "sub_1" }));
    expect(b1()).toMatchObject({ planStatus: "canceled", extrasPausedAt: paused });
    expect(repo.jobs).toEqual([]);
  });

  test("a recovered customer isn't disabled by a delayed unpaid event", async () => {
    stripe.set("sub_1", "unpaid");
    await run(ev("customer.subscription.updated", { id: "sub_1", status: "unpaid" }, "evt_unpaid_1"));
    stripe.set("sub_1", "active");
    await run(ev("invoice.paid", { id: "in_2", subscription: "sub_1" }));
    expect(b1()).toMatchObject({ planStatus: "active", extrasPausedAt: null });
    // The delayed copy of an "unpaid" update arrives now.
    await run(ev("customer.subscription.updated", { id: "sub_1", status: "unpaid" }, "evt_unpaid_late"));
    expect(b1()).toMatchObject({ planStatus: "active", extrasPausedAt: null });
  });

  test("unpaid pauses the extras once, with the date the address goes", async () => {
    stripe.set("sub_1", "unpaid");
    await run(ev("customer.subscription.updated", { id: "sub_1", status: "unpaid" }));
    expect(b1().extrasPausedAt).toBeInstanceOf(Date);
    expect(b1().status).toBe("active"); // the box itself is never touched
    const until = new Date(String(repo.out.get("box_1|extras_paused|sub_1")!.until));
    expect(until.getTime() - b1().extrasPausedAt!.getTime()).toBe(30 * 86_400_000);
    stripe.set("sub_1", "canceled");
    await run(ev("customer.subscription.updated", { id: "sub_1", status: "canceled" }));
    expect(repo.kinds().filter((k) => k === "extras_paused")).toHaveLength(1);
  });

  test("past_due keeps the extras on (after a first payment)", async () => {
    stripe.set("sub_1", "past_due", "open");
    await run(ev("customer.subscription.updated", { id: "sub_1", status: "past_due" }));
    expect(b1().extrasPausedAt).toBeNull();
  });

  test("a failed payment emails once per invoice, only for the bound subscription", async () => {
    stripe.set("sub_1", "past_due", "open");
    await run(ev("invoice.payment_failed", { id: "in_9", parent: { subscription_details: { subscription: "sub_1" } } }));
    await run(ev("invoice.payment_failed", { id: "in_9", subscription: "sub_1" }));
    expect(repo.kinds().filter((k) => k === "payment_failed")).toHaveLength(1);
  });

  test("a killed address is not restored by paying", async () => {
    stripe.set("sub_1", "unpaid");
    await run(ev("customer.subscription.updated", { id: "sub_1", status: "unpaid" }));
    b1().dnsState = "killed";
    stripe.set("sub_1", "active");
    await run(ev("invoice.paid", { id: "in_1", subscription: "sub_1" }));
    expect(repo.jobs).toEqual([]);
  });

  test("cancel at period end is recorded, extras stay on", async () => {
    stripe.set("sub_1", "active", "paid", { cancel_at_period_end: true, items: { data: [{ current_period_end: 1_793_000_000 }] } });
    await run(ev("customer.subscription.updated", { id: "sub_1" }));
    expect(b1()).toMatchObject({ cancelAtPeriodEnd: true, extrasPausedAt: null });
    expect(b1().currentPeriodEnd?.getTime()).toBe(1_793_000_000_000);
  });
});

describe("renewing a cancelled box", () => {
  test("a new subscription for the same box replaces the ended one; the address comes back at the next check-in", async () => {
    stripe.set("sub_1", "active");
    await run(completed());
    Object.assign(b1(), { status: "active", name: "shop", dnsState: "live" });
    stripe.set("sub_1", "canceled");
    await run(ev("customer.subscription.deleted", { id: "sub_1" }));
    b1().dnsState = "removed"; // the grace period ran out
    stripe.set("sub_2", "active");
    await run(completed({ id: "cs_renew", subscription: "sub_2", total_details: {} }));
    expect(b1()).toMatchObject({ stripeSubscriptionId: "sub_2", planStatus: "active", extrasPausedAt: null, status: "active" });
    expect(repo.jobs).toEqual([{ boxId: "box_1", kind: "dns_set", args: { reason: "renewed", gen: 1 } }]);
    expect(repo.kinds()).toContain("extras_resumed");
    // The old subscription's stragglers are ignored.
    await run(ev("customer.subscription.updated", { id: "sub_1", status: "canceled" }));
    expect(b1().planStatus).toBe("active");
  });
  for (const status of ["paid", "failed"] as const) {
    test(`a ${status} box (never set up, or its setup failed) renews the same way, bound to the same box`, async () => {
      stripe.set("sub_1", "active");
      await run(completed());
      b1().status = status;
      stripe.set("sub_1", "canceled");
      await run(ev("customer.subscription.deleted", { id: "sub_1" }));
      expect(b1().extrasPausedAt).not.toBeNull();
      stripe.set("sub_2", "active");
      await run(completed({ id: "cs_renew", subscription: "sub_2", total_details: {} }));
      expect(b1()).toMatchObject({ stripeSubscriptionId: "sub_2", planStatus: "active", extrasPausedAt: null, status });
      expect(repo.jobs).toEqual([]); // no address to bring back
      expect(repo.boxes.size).toBe(1);
    });
  }
});

describe("refunds", () => {
  beforeEach(async () => {
    stripe.set("sub_1", "active");
    await run(completed());
    stripe.invoices.set("in_first", { id: "in_first", subscription: "sub_1", billing_reason: "subscription_create" });
    stripe.invoices.set("in_month2", { id: "in_month2", subscription: "sub_1", billing_reason: "subscription_cycle" });
  });

  test("a full refund of the first payment (from Stripe's dashboard) ends the subscription", async () => {
    await run(ev("charge.refunded", { id: "ch_1", invoice: "in_first", refunded: true, amount_refunded: 1200 }));
    expect(b1().refundedAt).toBeInstanceOf(Date);
    expect(repo.out.get("box_1|stripe_cancel|refunded:sub_1")).toEqual({ subscription: "sub_1" });
    // Stripe then cancels: extras pause, the address keeps its 30 days.
    stripe.set("sub_1", "canceled");
    await run(ev("customer.subscription.deleted", { id: "sub_1" }));
    expect(b1().extrasPausedAt).toBeInstanceOf(Date);
  });

  test("a partial refund, or one of a later month, only records", async () => {
    await run(ev("charge.refunded", { id: "ch_2", invoice: "in_first", refunded: false, amount_refunded: 100 }));
    await run(ev("charge.refunded", { id: "ch_3", invoice: "in_month2", refunded: true }));
    expect(repo.kinds()).not.toContain("stripe_cancel");
    expect(b1().refundedAt).toBeUndefined();
  });

  test("our own money-back refund (subscription already cancelled) queues nothing more", async () => {
    stripe.set("sub_1", "canceled");
    await run(ev("charge.refunded", { id: "ch_4", invoice: "in_first", refunded: true }));
    expect(repo.kinds()).not.toContain("stripe_cancel");
    expect(b1().refundedAt).toBeInstanceOf(Date);
  });
});

test("a failing handler leaves the event unrecorded, so Stripe's retry works", async () => {
  stripe.set("sub_1", "active");
  const e = completed();
  const orig = repo.update.bind(repo);
  repo.update = async () => {
    throw new Error("db down");
  };
  await expect(run(e)).rejects.toThrow("db down");
  repo.update = orig;
  expect((await run(e)).handled).toBe(true);
  expect(b1().status).toBe("paid");
});

test("both invoice shapes name the subscription and the payment", () => {
  expect(invoiceSubscription({ subscription: "sub_a" })).toBe("sub_a");
  expect(invoiceSubscription({ parent: { subscription_details: { subscription: "sub_b" } } })).toBe("sub_b");
  expect(invoiceSubscription({ subscription: { id: "sub_c" } })).toBe("sub_c");
  expect(paymentIntentOf({ payment_intent: "pi_1" })).toBe("pi_1");
  expect(paymentIntentOf({ payments: { data: [{ status: "paid", payment: { payment_intent: "pi_2" } }] } })).toBe("pi_2");
  expect(paymentIntentOf({})).toBeNull();
});

describe("stripe", () => {
  const secret = "whsec_test";
  const body = JSON.stringify({ id: "evt_1" });
  test("signature: valid, wrong secret, stale, changed body", () => {
    const now = 1_791_400_000;
    const h = signStripePayload(body, secret, now);
    expect(verifyStripeSignature(body, h, secret, now)).toBe(true);
    expect(verifyStripeSignature(body, `${h},v1=00`, secret, now)).toBe(true);
    expect(verifyStripeSignature(body, h, "whsec_other", now)).toBe(false);
    expect(verifyStripeSignature(body, h, secret, now + 301)).toBe(false);
    expect(verifyStripeSignature(body + " ", h, secret, now)).toBe(false);
    expect(verifyStripeSignature(body, null, secret, now)).toBe(false);
    expect(verifyStripeSignature(body, "t=abc,v1=", secret, now)).toBe(false);
  });
  test("form encoding as Stripe wants it", () => {
    expect(formEncode({ mode: "subscription", line_items: [{ price: "price_1", quantity: 1 }], metadata: { box_id: "box_1" }, payment_method_types: ["card"], skip: undefined })).toBe(
      "mode=subscription&line_items%5B0%5D%5Bprice%5D=price_1&line_items%5B0%5D%5Bquantity%5D=1&metadata%5Bbox_id%5D=box_1&payment_method_types%5B0%5D=card",
    );
  });
});
