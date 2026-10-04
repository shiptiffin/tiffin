import { useSyncExternalStore } from "react";

/**
 * The projects you opened last, newest first: the switcher's short list and
 * the home page's "recently opened" sort. Per browser (localStorage); a
 * private window starts empty and works the same.
 */
const KEY = "tiffin.recent-projects";
const subs = new Set<() => void>();

function read(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

let list = read();

export function rememberProject(project: string) {
  if (list[0] === project) return;
  list = [project, ...list.filter((p) => p !== project)].slice(0, 12);
  try {
    localStorage.setItem(KEY, JSON.stringify(list));
  } catch {
    /* storage blocked: it lasts for this page */
  }
  subs.forEach((f) => f());
}

export function useRecentProjects(): string[] {
  return useSyncExternalStore(
    (f) => (subs.add(f), () => subs.delete(f)),
    () => list,
  );
}
