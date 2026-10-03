const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto", style: "long" });
const timeFmt = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });
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

/** Go durations like "1h2m3.5s" → "1 h 2 min". */
export function uptime(go: string): string {
  const m = go.match(/(?:(\d+)h)?(?:(\d+)m(?!s))?(?:([\d.]+)s)?/);
  if (!m) return go;
  const h = Number(m[1] ?? 0);
  const min = Number(m[2] ?? 0);
  const s = Math.floor(Number(m[3] ?? 0));
  if (h >= 48) return `${Math.floor(h / 24)} days`;
  if (h > 0) return `${h} h ${min} min`;
  if (min > 0) return `${min} min ${s} s`;
  return `${s} s`;
}

/** "Thursday, 2 October". */
export function longDay(iso: string): string {
  return dayFmt.format(new Date(iso));
}
