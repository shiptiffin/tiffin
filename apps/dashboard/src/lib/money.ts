import { MINUS } from "./format";

/**
 * Money from the revenue API: amounts are integers in the currency's smallest
 * unit (cents; yen have none), formatted with Intl.NumberFormat in that
 * currency. Whole units from 1,000 up, where cents are noise.
 */
const cache = new Map<string, Intl.NumberFormat>();

function fmt(currency: string, digits?: number): Intl.NumberFormat {
  const k = `${currency}:${digits ?? ""}`;
  let f = cache.get(k);
  if (!f) {
    try {
      f = new Intl.NumberFormat("en-US", { style: "currency", currency: currency.toUpperCase(), ...(digits !== undefined ? { minimumFractionDigits: digits, maximumFractionDigits: digits } : {}) });
    } catch {
      f = new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 }); // a code Intl doesn't know
    }
    cache.set(k, f);
  }
  return f;
}

/** How many minor units make one: 100 for USD, 1 for JPY, 1000 for KWD. */
export function minorPer(currency: string): number {
  return 10 ** (fmt(currency).resolvedOptions().maximumFractionDigits ?? 2);
}

/**
 * 1248050, "usd" → "$12,481"; 4150 → "$41.50". "exact" keeps the cents
 * (a single payment); "whole" drops them (columns and axes, so they line up).
 */
export function money(minor: number | null | undefined, currency: string, mode: "auto" | "exact" | "whole" = "auto"): string {
  if (minor === null || minor === undefined || !Number.isFinite(minor) || !currency) return "–";
  const v = minor / minorPer(currency);
  const whole = mode === "whole" || (mode === "auto" && Math.abs(v) >= 1000);
  const out = fmt(currency, whole ? 0 : undefined).format(v);
  return out.replace("-", MINUS);
}
