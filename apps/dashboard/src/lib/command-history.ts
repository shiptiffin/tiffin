import { api } from "@/api/client";

/**
 * What you typed in the SQL and KV consoles, for the up arrow and the
 * history menu. Commands can carry secrets (a password literal, AUTH …), so
 * they live in this tab's sessionStorage only: gone when the tab closes, and
 * wiped on sign-out or when the session ends, so the next person at this
 * browser (or the next account) never sees them.
 */
const PREFIX = "tiffin.history.";

export function loadHistory(kind: "sql" | "kv", project: string): string[] {
  try {
    const v = JSON.parse(sessionStorage.getItem(`${PREFIX}${kind}.${project}`) ?? "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

export function saveHistory(kind: "sql" | "kv", project: string, list: string[]) {
  try {
    sessionStorage.setItem(`${PREFIX}${kind}.${project}`, JSON.stringify(list));
  } catch {
    // Storage blocked: the history lasts as long as the page.
  }
}

/** Forgets every console history (and the ones older dashboards kept in localStorage). */
export function forgetHistory() {
  for (const store of [sessionStorage, localStorage]) {
    try {
      for (const k of Object.keys(store)) if (k.startsWith(PREFIX) || k.startsWith("tiffin.sql.history") || k.startsWith("tiffin.kv.history")) store.removeItem(k);
    } catch {
      // Storage blocked: nothing was kept.
    }
  }
}

/** Signs out of the dashboard and forgets what this browser kept of the session. */
export async function signOut() {
  forgetHistory();
  try {
    await api.logout();
  } finally {
    location.assign("/login?reason=signed-out");
  }
}
