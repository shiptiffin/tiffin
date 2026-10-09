// A small Stripe client: the handful of calls the control plane makes, form
// encoded as Stripe's API wants, and the webhook signature check. Test mode
// until the owner says otherwise; the keys come from the project's secrets.
import { createHmac, timingSafeEqual } from "node:crypto";

export const STRIPE_API = "https://api.stripe.com/v1";
/**
 * The API version every request asks for (the Stripe-Version header), so a
 * change of the account's default version in Stripe's dashboard can't change
 * what this code gets. The version the code and its tests were checked
 * against; move it on deliberately (docs.stripe.com/upgrades), with the
 * webhook endpoint's version.
 */
export const STRIPE_API_VERSION = "2026-09-30.endive";
export const PRICE_LOOKUP_KEY = "box_monthly_v1";
export const FOUNDING_LIMIT = 100;

export class StripeError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
    readonly param?: string,
    /** Stripe's error type (idempotency_error, invalid_request_error, …). */
    readonly type?: string,
  ) {
    super(message);
  }
}

/** Stripe's form encoding: nested objects as a[b][c], arrays as a[0]. */
export function formEncode(obj: Record<string, unknown>, prefix = ""): string {
  const parts: string[] = [];
  const walk = (v: unknown, key: string) => {
    if (v === undefined || v === null) return;
    if (Array.isArray(v)) v.forEach((x, i) => walk(x, `${key}[${i}]`));
    else if (typeof v === "object") for (const [k, x] of Object.entries(v as Record<string, unknown>)) walk(x, `${key}[${k}]`);
    else parts.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(v))}`);
  };
  for (const [k, v] of Object.entries(obj)) walk(v, prefix ? `${prefix}[${k}]` : k);
  return parts.join("&");
}

export type Stripe = ReturnType<typeof stripeClient>;

export function stripeClient(secretKey: string, base = process.env.STRIPE_API_BASE || STRIPE_API) {
  async function req<T = any>(method: "GET" | "POST" | "DELETE", path: string, params?: Record<string, unknown>, idempotencyKey?: string): Promise<T> {
    let url = base.replace(/\/+$/, "") + path;
    const headers: Record<string, string> = { Authorization: `Bearer ${secretKey}`, "Stripe-Version": STRIPE_API_VERSION };
    let body: string | undefined;
    if (params && method === "GET") url += (url.includes("?") ? "&" : "?") + formEncode(params);
    else if (params) {
      body = formEncode(params);
      headers["Content-Type"] = "application/x-www-form-urlencoded";
    }
    if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
    const res = await fetch(url, { method, headers, body, signal: AbortSignal.timeout(20_000) });
    const json = (await res.json().catch(() => ({}))) as any;
    if (!res.ok) {
      const e = json?.error ?? {};
      throw new StripeError(e.message ?? `Stripe answered ${res.status}`, res.status, e.code, e.param, e.type);
    }
    return json as T;
  }

  let priceCache: { id: string; at: number } | null = null;

  return {
    req,
    /** The monthly price: STRIPE_PRICE_MONTHLY, else the active price with lookup key box_monthly_v1. */
    async monthlyPrice(): Promise<string> {
      const fixed = process.env.STRIPE_PRICE_MONTHLY?.trim();
      if (fixed) return fixed;
      if (priceCache && Date.now() - priceCache.at < 10 * 60_000) return priceCache.id;
      const list = await req<{ data: { id: string }[] }>("GET", "/prices", { lookup_keys: [PRICE_LOOKUP_KEY], active: true, limit: 1 });
      const id = list.data[0]?.id;
      if (!id) throw new StripeError(`No active Stripe price with lookup key ${PRICE_LOOKUP_KEY}`, 500);
      priceCache = { id, at: Date.now() };
      return id;
    },
    /** Whether the founding coupon can still be redeemed (valid and under its own limit). */
    async couponOpen(coupon: string): Promise<boolean> {
      try {
        const c = await req<{ valid: boolean; times_redeemed: number; max_redemptions: number | null }>("GET", `/coupons/${encodeURIComponent(coupon)}`);
        return c.valid && (c.max_redemptions == null || c.times_redeemed < c.max_redemptions);
      } catch {
        return false;
      }
    },
    createCheckout(params: Record<string, unknown>, idempotencyKey: string) {
      return req<{ id: string; url: string; expires_at?: number }>("POST", "/checkout/sessions", params, idempotencyKey);
    },
    expireCheckout(id: string) {
      return req<any>("POST", `/checkout/sessions/${encodeURIComponent(id)}/expire`);
    },
    /** The subscription as Stripe has it now (the latest invoice and discounts expanded), or null when it doesn't exist. */
    /** Whether a customer exists in this key's mode (a test-mode id is unknown to live, and the reverse). */
    async customerExists(id: string): Promise<boolean> {
      try {
        const c = await req<{ deleted?: boolean }>("GET", `/customers/${encodeURIComponent(id)}`);
        return !c.deleted;
      } catch (e) {
        if (e instanceof StripeError && e.status === 404) return false;
        throw e;
      }
    },
    async getSubscription(id: string): Promise<StripeSubscription | null> {
      try {
        return await req<StripeSubscription>("GET", `/subscriptions/${encodeURIComponent(id)}`, { expand: ["latest_invoice", "discounts"] });
      } catch (e) {
        if (e instanceof StripeError && e.status === 404) return null;
        throw e;
      }
    },
    getInvoice(id: string) {
      return req<any>("GET", `/invoices/${encodeURIComponent(id)}`, { expand: ["payments"] });
    },
    listInvoices(subscription: string) {
      return req<{ data: any[] }>("GET", "/invoices", { subscription, limit: 100 });
    },
    /** The invoice a charge paid, in both API shapes (charge.invoice, or the invoice payment of its payment intent). */
    async invoiceForCharge(charge: any): Promise<string | null> {
      const direct = typeof charge?.invoice === "string" ? charge.invoice : charge?.invoice?.id;
      if (direct) return direct;
      const pi = typeof charge?.payment_intent === "string" ? charge.payment_intent : charge?.payment_intent?.id;
      if (!pi) return null;
      const r = await req<{ data: { invoice: string | { id: string } }[] }>("GET", "/invoice_payments", { payment: { type: "payment_intent", payment_intent: pi }, limit: 1 });
      const inv = r.data[0]?.invoice;
      return typeof inv === "string" ? inv : (inv?.id ?? null);
    },
    refund(params: { payment_intent?: string; charge?: string; metadata?: Record<string, string> }, idempotencyKey: string) {
      return req<{ id: string; status: string; amount: number }>("POST", "/refunds", params, idempotencyKey);
    },
    getCheckout(id: string) {
      return req<any>("GET", `/checkout/sessions/${encodeURIComponent(id)}`);
    },
    portal(customer: string, returnUrl: string) {
      return req<{ url: string }>("POST", "/billing_portal/sessions", { customer, return_url: returnUrl });
    },
    cancelAtPeriodEnd(subscription: string, cancel: boolean) {
      return req<any>("POST", `/subscriptions/${encodeURIComponent(subscription)}`, { cancel_at_period_end: cancel });
    },
    cancelNow(subscription: string) {
      return req<any>("DELETE", `/subscriptions/${encodeURIComponent(subscription)}`);
    },
    /**
     * Cancels a subscription now and refunds its first invoice in full: a
     * duplicate subscription for a box, or the 14-day money-back guarantee.
     * Safe to repeat (Stripe idempotency keys; an ended subscription and a
     * refunded payment are left as they are). Returns what it did.
     */
    async cancelAndRefund(subscription: string, key: string, metadata: Record<string, string>): Promise<{ canceled: boolean; refund: string | null; amount: number }> {
      let canceled = false;
      try {
        const sub = await req<any>("GET", `/subscriptions/${encodeURIComponent(subscription)}`);
        if (sub.status !== "canceled" && sub.status !== "incomplete_expired") {
          await req<any>("DELETE", `/subscriptions/${encodeURIComponent(subscription)}`, undefined, `cancel:${key}`);
          canceled = true;
        }
      } catch (e) {
        if (!(e instanceof StripeError && e.status === 404)) throw e;
      }
      const invoices = await req<{ data: any[] }>("GET", "/invoices", { subscription, limit: 100 });
      const first = invoices.data.find((i) => i.billing_reason === "subscription_create") ?? invoices.data.at(-1);
      if (!first || !(first.amount_paid > 0)) return { canceled, refund: null, amount: 0 };
      const inv = await req<any>("GET", `/invoices/${encodeURIComponent(first.id)}`, { expand: ["payments"] });
      const pi = paymentIntentOf(inv);
      const charge = typeof inv.charge === "string" ? inv.charge : null;
      if (!pi && !charge) throw new StripeError(`invoice ${first.id} has no payment to refund`, 500);
      try {
        const r = await req<{ id: string; amount: number }>("POST", "/refunds", { ...(pi ? { payment_intent: pi } : { charge: charge! }), metadata }, `refund:${key}`);
        return { canceled, refund: r.id, amount: r.amount };
      } catch (e) {
        if (e instanceof StripeError && e.code === "charge_already_refunded") return { canceled, refund: "already", amount: 0 };
        throw e;
      }
    },
  };
}

export type StripeSubscription = {
  id: string;
  status: string;
  customer: string | { id: string };
  metadata?: Record<string, string>;
  cancel_at_period_end?: boolean;
  cancel_at?: number | null;
  current_period_end?: number;
  items?: { data?: { current_period_end?: number }[] };
  latest_invoice?: string | { id: string; status: string; billing_reason?: string; amount_paid?: number; currency?: string; status_transitions?: { paid_at?: number | null } } | null;
  canceled_at?: number | null;
  ended_at?: number | null;
  discounts?: (string | { end?: number | null })[];
};

/** An invoice's payment intent, in both API shapes. */
export function paymentIntentOf(inv: any): string | null {
  const direct = typeof inv?.payment_intent === "string" ? inv.payment_intent : inv?.payment_intent?.id;
  if (direct) return direct;
  for (const p of inv?.payments?.data ?? []) {
    const pi = p?.payment?.payment_intent;
    if (pi && (p.status === "paid" || p.status === undefined)) return typeof pi === "string" ? pi : pi.id;
  }
  return null;
}

/**
 * Checks a webhook's Stripe-Signature header ("t=<unix>,v1=<hex>,...") over
 * the raw body with the endpoint's secret, within `toleranceSec`.
 */
export function verifyStripeSignature(rawBody: string, header: string | null, secret: string, nowSec = Math.floor(Date.now() / 1000), toleranceSec = 300): boolean {
  if (!header || !secret) return false;
  let t = "";
  const sigs: string[] = [];
  for (const part of header.split(",")) {
    const [k, v] = part.split("=", 2) as [string, string | undefined];
    if (k?.trim() === "t") t = v ?? "";
    if (k?.trim() === "v1" && v) sigs.push(v.trim());
  }
  const ts = Number(t);
  if (!t || !Number.isFinite(ts) || Math.abs(nowSec - ts) > toleranceSec || sigs.length === 0) return false;
  const want = createHmac("sha256", secret).update(`${t}.${rawBody}`).digest();
  return sigs.some((s) => {
    const got = Buffer.from(s, "hex");
    return got.length === want.length && timingSafeEqual(got, want);
  });
}

/** Signs a payload as Stripe does (tests and local tries). */
export function signStripePayload(rawBody: string, secret: string, t = Math.floor(Date.now() / 1000)): string {
  return `t=${t},v1=${createHmac("sha256", secret).update(`${t}.${rawBody}`).digest("hex")}`;
}
