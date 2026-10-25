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
export function clientIP(request: Request): string | undefined {
  const xff = request.headers.get("x-forwarded-for");
  if (xff) {
    const parts = xff.split(",");
    return parts[parts.length - 1]?.trim() || undefined;
  }
  return request.headers.get("x-real-ip") ?? undefined;
}

/**
 * Record a custom event. Resolves to true when the box accepted it, false
 * when analytics is not configured, the event was dropped (bots) or the box
 * could not be reached.
 */
export async function track(name: string, props?: Props, opts: TrackOptions = {}): Promise<boolean> {
  const env = typeof process !== "undefined" ? process.env : {};
  const endpoint = opts.endpoint ?? env.TIFFIN_ANALYTICS_URL;
  const key = opts.key ?? env.TIFFIN_ANALYTICS_KEY;
  if (!endpoint || !key || !name) return false;
  const body: Record<string, unknown> = { name };
  if (props && Object.keys(props).length > 0) body.props = props;
  const req = opts.request;
  if (req) {
    body.url = req.url;
    const ip = clientIP(req);
    if (ip) body.ip = ip;
    const ua = req.headers.get("user-agent");
    if (ua) body.ua = ua;
    const ref = req.headers.get("referer");
    if (ref) body.referrer = ref;
    // The visitor's browser sent Global Privacy Control: the box won't count it.
    if (req.headers.get("sec-gpc") === "1") body.gpc = true;
  }
  if (opts.url) body.url = opts.url;
  if (opts.at) body.at = opts.at.toISOString();
  const f = opts.fetch ?? fetch;
  try {
    const res = await f(endpoint.replace(/\/+$/, "") + "/track", {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${key}` },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(opts.timeoutMs ?? 2000),
    });
    if (res.status !== 202) return false;
    const out = (await res.json().catch(() => ({}))) as { ok?: boolean };
    return out.ok === true;
  } catch {
    return false;
  }
}

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
export async function revenue(payment: Revenue, opts: Omit<TrackOptions, "url"> = {}): Promise<RevenueResult> {
  const env = typeof process !== "undefined" ? process.env : {};
  const endpoint = opts.endpoint ?? env.TIFFIN_ANALYTICS_URL;
  const key = opts.key ?? env.TIFFIN_ANALYTICS_KEY;
  if (!endpoint || !key) return { ok: false, error: "analytics is not configured (TIFFIN_ANALYTICS_URL, TIFFIN_ANALYTICS_KEY)" };
  if (!payment || !Number.isSafeInteger(payment.amount) || payment.amount <= 0) {
    return { ok: false, error: "amount is a whole number in the currency's smallest unit: 1999 for 19.99" };
  }
  if (!payment.id || !/^[a-zA-Z]{3}$/.test(payment.currency ?? "")) {
    return { ok: false, error: "id and a three-letter currency are required" };
  }
  const body: Record<string, unknown> = { id: String(payment.id), amount: payment.amount, currency: payment.currency.toLowerCase() };
  if (payment.user) body.user = String(payment.user);
  if (payment.refund) body.refund = payment.refund === true ? true : String(payment.refund);
  if (payment.renewal) body.renewal = true;
  const at = payment.at ?? opts.at;
  if (at) body.at = at.toISOString();
  const req = opts.request;
  if (req) {
    const ip = clientIP(req);
    if (ip) body.ip = ip;
    const ua = req.headers.get("user-agent");
    if (ua) body.ua = ua;
    if (req.headers.get("sec-gpc") === "1") body.gpc = true;
  }
  const f = opts.fetch ?? fetch;
  try {
    const res = await f(endpoint.replace(/\/+$/, "") + "/revenue", {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${key}` },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(opts.timeoutMs ?? 2000),
    });
    const out = (await res.json().catch(() => ({}))) as RevenueResult;
    if (res.status !== 202) return { ok: false, error: out.error ?? `the box answered ${res.status}` };
    if (out.ignored) return { ok: false, ignored: out.ignored };
    return { ok: out.ok === true, matched: out.matched ?? "", ...(out.duplicate ? { duplicate: true } : {}) };
  } catch (err) {
    return { ok: false, error: `the box could not be reached: ${err instanceof Error ? err.message : String(err)}` };
  }
}

/** Where a visit came from (marketing attribution). */
export interface Touch {
  at: string;
  /** The source: a utm_source or ref tag, else the referrer's name ("Google", "news.ycombinator.com"); "" for direct. */
  source: string;
  refHost: string;
  utmSource: string;
  utmMedium: string;
  utmCampaign: string;
  /** The visit's first page. */
  landing: string;
  /** ISO country code. */
  country: string;
}

/** Where an account came from. */
export interface Attribution {
  userId: string;
  at: string;
  /** visit: the sign-up joined a visit going on, whose source `visit` is; none: no visit was found. */
  matched: "visit" | "none";
  /** The visit the account signed up in. */
  visit: Touch;
  /** The person's first visit, remembered by the first-visit cookie (when the project sets it). */
  first?: Touch;
}

/**
 * Where an account came from: the source, campaign tags and first page of the
 * visit it signed up in (and of its first visit, with the first-visit
 * cookie). Needs marketing attribution with signups on; null when it is off,
 * the account has none, or the box can't be reached. Never throws.
 *
 * ```ts
 * const a = await attribution(session.user.id);
 * if (a?.visit.utmCampaign === "launch") ...
 * ```
 */
export async function attribution(
  userId: string,
  opts: Pick<TrackOptions, "endpoint" | "key" | "fetch" | "timeoutMs"> = {},
): Promise<Attribution | null> {
  const env = typeof process !== "undefined" ? process.env : {};
  const endpoint = opts.endpoint ?? env.TIFFIN_ANALYTICS_URL;
  const key = opts.key ?? env.TIFFIN_ANALYTICS_KEY;
  if (!endpoint || !key || !userId) return null;
  const f = opts.fetch ?? fetch;
  try {
    const res = await f(`${endpoint.replace(/\/+$/, "")}/attribution?user=${encodeURIComponent(userId)}`, {
      headers: { authorization: `Bearer ${key}` },
      signal: AbortSignal.timeout(opts.timeoutMs ?? 2000),
    });
    if (!res.ok) return null;
    const out = (await res.json()) as { attribution?: Attribution | null };
    return out.attribution ?? null;
  } catch {
    return null;
  }
}
