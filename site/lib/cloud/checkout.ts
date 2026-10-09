// One Checkout session per box, made safely again after anything fails.
//
// Stripe replays a request with the same idempotency key only when its
// parameters are exactly the same; otherwise it refuses (idempotency_error).
// Our parameters include expires_at, which changes with the clock. So the
// attempt (its key and its exact parameters) is saved, committed, before
// Stripe is called, and a retry sends that very request again: Stripe
// answers with the session it made, if it made one, whatever happened to
// our side afterwards (a lost answer, a failed database write).
//
// The attempt's expires_at is set when the attempt is made (now + 35
// minutes, CHECKOUT_TTL_SECONDS) and saved with it: Stripe wants at least 30
// minutes from when it receives the request, so the margin absorbs the
// commit and the network on the way (and a retry a few minutes later).
//
// A saved attempt is replaced by a new one only when it can no longer give a
// usable session (its session would expire within two minutes), or when
// Stripe refuses it as a request (it never ran there: no cached answer, so
// no session exists for it; e.g. its expires_at is now too close, or a
// parameter Stripe took when the attempt was saved is refused now). A
// refused attempt is built again from the current parameters rather than
// sent again as it was: what Stripe refused once, it refuses every time.
import { randomBytes } from "node:crypto";
import { StripeError } from "./stripe";

export type CheckoutAttempt = { id: string; key: string; params: Record<string, unknown>; at: string };

export type CheckoutBox = {
  id: string;
  checkout_url: string | null;
  checkout_expires_at: Date | null;
  checkout_attempt: CheckoutAttempt | null;
};

export type Session = { id: string; url: string; expires_at?: number };

export interface CheckoutRepo<B extends CheckoutBox = CheckoutBox> {
  /** Runs fn under the box's row lock; what save writes is committed when fn returns, before Stripe is called. */
  locked<T>(boxId: string, fn: (b: B, save: (a: CheckoutAttempt) => Promise<void>) => Promise<T>): Promise<T>;
  /** Records the session an attempt made, unless a newer attempt replaced it meanwhile. */
  saveSession(boxId: string, attemptId: string, cs: Session, expires: Date): Promise<void>;
}

export interface CheckoutStripe {
  createCheckout(params: Record<string, unknown>, idempotencyKey: string): Promise<Session>;
}

/** How long a new Checkout session lives, from when its attempt is made: Stripe's minimum (30 minutes) plus a margin. */
export const CHECKOUT_TTL_SECONDS = 35 * 60;

/** Whether a stored Checkout session can be handed out again (it lives about 35 minutes at Stripe). */
export function reusableCheckout(b: Pick<CheckoutBox, "checkout_url" | "checkout_expires_at">, now = new Date()): boolean {
  return Boolean(b.checkout_url && b.checkout_expires_at && b.checkout_expires_at.getTime() - now.getTime() > 2 * 60_000);
}

/** Whether a saved attempt can still give a usable session (its expires_at is more than two minutes away). */
export function attemptUsable(a: CheckoutAttempt | null, now = new Date()): a is CheckoutAttempt {
  const exp = Number(a?.params?.expires_at ?? 0);
  return Boolean(a && exp * 1000 - now.getTime() > 2 * 60_000);
}

/** Stripe refused the request itself (so it never ran, and made nothing), not an idempotency clash or an outage. */
const refusedRequest = (e: unknown) => e instanceof StripeError && e.status === 400 && e.type !== "idempotency_error";
const couponProblem = (e: unknown) => e instanceof StripeError && (Boolean(e.param?.startsWith("discounts")) || /coupon/i.test(e.message));

/**
 * The Checkout URL for a box: the stored session while it lasts; else the
 * saved attempt sent again; else a new attempt from build (which may ask
 * Stripe and the database things, under the box's lock), saved first.
 */
export async function checkoutUrl<B extends CheckoutBox>(
  repo: CheckoutRepo<B>,
  stripe: CheckoutStripe,
  boxId: string,
  build: (b: B) => Promise<{ params: Record<string, unknown>; discounted: boolean }>,
  now: () => Date = () => new Date(),
): Promise<string> {
  // The expiry is fixed here, when the attempt is made, and saved with it.
  const expiring = (params: Record<string, unknown>) => ({ ...params, expires_at: Math.floor(now().getTime() / 1000) + CHECKOUT_TTL_SECONDS });
  // A new attempt, saved: the given parameters, or (null) built again now.
  const fresh = async (params: Record<string, unknown> | null) =>
    repo.locked(boxId, async (b, save) => {
      const id = randomBytes(9).toString("base64url");
      const a: CheckoutAttempt = { id, key: `checkout:${boxId}:${id}`, params: expiring(params ?? (await build(b)).params), at: now().toISOString() };
      await save(a);
      return a;
    });
  type First = { url: string } | { attempt: CheckoutAttempt; discounted: boolean };
  const first = await repo.locked<First>(boxId, async (b, save) => {
    if (reusableCheckout(b, now())) return { url: b.checkout_url! };
    if (attemptUsable(b.checkout_attempt, now())) return { attempt: b.checkout_attempt, discounted: Array.isArray(b.checkout_attempt.params.discounts) };
    const { params, discounted } = await build(b);
    const id = randomBytes(9).toString("base64url");
    const a: CheckoutAttempt = { id, key: `checkout:${b.id}:${id}`, params: expiring(params), at: now().toISOString() };
    await save(a);
    return { attempt: a, discounted };
  });
  if ("url" in first) return first.url;
  let attempt = first.attempt;
  let cs: Session;
  try {
    cs = await stripe.createCheckout(attempt.params, attempt.key);
  } catch (e) {
    if (first.discounted && couponProblem(e)) {
      // The founding coupon ran out between the check and the session: full price.
      const { discounts: _, ...params } = attempt.params;
      attempt = await fresh(params);
    } else if (refusedRequest(e)) {
      // The saved request never ran at Stripe (it would have answered with
      // its cached result), so no session exists for it: built again from
      // the current parameters, with a fresh expiry.
      attempt = await fresh(null);
    } else throw e; // an outage or a lost answer: the next try sends the same request
    cs = await stripe.createCheckout(attempt.params, attempt.key);
  }
  const expires = new Date((cs.expires_at ?? Number(attempt.params.expires_at)) * 1000);
  await repo.saveSession(boxId, attempt.id, cs, expires);
  return cs.url;
}
