// The funnel's custom events (sign in → pay → connect Hetzner → set up → open),
// sent with @shiptiffin/sdk/analytics track() from the routes and pages. Props
// are low-cardinality facts only: never emails, names, account, box or Stripe
// ids. Page views need no code; these say what a page view can't.

/**
 * Where the sign-in form sends a browser once signed in: /sign-in/done counts
 * it (a first sign-in makes the account), then goes on to `next`.
 */
export function signInCallbacks(next: string): { callbackURL: string; newUserCallbackURL: string } {
  const to = `/sign-in/done?next=${encodeURIComponent(next)}`;
  return { callbackURL: to, newUserCallbackURL: `${to}&new=1` };
}

/**
 * A Request for track() in a page (server components get headers, not the
 * request): what joins the visitor's session (client IP, user agent), the
 * referrer and Global Privacy Control.
 */
export function requestFrom(h: { get(name: string): string | null }, url: string): Request {
  const headers = new Headers();
  for (const k of ["x-forwarded-for", "x-real-ip", "user-agent", "referer", "sec-gpc"]) {
    const v = h.get(k);
    if (v) headers.set(k, v);
  }
  return new Request(url, { headers });
}

/** How "Open your dashboard" went: the one-time sign-in link, a request to the box for a new one, or the dashboard itself. */
export function openedVia(url: string): "link" | "asked" | "direct" {
  if (url.startsWith("/")) return "asked";
  return url.includes("/login#") ? "link" : "direct";
}

/** The account page's ways out, as subscription_cancelled's `how` (null: not a cancellation). */
export function cancelHow(action: string): "period_end" | "keep_server" | "delete_server" | null {
  if (action === "cancel") return "period_end";
  if (action === "release") return "keep_server";
  if (action === "delete-server") return "delete_server";
  return null;
}
