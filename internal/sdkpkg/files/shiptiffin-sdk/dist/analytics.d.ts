/**
 * `@shiptiffin/sdk/analytics`: send custom events from server code to the box's
 * first-party, cookieless analytics.
 *
 * ```ts
 * import { track } from "@shiptiffin/sdk/analytics";
 *
 * // In a request handler: pass the request so the event joins the visitor's
 * // session (the box hashes IP + user agent with a daily salt; neither is stored).
 * await track("Signup", { plan: "pro" }, { request });
 *
 * // Anywhere else (a queue job, a webhook): an event without a visitor.
 * await track("Invoice paid", { amount: 49 });
 * ```
 *
 * Pageviews need no code: the box counts them from its edge logs. For
 * single-page-app navigations and browser events add the script tag from
 * `tiffin analytics setup --project <name>`.
 *
 * The box gives each app TIFFIN_ANALYTICS_URL and TIFFIN_ANALYTICS_KEY when
 * the project has `services: { analytics: {} }`. Without them `track()` does
 * nothing and returns false, so code works unchanged in development.
 * `track()` never throws: analytics must not break a request.
 */
export type Props = Record<string, string | number | boolean>;
export interface TrackOptions {
    /** The incoming request: its URL, referrer, client IP and user agent are used. */
    request?: Request;
    /** The page URL or path the event belongs to (default: the request URL, or "/"). */
    url?: string;
    /** When it happened (default now; at most 24 hours ago). */
    at?: Date;
    /** Override the endpoint (default process.env.TIFFIN_ANALYTICS_URL). */
    endpoint?: string;
    /** Override the key (default process.env.TIFFIN_ANALYTICS_KEY). */
    key?: string;
    /** Override fetch (tests). */
    fetch?: typeof fetch;
    /** Give up after this many milliseconds (default 2000). */
    timeoutMs?: number;
}
/** The client IP the box's edge forwarded: the last X-Forwarded-For entry. */
export declare function clientIP(request: Request): string | undefined;
/**
 * Record a custom event. Resolves to true when the box accepted it, false
 * when analytics is not configured, the event was dropped (bots) or the box
 * could not be reached.
 */
export declare function track(name: string, props?: Props, opts?: TrackOptions): Promise<boolean>;
/** A payment for revenue attribution. */
export interface Revenue {
    /** In the currency's smallest unit, a whole number: 1999 for 19.99 USD, 500 for ¥500. */
    amount: number;
    /** Three-letter ISO code: "usd", "eur". */
    currency: string;
    /** Your payment's own id: a retry with the same id is not counted twice. */
    id: string;
    /** The signed-in user's id (Tiffin auth): the payment is credited to where the account came from. */
    user?: string;
    /** A refund (amount positive, it is subtracted). Give the refunded payment's id to credit it the same way. */
    refund?: boolean | string;
    /** A repeat payment (a subscription renewal). Payments after a user's first count as renewals anyway. */
    renewal?: boolean;
    /** When it was paid (default now; up to a year ago). */
    at?: Date;
}
/** What the box did with a revenue() call. */
export interface RevenueResult {
    /** Recorded (or recorded before: duplicate). */
    ok: boolean;
    /** How it was matched: "reference" (the user given), "visit" (the request's visit), "customer" (an earlier payment), "" (not matched). */
    matched?: string;
    duplicate?: boolean;
    /** Not recorded on purpose: "stripe connected" (the box imports Stripe payments itself, so nothing counts twice). */
    ignored?: string;
    /** Why it was not recorded (not configured, attribution off, invalid input, box unreachable). */
    error?: string;
}
/**
 * Record a payment from a provider other than Stripe (Stripe is connected
 * in the dashboard instead), for revenue attribution: it is matched to the
 * user's sign-up source, or to the visit the request belongs to, and shows
 * in the revenue report by source, campaign and landing page. Needs marketing
 * attribution on for the project.
 *
 * ```ts
 * await revenue({ id: order.id, amount: 4900, currency: "usd", user: session.user.id }, { request });
 * ```
 *
 * Never throws: resolves to `{ ok: false, error }` when it could not be recorded.
 */
export declare function revenue(payment: Revenue, opts?: Omit<TrackOptions, "url">): Promise<RevenueResult>;
