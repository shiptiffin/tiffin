import { beforeEach, describe, expect, test } from "bun:test";
import { paymentMethods, renewable } from "./actions";
import { checkoutUrl, type CheckoutAttempt, type CheckoutBox, type CheckoutRepo, type Session } from "./checkout";
import { StripeError } from "./stripe";

// Stripe's idempotency as documented: a key's first request that ran is
// cached (answer or error) and replayed for the same parameters; other
// parameters with that key are refused (idempotency_error); a request
// refused as invalid never ran, so nothing is cached for it.
class FakeStripe {
  cache = new Map<string, { params: string; result?: Session; error?: StripeError }>();
  sessions: Session[] = [];
  calls = 0;
  down = false; // the request never reaches Stripe
  loseAnswer = false; // Stripe runs it; the answer is lost on the way back
  couponGone = false;
  refused: string | null = null; // a parameter Stripe no longer takes
  constructor(readonly clock: () => Date) {}
  async createCheckout(params: Record<string, unknown>, key: string): Promise<Session> {
    this.calls++;
    if (this.down) throw new Error("fetch failed");
    const json = JSON.stringify(params);
    const hit = this.cache.get(key);
    if (hit) {
      if (hit.params !== json) throw new StripeError("Keys for idempotent requests can only be used with the same parameters they were first used with.", 400, undefined, undefined, "idempotency_error");
      if (hit.error) throw hit.error;
      return hit.result!;
    }
    const now = Math.floor(this.clock().getTime() / 1000);
    if (Number(params.expires_at) < now + 30 * 60 - 5) throw new StripeError("expires_at must be at least 30 minutes from now", 400, "parameter_invalid_integer", "expires_at", "invalid_request_error");
    if (this.refused && this.refused in params) throw new StripeError(`The \`${this.refused}\` parameter is no longer supported`, 400, undefined, this.refused, "invalid_request_error");
    if (this.couponGone && params.discounts) {
      const error = new StripeError("This coupon has reached its maximum redemptions", 400, "coupon_expired", "discounts[0][coupon]", "invalid_request_error");
      this.cache.set(key, { params: json, error });
      throw error;
    }
    const cs = { id: `cs_${this.sessions.length + 1}`, url: `https://checkout.stripe.test/c/${this.sessions.length + 1}`, expires_at: Number(params.expires_at) };
    this.sessions.push(cs);
    this.cache.set(key, { params: json, result: cs });
    if (this.loseAnswer) {
      this.loseAnswer = false;
      throw new Error("socket hang up");
    }
    return cs;
  }
}

class MemRepo implements CheckoutRepo {
  box: CheckoutBox & { checkout_session_id: string | null } = { id: "box_1", checkout_url: null, checkout_expires_at: null, checkout_attempt: null, checkout_session_id: null };
  failSave = false;
  async locked<T>(_id: string, fn: (b: CheckoutBox, save: (a: CheckoutAttempt) => Promise<void>) => Promise<T>): Promise<T> {
    let pending: CheckoutAttempt | null = null;
    const r = await fn(structuredClone(this.box), async (a) => {
      pending = structuredClone(a);
    });
    if (pending) this.box.checkout_attempt = pending; // committed with the transaction
    return r;
  }
  async saveSession(_id: string, attemptId: string, cs: Session, expires: Date) {
    if (this.failSave) {
      this.failSave = false;
      throw new Error("connection terminated");
    }
    if (this.box.checkout_attempt?.id !== attemptId) return;
    Object.assign(this.box, { checkout_session_id: cs.id, checkout_url: cs.url, checkout_expires_at: expires });
  }
}

let now: Date;
let stripe: FakeStripe;
let repo: MemRepo;
const clock = () => now;
const later = (min: number) => (now = new Date(now.getTime() + min * 60_000));
// Parameters as startCheckout builds them: expires_at follows the clock.
const build = async () => ({
  params: { mode: "subscription", line_items: [{ price: "price_1", quantity: 1 }], expires_at: Math.floor(now.getTime() / 1000) + 30 * 60, metadata: { box_id: "box_1" }, discounts: [{ coupon: "FOUNDING" }] },
  discounted: true,
});
const url = () => checkoutUrl(repo, stripe, "box_1", build, clock);

beforeEach(() => {
  now = new Date("2026-10-08T12:00:00Z");
  stripe = new FakeStripe(clock);
  repo = new MemRepo();
});

describe("Checkout retries", () => {
  test("Stripe made the session but its answer was lost: a retry minutes later gets that very session", async () => {
    stripe.loseAnswer = true;
    await expect(url()).rejects.toThrow("socket hang up");
    later(3);
    expect(await url()).toBe("https://checkout.stripe.test/c/1");
    expect(stripe.sessions.length).toBe(1);
    expect(repo.box.checkout_session_id).toBe("cs_1");
  });

  test("our database write after Stripe failed: the retry gets the same session, no second one", async () => {
    repo.failSave = true;
    await expect(url()).rejects.toThrow("connection terminated");
    later(1);
    expect(await url()).toBe("https://checkout.stripe.test/c/1");
    expect(stripe.sessions.length).toBe(1);
  });

  test("the request never reached Stripe: the retry sends a fresh expiry under a new key", async () => {
    stripe.down = true;
    await expect(url()).rejects.toThrow("fetch failed");
    const first = repo.box.checkout_attempt!;
    stripe.down = false;
    later(6); // past the 5-minute margin: Stripe refuses the saved expiry
    expect(await url()).toBe("https://checkout.stripe.test/c/1");
    expect(repo.box.checkout_attempt!.id).not.toBe(first.id);
    expect(stripe.sessions.length).toBe(1);
  });

  test("a stored session is handed out again (two tabs, one session); an expired one is replaced", async () => {
    const a = await url();
    expect(await url()).toBe(a);
    expect(stripe.calls).toBe(1);
    later(34);
    expect(await url()).not.toBe(a);
    expect(stripe.sessions.length).toBe(2);
  });

  test("a slow commit before Stripe still leaves the expiry at least 30 minutes away", async () => {
    // Every commit (the saved attempt) takes 20 seconds to reach Stripe.
    const locked = repo.locked.bind(repo);
    repo.locked = async (id, fn) => {
      const r = await locked(id, fn);
      later(20 / 60);
      return r;
    };
    expect(await url()).toBe("https://checkout.stripe.test/c/1");
    expect(stripe.calls).toBe(1);
    const exp = Number(repo.box.checkout_attempt!.params.expires_at);
    // The expiry was fixed when the attempt was made and is the one saved.
    expect(exp).toBe(Math.floor(new Date("2026-10-08T12:00:00Z").getTime() / 1000) + 35 * 60);
    // A retry two minutes later replays the same saved request, still valid at Stripe.
    repo.box.checkout_url = null;
    stripe.cache.clear();
    later(2);
    expect(await url()).toBe("https://checkout.stripe.test/c/2");
    expect(Number(repo.box.checkout_attempt!.params.expires_at)).toBe(exp);
  });

  test("a saved request Stripe now refuses (a parameter from before a deploy) is built again from the current parameters", async () => {
    // Saved by an earlier deploy, never sent (the request didn't reach Stripe).
    const stale = { ...(await build()).params, payment_method_types: ["card"], expires_at: Math.floor(now.getTime() / 1000) + 35 * 60 };
    repo.box.checkout_attempt = { id: "old", key: "checkout:box_1:old", params: stale, at: now.toISOString() };
    stripe.refused = "payment_method_types";
    later(1);
    expect(await url()).toBe("https://checkout.stripe.test/c/1");
    expect(repo.box.checkout_attempt!.id).not.toBe("old");
    expect(repo.box.checkout_attempt!.params.payment_method_types).toBeUndefined();
    // And the next click reuses the session rather than the refused request.
    expect(await url()).toBe("https://checkout.stripe.test/c/1");
    expect(stripe.sessions.length).toBe(1);
  });

  test("the founding coupon ran out between the check and the session: full price", async () => {
    stripe.couponGone = true;
    await url();
    expect(stripe.sessions.length).toBe(1);
    expect(repo.box.checkout_attempt!.params.discounts).toBeUndefined();
  });
});

test("Renew covers every box whose subscription ended, whatever stage it reached", () => {
  const b = (status: string, plan_status = "canceled", extra = {}) => ({ status, plan_status, first_paid_at: new Date(), refunded_at: null, ...extra }) as any;
  for (const s of ["paid", "provisioning", "cert_pending", "active", "failed"]) expect(renewable(b(s))).toBe(true);
  expect(renewable(b("active", "incomplete_expired"))).toBe(true);
  for (const s of ["awaiting_payment", "deleting", "deleted", "released"]) expect(renewable(b(s))).toBe(false);
  expect(renewable(b("active", "active"))).toBe(false);
  expect(renewable(b("active", "past_due"))).toBe(false);
  expect(renewable(b("failed", "canceled", { refunded_at: new Date() }))).toBe(false);
  expect(renewable(b("paid", "canceled", { first_paid_at: null }))).toBe(false);
});

test("Checkout's payment methods: the configuration when set, else cards only (never payment_method_types)", () => {
  expect(paymentMethods({ STRIPE_PAYMENT_METHODS: " pmc_123 " })).toEqual({ payment_method_configuration: "pmc_123" });
  expect(paymentMethods({})).toEqual({ allowed_payment_method_types: ["card"] });
  expect(paymentMethods({ STRIPE_PAYMENT_METHODS: "" })).toEqual({ allowed_payment_method_types: ["card"] });
});
