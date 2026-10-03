import { useSyncExternalStore } from "react";

export type ThemePref = "system" | "light" | "dark";
const KEY = "tiffin.theme";
const listeners = new Set<() => void>();
const media = window.matchMedia("(prefers-color-scheme: dark)");

function read(): ThemePref {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "light" || v === "dark") return v;
  } catch {
    /* storage blocked */
  }
  return "system";
}

let pref: ThemePref = read();

function apply() {
  const resolved = pref === "system" ? (media.matches ? "dark" : "light") : pref;
  document.documentElement.dataset.theme = resolved;
  listeners.forEach((l) => l());
}

media.addEventListener("change", () => pref === "system" && apply());

export function setTheme(next: ThemePref) {
  pref = next;
  try {
    if (next === "system") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, next);
  } catch {
    /* storage blocked */
  }
  apply();
}

export function useTheme(): { pref: ThemePref; resolved: "light" | "dark" } {
  const snap = useSyncExternalStore(
    (cb) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    () => `${pref}:${document.documentElement.dataset.theme ?? "dark"}`,
  );
  const [p, r] = snap.split(":") as [ThemePref, "light" | "dark"];
  return { pref: p, resolved: r };
}

apply();
