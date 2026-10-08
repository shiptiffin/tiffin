import { beforeEach, describe, expect, test } from "bun:test";
import { handleEvent, invoiceSubscription, type BillingPatch, type BillingRepo, type BoxBilling, type StripeEvent } from "./billing";
import { formEncode, signStripePayload, verifyStripeSignature } from "./stripe";

// An in-memory BillingRepo: what the Postgres one does, without Postgres.
class Mem implements BillingRepo {
  boxes = new Map<string, BoxBilling & { cancelAtPeriodEnd?: boolean; currentPeriodEnd?: Date | null }>();
  events = new Set<string>();
  founding = new Set<string>();
  customers = new Map<string, string>();
  jobs: { boxId: string; kind: string; args?: unknown }[] = [];
  emails = new Set<string>();
  async transaction<T>(fn: (r: BillingRepo) => Promise<T>): Promise<T> {
    // Roll back on error, as a transaction would.
    const snap = structuredClone({ boxes: [...this.boxes], events: [...this.events], founding: [...this.founding], jobs: this.jobs, emails: [...this.emails] });
    try {
      return await fn(this);
    } catch (e) {
      this.boxes = new Map(snap.boxes);
      this.events = new Set(snap.events);
      this.founding = new Set(snap.founding);
      this.jobs = snap.jobs;
      this.emails = new Set(snap.emails);
      throw e;
    }
  }
  async firstTime(id: string) {
    if (this.events.has(id)) return false;
    this.events.add(id);
    return true;
  }
  async findBox({ boxId, subscriptionId }: { boxId?: string | null; subscriptionId?: string | null }) {
    if (boxId && this.boxes.has(boxId)) return { ...this.boxes.get(boxId)! };
    for (const b of this.boxes.values()) if (subscriptionId && b.stripeSubscriptionId === subscriptionId) return { ...b };
    return null;
  }
  async update(id: string, p: BillingPatch) {
    const b = this.boxes.get(id)!;
    Object.assign(b, Object.fromEntries(Object.entries(p).filter(([k]) => k !== "checkoutSessionId")));
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
  async emailOnce(boxId: string, kind: string, key: string) {
    const k = `${boxId}|${kind}|${key}`;
    if (this.emails.has(k)) return false;
    this.emails.add(k);
    return true;
  }
}

const box = (over: Partial<BoxBilling> = {}): BoxBilling => ({
  id: "box_1",
  userId: "u1",
  email: "sam@example.com",
  name: null,
  status: "awaiting_payment",
  planStatus: "none",
  planStatusAt: null,
  extrasPausedAt: null,
  dnsState: "none",
  stripeCustomerId: null,
  stripeSubscriptionId: null,
  founding: false,
  ...over,
});

let t = 1_791_400_000;
const ev = (type: string, object: any, id = `evt_${++t}`, created = t): StripeEvent => ({ id, type, created, data: { object } });

const completed = (over: any = {}) =>
  ev("checkout.session.completed", {
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
  });

let repo: Mem;
beforeEach(() => {
  repo = new Mem();
  repo.boxes.set("box_1", box());
});

describe("checkout.session.completed", () => {
  test("marks the box paid, records the founding price and the customer", async () => {
    const r = await handleEvent(repo, completed());
    const b = repo.boxes.get("box_1")!;
    expect(r.handled).toBe(true);
    expect(b.status).toBe("paid");
    expect(b.planStatus).toBe("active");
    expect(b.stripeSubscriptionId).toBe("sub_1");
    expect(b.founding).toBe(true);
    expect(repo.founding.has("box_1")).toBe(true);
    expect(repo.customers.get("u1")).toBe("cus_1");
    expect(r.notices.map((n) => n.kind)).toEqual(["paid"]);
  });

  test("the same event twice changes nothing the second time", async () => {
    const e = completed();
    await handleEvent(repo, e);
    repo.boxes.get("box_1")!.status = "provisioning";
    const again = await handleEvent(repo, e);
    expect(again.handled).toBe(false);
    expect(repo.boxes.get("box_1")!.status).toBe("provisioning");
  });

  test("the return page and the webhook both confirming send one email", async () => {
    const a = await handleEvent(repo, completed({}, "return:cs_1"));
    const b = await handleEvent(repo, completed());
    expect([...a.notices, ...b.notices].filter((n) => n.kind === "paid")).toHaveLength(1);
    expect(repo.boxes.get("box_1")!.status).toBe("paid");
  });

  test("full price: no founding claim", async () => {
    await handleEvent(repo, completed({ total_details: { amount_discount: 0 } }));
    expect(repo.founding.size).toBe(0);
    expect(repo.boxes.get("box_1")!.founding).toBe(false);
  });

  test("an unknown box or a one-off payment is ignored", async () => {
    expect((await handleEvent(repo, completed({ metadata: { box_id: "box_nope" }, client_reference_id: "box_nope" }))).notices).toEqual([]);
    await handleEvent(repo, completed({ mode: "payment" }));
    expect(repo.boxes.get("box_1")!.status).toBe("awaiting_payment");
  });
});

describe("subscription changes", () => {
  beforeEach(async () => {
    await handleEvent(repo, completed());
    Object.assign(repo.boxes.get("box_1")!, { status: "active", name: "shop", dnsState: "live" });
  });

  test("unpaid pauses the extras once, and says until when the address stays", async () => {
    const r = await handleEvent(repo, ev("customer.subscription.updated", { id: "sub_1", status: "unpaid", metadata: { box_id: "box_1" }, cancel_at_period_end: false }));
    const b = repo.boxes.get("box_1")!;
    expect(b.planStatus).toBe("unpaid");
    expect(b.extrasPausedAt).toBeInstanceOf(Date);
    expect(b.status).toBe("active"); // the box itself is never touched
    expect(r.notices[0]?.kind).toBe("extras_paused");
    expect(r.notices[0]!.until!.getTime() - b.extrasPausedAt!.getTime()).toBe(30 * 86_400_000);
    const again = await handleEvent(repo, ev("customer.subscription.updated", { id: "sub_1", status: "canceled" }));
    expect(again.notices).toEqual([]);
    expect(repo.jobs).toEqual([]);
  });

  test("an older event never overwrites a newer status", async () => {
    await handleEvent(repo, ev("customer.subscription.updated", { id: "sub_1", status: "past_due" }, "evt_new", t + 100));
    await handleEvent(repo, ev("customer.subscription.updated", { id: "sub_1", status: "active" }, "evt_old", t + 50));
    expect(repo.boxes.get("box_1")!.planStatus).toBe("past_due");
  });

  test("past_due keeps the extras on", async () => {
    await handleEvent(repo, ev("customer.subscription.updated", { id: "sub_1", status: "past_due" }));
    expect(repo.boxes.get("box_1")!.extrasPausedAt).toBeNull();
  });

  test("deleted cancels; paying again restores the address", async () => {
    await handleEvent(repo, ev("customer.subscription.deleted", { id: "sub_1", status: "canceled" }));
    expect(repo.boxes.get("box_1")!.planStatus).toBe("canceled");
    repo.boxes.get("box_1")!.dnsState = "removed"; // the grace period ran out
    const r = await handleEvent(repo, ev("customer.subscription.updated", { id: "sub_1", status: "active" }));
    expect(repo.boxes.get("box_1")!.extrasPausedAt).toBeNull();
    expect(repo.jobs).toEqual([{ boxId: "box_1", kind: "dns_set", args: undefined }]);
    expect(r.notices.map((n) => n.kind)).toEqual(["extras_resumed"]);
  });

  test("a killed address is not restored by paying", async () => {
    await handleEvent(repo, ev("customer.subscription.updated", { id: "sub_1", status: "unpaid" }));
    repo.boxes.get("box_1")!.dnsState = "killed";
    await handleEvent(repo, ev("invoice.paid", { id: "in_1", subscription: "sub_1" }));
    expect(repo.jobs).toEqual([]);
  });

  test("cancel at period end is recorded, extras stay on", async () => {
    await handleEvent(repo, ev("customer.subscription.updated", { id: "sub_1", status: "active", cancel_at_period_end: true, items: { data: [{ current_period_end: 1_793_000_000 }] } }));
    const b = repo.boxes.get("box_1")!;
    expect(b.cancelAtPeriodEnd).toBe(true);
    expect(b.currentPeriodEnd?.getTime()).toBe(1_793_000_000_000);
    expect(b.extrasPausedAt).toBeNull();
  });
});

describe("invoices", () => {
  beforeEach(async () => {
    await handleEvent(repo, completed());
  });

  test("a failed payment emails once per invoice", async () => {
    const a = await handleEvent(repo, ev("invoice.payment_failed", { id: "in_9", parent: { subscription_details: { subscription: "sub_1" } } }));
    const b = await handleEvent(repo, ev("invoice.payment_failed", { id: "in_9", subscription: "sub_1" }));
    expect(a.notices.map((n) => n.kind)).toEqual(["payment_failed"]);
    expect(b.notices).toEqual([]);
  });

  test("invoice.paid after unpaid turns the extras back on", async () => {
    await handleEvent(repo, ev("customer.subscription.updated", { id: "sub_1", status: "unpaid" }));
    await handleEvent(repo, ev("invoice.paid", { id: "in_2", subscription: "sub_1" }));
    expect(repo.boxes.get("box_1")!.planStatus).toBe("active");
    expect(repo.boxes.get("box_1")!.extrasPausedAt).toBeNull();
  });

  test("both invoice shapes name the subscription", () => {
    expect(invoiceSubscription({ subscription: "sub_a" })).toBe("sub_a");
    expect(invoiceSubscription({ parent: { subscription_details: { subscription: "sub_b" } } })).toBe("sub_b");
    expect(invoiceSubscription({ subscription: { id: "sub_c" } })).toBe("sub_c");
  });
});

test("an abandoned first payment pauses nothing", async () => {
  await handleEvent(repo, ev("customer.subscription.updated", { id: "sub_x", status: "incomplete_expired", metadata: { box_id: "box_1" } }));
  const b = repo.boxes.get("box_1")!;
  expect(b.extrasPausedAt).toBeNull();
  expect(b.status).toBe("awaiting_payment");
});

test("a failing handler leaves the event unrecorded, so Stripe's retry works", async () => {
  const e = completed();
  const orig = repo.update.bind(repo);
  repo.update = async () => {
    throw new Error("db down");
  };
  await expect(handleEvent(repo, e)).rejects.toThrow("db down");
  repo.update = orig;
  expect((await handleEvent(repo, e)).handled).toBe(true);
  expect(repo.boxes.get("box_1")!.status).toBe("paid");
});

describe("stripe", () => {
  const secret = "whsec_test";
  const body = JSON.stringify({ id: "evt_1" });
  test("signature: valid, wrong secret, stale, changed body", () => {
    const now = 1_791_400_000;
    const h = signStripePayload(body, secret, now);
    expect(verifyStripeSignature(body, h, secret, now)).toBe(true);
    expect(verifyStripeSignature(body, `${h},v1=00`, secret, now)).toBe(true); // several signatures: any may match
    expect(verifyStripeSignature(body, h, "whsec_other", now)).toBe(false);
    expect(verifyStripeSignature(body, h, secret, now + 301)).toBe(false);
    expect(verifyStripeSignature(body + " ", h, secret, now)).toBe(false);
    expect(verifyStripeSignature(body, null, secret, now)).toBe(false);
    expect(verifyStripeSignature(body, "t=abc,v1=", secret, now)).toBe(false);
  });
  test("form encoding as Stripe wants it", () => {
    expect(formEncode({ mode: "subscription", line_items: [{ price: "price_1", quantity: 1 }], metadata: { box_id: "box_1" }, discounts: [{ coupon: "founding-v1" }], skip: undefined })).toBe(
      "mode=subscription&line_items%5B0%5D%5Bprice%5D=price_1&line_items%5B0%5D%5Bquantity%5D=1&metadata%5Bbox_id%5D=box_1&discounts%5B0%5D%5Bcoupon%5D=founding-v1",
    );
  });
});
