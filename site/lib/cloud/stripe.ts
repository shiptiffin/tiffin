// A small Stripe client: the handful of calls the control plane makes, form
// encoded as Stripe's API wants, and the webhook signature check. Test mode
// until the owner says otherwise; the keys come from the project's secrets.
import { createHmac, timingSafeEqual } from "node:crypto";

export const STRIPE_API = "https://api.stripe.com/v1";
export const PRICE_LOOKUP_KEY = "box_monthly_v1";
export const FOUNDING_LIMIT = 100;

export class StripeError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
    readonly param?: string,
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
    const headers: Record<string, string> = { Authorization: `Bearer ${secretKey}` };
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
      throw new StripeError(e.message ?? `Stripe answered ${res.status}`, res.status, e.code, e.param);
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
      return req<{ id: string; url: string }>("POST", "/checkout/sessions", params, idempotencyKey);
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
  };
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
