// Backup history by day, and the moment picker's local-time helpers.
import type { Backup } from "@/api/modules";
import { dayKey } from "@/lib/time";

/** What a backup restores to: the cluster, the KV snapshot and the files. */
export function total(b: Backup) {
  return (b.postgres?.sizeBytes ?? 0) + (b.valkey?.sizeBytes ?? 0) + Object.values(b.files ?? {}).reduce((n, f) => n + f.sizeBytes, 0);
}

/** What a backup added to the repository: Postgres's compressed delta plus the copied files. */
export function stored(b: Backup) {
  return (b.postgres?.repoBytes ?? 0) + (b.valkey?.sizeBytes ?? 0) + Object.values(b.files ?? {}).reduce((n, f) => n + f.sizeBytes, 0);
}

export type BackupDay = {
  /** Local calendar day, "2026-10-07". */
  key: string;
  /** "Today", "Yesterday", "Tue 7 Oct" (with the year when it isn't this one). */
  label: string;
  /** That day's sets, newest first. */
  sets: Backup[];
  /** Successful sets: the restore points. */
  points: number;
  /** What the successful sets stored. */
  bytes: number;
  failed: number;
  running: boolean;
};

const shortDay = new Intl.DateTimeFormat("en-GB", { weekday: "short", day: "numeric", month: "short" });
const shortDayYear = new Intl.DateTimeFormat("en-GB", { weekday: "short", day: "numeric", month: "short", year: "numeric" });

/** "Tue 7 Oct" ("Tue 7 Oct 2025" in another year). */
export function shortDate(iso: string, now = new Date()): string {
  const d = new Date(iso);
  return (d.getFullYear() === now.getFullYear() ? shortDay : shortDayYear).format(d).replace(",", "");
}

/** "Today", "Yesterday" or "Tue 7 Oct", in local time. */
export function dayName(iso: string, now = new Date()): string {
  const k = dayKey(iso);
  if (k === dayKey(now.toISOString())) return "Today";
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (k === dayKey(y.toISOString())) return "Yesterday";
  return shortDate(iso, now);
}

/** Groups a newest-first list into local days, newest first. */
export function groupByDay(list: Backup[], now = new Date()): BackupDay[] {
  const out: BackupDay[] = [];
  for (const b of list) {
    const key = dayKey(b.startedAt);
    let day = out.find((d) => d.key === key);
    if (!day) {
      day = { key, label: dayName(b.startedAt, now), sets: [], points: 0, bytes: 0, failed: 0, running: false };
      out.push(day);
    }
    day.sets.push(b);
    if (b.status === "ok") {
      day.points++;
      day.bytes += stored(b);
    } else if (b.status === "failed") day.failed++;
    else if (b.status === "running") day.running = true;
  }
  return out.sort((a, b) => (a.key < b.key ? 1 : a.key > b.key ? -1 : 0));
}

const pad = (n: number) => String(n).padStart(2, "0");

/** An instant as a datetime-local value in this browser's time zone, to the minute: "2026-10-07T14:32". */
export function toLocalInput(iso: string): string {
  const d = new Date(iso);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** A datetime-local value (local time) as an ISO instant in UTC; null when empty or not a time. */
export function fromLocalInput(v: string): string | null {
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d(:\d\d)?$/.test(v)) return null;
  const d = new Date(v); // no offset: local time
  return Number.isNaN(d.getTime()) ? null : d.toISOString();
}

/** "UTC", "UTC+2", "UTC−5:30" for the offset at that instant. */
export function utcOffset(iso: string): string {
  const m = -new Date(iso).getTimezoneOffset();
  if (m === 0) return "UTC";
  const a = Math.abs(m);
  return `UTC${m > 0 ? "+" : "−"}${Math.floor(a / 60)}${a % 60 ? `:${pad(a % 60)}` : ""}`;
}

/** "14:32" in UTC. */
export function utcClock(iso: string): string {
  const d = new Date(iso);
  return `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`;
}

/**
 * Whether a picked moment can be restored to: inside [earliest, latest].
 * A moment picked to the minute counts from the start of that minute, so
 * the earliest's own minute rounds up to the next one.
 */
export function inRange(iso: string | null, r: { earliest: string; latest: string } | null | undefined): boolean {
  if (!iso || !r) return false;
  const t = new Date(iso).getTime();
  return t >= new Date(r.earliest).getTime() && t <= new Date(r.latest).getTime();
}

/** The first whole minute at or after iso, as a datetime-local value: the picker's min. */
export function minInput(iso: string): string {
  const d = new Date(iso);
  if (d.getSeconds() || d.getMilliseconds()) d.setMinutes(d.getMinutes() + 1, 0, 0);
  return toLocalInput(d.toISOString());
}
