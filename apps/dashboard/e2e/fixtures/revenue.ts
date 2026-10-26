import type { components } from "../../src/api/schema";

// Believable answers from the revenue API (GET /v1/analytics/stripe and
// /v1/analytics/revenue) for specs: the scratch box can't reach Stripe.
type S = components["schemas"];

export const PERMISSIONS = [
  "Balance: Read",
  "Charges and Refunds: Read",
  "Customers: Read",
  "Invoices: Read",
  "Subscriptions: Read",
  "Checkout Sessions: Read",
  "Webhook Endpoints, Event Destinations: Write",
];

const day = 86_400_000;

export function stripeStatus(project: string, over: Partial<S["AnalyticsStripeStatus"]> = {}, now = Date.now()): S["AnalyticsStripeStatus"] {
  if (over.connected === false)
    return { project, connected: false, urlMismatch: false, eventsReceived: 0, eventsWaiting: 0, eventsFailed: 0, permissions: PERMISSIONS, warnings: [], backfill: { state: "done", payments: 0, refunds: 0, subscriptions: 0, started: new Date(0).toISOString() }, ...over };
  return {
    project,
    connected: true,
    mode: "live",
    key: "rk_live_…a1b2",
    webhookId: "we_1Q2w3E4r",
    webhookUrl: `https://${project}.example.com/_tiffin/stripe`,
    wantUrl: `https://${project}.example.com/_tiffin/stripe`,
    urlMismatch: false,
    accountCurrency: "usd",
    connectedAt: new Date(now - 21 * day).toISOString(),
    connectedBy: "Bilal",
    backfill: { state: "done", payments: 2304, refunds: 41, subscriptions: 118, started: new Date(now - 21 * day).toISOString(), finished: new Date(now - 21 * day + 90_000).toISOString() },
    eventsReceived: 1288,
    eventsWaiting: 0,
    eventsFailed: 0,
    lastEventAt: new Date(now - 7 * 60_000).toISOString(),
    permissions: PERMISSIONS,
    warnings: [],
    ...over,
  };
}

const count = (value: string, revenue: number, customers: number): S["AnalyticsRevenueCount"] => ({ value, revenue, customers, payments: Math.round(customers * 1.3) });

export function revenueReport(project: string, touch: "first" | "last" = "last", now = Date.now()): S["AnalyticsRevenueReport"] {
  const end = Math.floor(now / day) * day;
  const first = touch === "first";
  // A launch on Hacker News 11 days ago, then the newsletter.
  const timeseries = Array.from({ length: 30 }, (_, i) => {
    const t = end - (29 - i) * day;
    const spike = i === 18 ? 3.2 : i === 19 ? 1.8 : 1;
    const weekday = new Date(t).getUTCDay() % 6 === 0 ? 0.7 : 1;
    const revenue = Math.round((32000 + ((i * 7919) % 17000)) * spike * weekday);
    return { t: new Date(t).toISOString(), revenue, payments: Math.round(revenue / 5800), refunds: i % 9 === 0 ? 1 : 0, newCustomers: Math.round(revenue / 8200) };
  });
  return {
    project,
    period: "Last 30 days",
    from: new Date(end - 29 * day).toISOString(),
    to: new Date(end + day).toISOString(),
    touch,
    provider: "stripe",
    currency: "usd",
    granularity: "day",
    totals: { revenue: 1248050, gross: 1279850, refunded: 31800, payments: 214, renewals: 61, refunds: 6, customers: 171, newCustomers: 153, mrr: 389000, visitors: 13120, conversionRate: 153 / 13120, revenuePerVisitor: 1248050 / 13120 },
    previous: { revenue: 1012000, gross: 1030000, refunded: 18000, payments: 180, renewals: 55, refunds: 4, customers: 149, newCustomers: 131, mrr: 351000, visitors: 11800, conversionRate: 131 / 11800, revenuePerVisitor: 1012000 / 11800 },
    timeseries,
    sources: [
      count("Google", first ? 351000 : 412000, 58),
      count("news.ycombinator.com", first ? 334000 : 268000, 41),
      count("", first ? 142000 : 201000, 30),
      count("newsletter", 148000, 22),
      count("producthunt", 92000, 13),
      count("x.com", 61000, 9),
      count("github.com", 38000, 6),
    ],
    utmSources: [count("newsletter", 148000, 22), count("producthunt", 92000, 13), count("twitter", 21000, 3)],
    utmMediums: [count("email", 148000, 22), count("social", 113000, 16)],
    utmCampaigns: [count("launch-week", 205000, 29), count("october-digest", 56000, 8)],
    landing: [count("/", 498000, 70), count("/pricing", 351000, 49), count("/blog/shipping-on-a-budget", 214000, 31), count("/docs/getting-started", 102000, 14)],
    countries: [count("US", 512000, 70), count("GB", 188000, 26), count("DE", 141000, 19), count("IN", 96000, 15), count("CA", 82000, 11), count("FR", 61000, 9)],
    unattributed: { value: "", revenue: 28050, payments: 4, customers: 4 },
    matchedBy: { reference: 120, email: 52, checkout: 21, customer: 17, none: 4 },
    otherCurrencies: [{ currency: "eur", amount: 42000, payments: 3 }],
    mrrOtherCurrencies: [],
    unmatched: [
      { id: "ch_3Q9a", provider: "stripe", kind: "payment", at: new Date(now - 2 * day).toISOString(), amount: 4900, currency: "usd", accountAmount: 4900, accountCurrency: "usd", customer: "cus_R2x8YtLk", reason: "no account has this customer's email" },
      { id: "ch_3Q8b", provider: "stripe", kind: "payment", at: new Date(now - 5 * day).toISOString(), amount: 4500, currency: "eur", accountAmount: 4862, accountCurrency: "usd", customer: "cus_R1pQ3mZa", reason: "the checkout session names no account and the buyer's visit wasn't seen" },
      { id: "ch_3Q7c", provider: "stripe", kind: "renewal", at: new Date(now - 9 * day).toISOString(), amount: 1900, currency: "usd", accountAmount: 1900, accountCurrency: "usd", customer: "cus_QzT7nB4c", reason: "paid before attribution was turned on" },
    ],
  };
}
