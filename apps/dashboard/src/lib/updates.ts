import { queryOptions } from "@tanstack/react-query";
import { request } from "@/api/client";
import type { components } from "@/api/schema";
import { clock, dayKey, relative } from "./time";

type S = components["schemas"];
export type UpdateStatus = S["UpdateStatus"];

/** How the box keeps Tiffin up to date (GET /v1/box/update; box admins). */
export const updateStatusQuery = queryOptions({
  queryKey: ["box-update"],
  queryFn: () => request<UpdateStatus>("GET", "/v1/box/update"),
  retry: false,
  staleTime: 60_000,
});

export const setUpdateSettings = (b: S["UpdateSettingsBody"]) => request<UpdateStatus>("PUT", "/v1/box/update/settings", b);

/** The last update in a line: "1.3.2 → 1.4.0, 3 days ago"; bad = it did not go through. */
export function lastUpdate(s: UpdateStatus, now = Date.now()): { words: string; bad: boolean } | undefined {
  const u = s.updates?.find((x) => x.status !== "running");
  if (!u) return undefined;
  const when = relative(u.at, now);
  if (u.status === "ok") return { words: `${u.from} → ${u.to}, ${when}`, bad: false };
  if (u.status === "rolled-back") return { words: `Tiffin ${u.to} didn’t start properly ${when}, so ${u.from} kept running`, bad: true };
  return { words: `Couldn’t update to ${u.to} ${when}; nothing changed`, bad: true };
}

/** "Today at 04:30", "Tomorrow at 04:30", or the date. */
export function nextWindow(iso: string, now = new Date()): string {
  const tomorrow = new Date(now);
  tomorrow.setDate(now.getDate() + 1);
  const day = dayKey(iso) === dayKey(now.toISOString()) ? "Today" : dayKey(iso) === dayKey(tomorrow.toISOString()) ? "Tomorrow" : new Date(iso).toLocaleDateString();
  return `${day} at ${clock(iso)}`;
}
