const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto", style: "long" });
const timeFmt = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const dayFmt = new Intl.DateTimeFormat(undefined, { weekday: "long", day: "numeric", month: "long" });
const dayYearFmt = new Intl.DateTimeFormat(undefined, { weekday: "long", day: "numeric", month: "long", year: "numeric" });
const fullFmt = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" });

/** "just now", "4 minutes ago", "yesterday", "3 days ago". */
export function relative(iso: string, now = Date.now()): string {
  const s = Math.round((new Date(iso).getTime() - now) / 1000);
  const a = Math.abs(s);
  if (a < 45) return "just now";
  if (a < 3600) return rtf.format(Math.round(s / 60), "minute");
  if (a < 86400) return rtf.format(Math.round(s / 3600), "hour");
  if (a < 86400 * 30) return rtf.format(Math.round(s / 86400), "day");
  if (a < 86400 * 365) return rtf.format(Math.round(s / (86400 * 30)), "month");
  return rtf.format(Math.round(s / (86400 * 365)), "year");
}

export function clock(iso: string): string {
  return timeFmt.format(new Date(iso));
}

export function full(iso: string): string {
  return fullFmt.format(new Date(iso));
}

/** Local calendar day key, e.g. "2026-10-02". */
export function dayKey(iso: string): string {
  const d = new Date(iso);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** "Today", "Yesterday", or "Thursday, 2 October". */
export function dayLabel(iso: string, now = new Date()): string {
  const d = new Date(iso);
  const k = dayKey(iso);
  if (k === dayKey(now.toISOString())) return "Today";
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (k === dayKey(y.toISOString())) return "Yesterday";
  return d.getFullYear() === now.getFullYear() ? dayFmt.format(d) : dayYearFmt.format(d);
}

/** "in 29 days", "expired 2 days ago", "never". */
export function expiry(iso?: string): string {
  if (!iso) return "never";
  const t = new Date(iso).getTime();
  return t < Date.now() ? `expired ${relative(iso)}` : relative(iso);
}

/** For "since …": "14:05" today, "yesterday", or "Thursday, 2 October". */
export function sinceWhen(iso: string, now = new Date()): string {
  const l = dayLabel(iso, now);
  return l === "Today" ? clock(iso) : l === "Yesterday" ? "yesterday" : l;
}

/** "Thursday, 2 October". */
export function longDay(iso: string): string {
  return dayFmt.format(new Date(iso));
}

/** True when `iso` is less than `ms` away (or already past). */
export function within(iso: string, ms: number): boolean {
  return new Date(iso).getTime() - Date.now() < ms;
}

/** "15m" → "Last 15 min", "1h" → "Last hour", "7d" → "Last 7 days". */
export function windowLabel(w: string): string {
  const m = w.match(/^(\d+)([mhd])$/);
  if (!m) return w;
  const n = Number(m[1]);
  const unit = { m: "min", h: n === 1 ? "hour" : "hours", d: n === 1 ? "day" : "days" }[m[2] as "m" | "h" | "d"];
  return n === 1 && m[2] !== "m" ? `Last ${unit}` : `Last ${n} ${unit}`;
}

/**
 * When a version went live: the one time every page quotes for "live …" (the
 * app header, its version row, the project and Box rows). A version that went
 * live again (a rollback to it) counts from then.
 */
export function liveSince(d: { liveAt?: string | null; finishedAt?: string | null; createdAt: string }): string {
  return d.liveAt ?? d.finishedAt ?? d.createdAt;
}
