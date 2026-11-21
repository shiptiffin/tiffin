// The box's Analytics page URL, kept out of the page's own chunk so the
// router can read it without loading the page.

export type BoxPeriod = "24h" | "7d" | "30d";

/** Only what the page understands: 7 days stays out of the URL. */
export function boxAnalyticsSearch(s: Record<string, unknown>): { period?: BoxPeriod } {
  return s.period === "24h" || s.period === "30d" ? { period: s.period } : {};
}
