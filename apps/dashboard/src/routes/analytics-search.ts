// The Analytics page's URL, kept out of the page's own chunk so the router
// can read it without loading the page.

export const FILTERS = [
  { key: "page", name: "Page" },
  { key: "entry", name: "Entry page" },
  { key: "exit", name: "Exit page" },
  { key: "source", name: "Referrer" },
  { key: "utmSource", name: "UTM source" },
  { key: "utmMedium", name: "UTM medium" },
  { key: "utmCampaign", name: "UTM campaign" },
  { key: "country", name: "Country" },
  { key: "device", name: "Device" },
  { key: "browser", name: "Browser" },
  { key: "os", name: "System" },
] as const;
export type FilterKey = (typeof FILTERS)[number]["key"];
export type Metric = "visitors" | "pageviews" | "bounce" | "duration";
export const VITALS = ["LCP", "INP", "CLS", "FCP", "TTFB"] as const;
export type Vital = (typeof VITALS)[number];
export const PERIODS = ["today", "yesterday", "24h", "7d", "30d", "90d", "12mo"] as const;
export type AnalyticsSearch = {
  period?: string;
  from?: string;
  to?: string;
  app?: string;
  interval?: "hour" | "day";
  metric?: Metric;
  /** "off" hides the period before (on by default). */
  compare?: "off";
  /** The Web Vital the speed chart shows (LCP by default). */
  vital?: Vital;
} & Partial<Record<FilterKey, string>>;

const str = (v: unknown) => (typeof v === "string" && v !== "" ? v : undefined);
const day = (v: unknown) => (typeof v === "string" && /^\d{4}-\d{2}-\d{2}$/.test(v) ? v : undefined);

/** Only what the page understands; empty values are left out of the URL. */
export function analyticsSearch(s: Record<string, unknown>): AnalyticsSearch {
  const out: AnalyticsSearch = {
    period: (PERIODS as readonly unknown[]).includes(s.period) ? (s.period as string) : undefined,
    from: day(s.from),
    to: day(s.to),
    app: str(s.app),
    interval: s.interval === "hour" || s.interval === "day" ? s.interval : undefined,
    metric: s.metric === "pageviews" || s.metric === "bounce" || s.metric === "duration" ? s.metric : undefined,
    compare: s.compare === "off" ? "off" : undefined,
    vital: (VITALS as readonly unknown[]).includes(s.vital) && s.vital !== "LCP" ? (s.vital as Vital) : undefined,
  };
  for (const f of FILTERS) out[f.key] = str(s[f.key]);
  // What the API would refuse: a device it doesn't know, a country that isn't a code.
  if (out.device && !["desktop", "mobile", "tablet"].includes(out.device)) out.device = undefined;
  if (out.country && !/^[A-Z]{2}$/.test(out.country)) out.country = undefined;
  if (out.period === "7d") out.period = undefined;
  return Object.fromEntries(Object.entries(out).filter(([, v]) => v !== undefined)) as AnalyticsSearch;
}
