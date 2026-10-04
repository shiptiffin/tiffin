/**
 * `tiffin-sdk/analytics`: send custom events from server code to the box's
 * first-party, cookieless analytics.
 *
 * ```ts
 * import { track } from "tiffin-sdk/analytics";
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
/** The client IP the box's edge forwarded: the last X-Forwarded-For entry. */
export function clientIP(request) {
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
export async function track(name, props, opts = {}) {
    const env = typeof process !== "undefined" ? process.env : {};
    const endpoint = opts.endpoint ?? env.TIFFIN_ANALYTICS_URL;
    const key = opts.key ?? env.TIFFIN_ANALYTICS_KEY;
    if (!endpoint || !key || !name)
        return false;
    const body = { name };
    if (props && Object.keys(props).length > 0)
        body.props = props;
    const req = opts.request;
    if (req) {
        body.url = req.url;
        const ip = clientIP(req);
        if (ip)
            body.ip = ip;
        const ua = req.headers.get("user-agent");
        if (ua)
            body.ua = ua;
        const ref = req.headers.get("referer");
        if (ref)
            body.referrer = ref;
    }
    if (opts.url)
        body.url = opts.url;
    if (opts.at)
        body.at = opts.at.toISOString();
    const f = opts.fetch ?? fetch;
    try {
        const res = await f(endpoint.replace(/\/+$/, "") + "/track", {
            method: "POST",
            headers: { "content-type": "application/json", authorization: `Bearer ${key}` },
            body: JSON.stringify(body),
            signal: AbortSignal.timeout(opts.timeoutMs ?? 2000),
        });
        if (res.status !== 202)
            return false;
        const out = (await res.json().catch(() => ({})));
        return out.ok === true;
    }
    catch {
        return false;
    }
}
