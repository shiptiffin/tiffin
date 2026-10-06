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
