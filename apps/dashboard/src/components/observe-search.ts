/**
 * The Observability page's tabs, for its route's validateSearch (kept out of
 * the page's module so the router doesn't load the page to read the address).
 */
export const observeTabs = ["overview", "resources", "errors", "requests"] as const;
export type ObserveTab = (typeof observeTabs)[number];

/** ?tab=errors → "errors"; the overview (the default) and anything unknown → undefined. */
export const observeTab = (v: unknown): Exclude<ObserveTab, "overview"> | undefined =>
  typeof v === "string" && v !== "overview" && (observeTabs as readonly string[]).includes(v) ? (v as Exclude<ObserveTab, "overview">) : undefined;

/** The page's address state: the tab, plus the charts' range ("24h" when absent) and app (all when absent). */
export type ObserveSearch = { tab?: Exclude<ObserveTab, "overview">; range?: "1h" | "7d" | "30d"; app?: string };
