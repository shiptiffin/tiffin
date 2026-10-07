import { extendTailwindMerge } from "tailwind-merge";
import { twMergeConfig } from "./cn-config";

type ClassPart = string | false | null | undefined;

/**
 * Joins class names, skipping falsy values. There is no tailwind-merge in
 * production (it would add ~9 KB gzip to the entry chunk): wrappers in
 * components/ui write the defaults a caller may override zero-specificity,
 * `[:where(&)]:max-w-xl`, or expose them as props, so a caller's class always
 * wins. `bun run check:overrides` enforces that statically, and in dev builds
 * cn() warns when tailwind-merge would have changed a class list.
 */
export function cn(...parts: ClassPart[]): string {
  const out = parts.filter(Boolean).join(" ");
  if (import.meta.env.DEV) devCheck(out);
  return out;
}

// ---- dev only ----------------------------------------------------------------
// Production builds replace import.meta.env.DEV with false, so devCheck and the
// two imports above are unreferenced and tree-shaken (tailwind-merge declares
// sideEffects: false). The imports are static on purpose: a dynamic import()
// here, even though its code is dropped, still becomes a dynamic entry in
// Rolldown's chunk graph and split the initial bundle from 11 files into 28.
// The merger itself is built lazily, on the first cn() call.
let dev: { merge: (s: string) => string; seen: Set<string> } | undefined;

function devCheck(classes: string) {
  dev ??= { merge: extendTailwindMerge(twMergeConfig), seen: new Set() };
  if (dev.seen.has(classes)) return;
  dev.seen.add(classes);
  const merged = dev.merge(classes);
  if (merged === classes) return;
  const kept = new Set(merged.split(" "));
  const lost = classes.split(" ").filter((c) => c && !kept.has(c));
  if (!lost.length) return; // only a repeated class, which is harmless
  console.warn(
    `cn(): conflicting classes; which one applies depends on CSS order, not on the order here. ` +
      `tailwind-merge would drop: ${lost.join(" ")}\n  in: ${classes}\n` +
      `Make the ui/* default zero-specificity ([:where(&)]:…) or a prop.`,
  );
}
