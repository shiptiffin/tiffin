// The Analytics page's URL, kept out of the page's own chunk so the router
// can read it without loading the page.

export const FILTERS = [
  { key: "page", name: "Page" },
  { key: "entry", name: "Entry page" },
  { key: "exit", name: "Exit page" },
  { key: "source", name: "Source" },
  { key: "utmSource", name: "UTM source" },
  { key: "utmMedium", name: "UTM medium" },
  { key: "utmCampaign", name: "UTM campaign" },
  { key: "country", name: "Country" },
  { key: "browser", name: "Browser" },
  { key: "os", name: "System" },
  { key: "device", name: "Device" },
] as const;
export type FilterKey = (typeof FILTERS)[number]["key"];
export type Metric = "visitors" | "pageviews" | "bounce" | "duration";
export type AnalyticsSearch = { period?: string; from?: string; to?: string; app?: string; interval?: "hour" | "day"; metric?: Metric } & Partial<Record<FilterKey, string>>;

const str = (v: unknown) => (typeof v === "string" && v !== "" ? v : undefined);
const day = (v: unknown) => (typeof v === "string" && /^\d{4}-\d{2}-\d{2}$/.test(v) ? v : undefined);

/** Only what the page understands; empty values are left out of the URL. */
export function analyticsSearch(s: Record<string, unknown>): AnalyticsSearch {
  const out: AnalyticsSearch = {
    period: str(s.period),
    from: day(s.from),
    to: day(s.to),
    app: str(s.app),
    interval: s.interval === "hour" || s.interval === "day" ? s.interval : undefined,
    metric: s.metric === "pageviews" || s.metric === "bounce" || s.metric === "duration" ? s.metric : undefined,
  };
  for (const f of FILTERS) out[f.key] = str(s[f.key]);
  // What the API would refuse: a device it doesn't know, a country that isn't a code.
  if (out.device && !["desktop", "mobile", "tablet"].includes(out.device)) out.device = undefined;
  if (out.country && !/^[A-Z]{2}$/.test(out.country)) out.country = undefined;
  return Object.fromEntries(Object.entries(out).filter(([, v]) => v !== undefined)) as AnalyticsSearch;
}
