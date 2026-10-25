/**
 * The privacy policy text the box writes for a project's analytics settings
 * (internal/mod/analytics/privacy.go, word for word), so the dashboard can
 * show it before a change applies. No imports: the e2e specs use it too.
 */

export type Cookie = "off" | "outside-eu" | "ask-eu";

export type Attribution = {
  on: boolean;
  /** Keep utm_* and ref tags. */
  campaigns: boolean;
  /** Save where each new account came from. */
  signups: boolean;
  cookie: Cookie;
};

/** The privacy policy text for these settings, as the box writes it (internal/mod/analytics/privacy.go). */
export function privacyText(s: Attribution, revenue = false): string {
  let b = "### Analytics\n\n";
  b +=
    "We count visits to this site with first-party analytics that runs on our own server; nothing is sent to anyone else. " +
    'For each page view we keep the page\'s address without its query string, the name of the site that sent you (such as "news.ycombinator.com"), ' +
    "your country, the kind of browser, system and device, and the time. Visitors are told apart by a code made from the site, your IP address and " +
    "browser string and a secret that changes every day and is deleted within 48 hours, so visits on different days can't be linked. " +
    "No IP addresses or browser strings are stored. Bots are not counted, and neither is anyone whose browser sends Global Privacy Control.\n\n";
  if (!s.on) return b + "This analytics uses no cookies and stores nothing on your device.\n";
  b += "### Where our customers come from\n\n";
  const keep: string[] = [];
  if (s.campaigns) keep.push("the campaign tags in the link you followed (utm_source, utm_medium, utm_campaign and ref)");
  if (s.signups) keep.push("when you create an account, where the visit you signed up in came from (the site that sent you, those tags and the first page you saw), saved with your account");
  if (revenue) keep.push("when you pay, the payment's amount, currency and date, linked to your account or to the visit you paid in");
  if (keep.length) b += "To see which sites and campaigns bring us customers, we also keep " + keep.join("; ") + ". ";
  const cookie = s.cookie === "outside-eu" || s.cookie === "ask-eu";
  b +=
    "We use this only to understand our own marketing, never to show you ads or to track you on other sites, and we share it with no one. " +
    "Our legitimate interest in knowing what brings us customers is the legal basis (GDPR Article 6(1)(f)). " +
    "To object, turn on Global Privacy Control in your browser" +
    (cookie ? " or use [this link](/_tiffin/opt-out)" : "") +
    "; you can also ask us to delete what is saved with your account. " +
    "It is deleted with your account, or when we stop collecting it.\n\n";
  if (!cookie) return b + "This analytics uses no cookies and stores nothing on your device.\n";
  b += "### Cookies\n\n";
  b +=
    "We set one first-party cookie, `tiffin_first_visit`, for 90 days. It holds a random code, no personal details, and lets us see " +
    "which site first sent you if you later sign up or buy. Only this site can read it; it is never shared or used across sites. ";
  b +=
    s.cookie === "ask-eu"
      ? "We don't set it for visitors in the EU, EEA or UK unless they agree, and you can change your mind at any time with the link below. "
      : "We don't set it for visitors in the EU, EEA or UK. ";
  return b + "To opt out, turn on Global Privacy Control in your browser, or use [this link](/_tiffin/opt-out).\n";
}

/** The first 12 hex digits of the text's SHA-256, as the box's `hash`. */
export async function privacyHash(md: string): Promise<string> {
  const sum = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(md));
  return Array.from(new Uint8Array(sum), (x) => x.toString(16).padStart(2, "0"))
    .join("")
    .slice(0, 12);
}

