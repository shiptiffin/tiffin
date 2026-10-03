/** Joins class names, skipping falsy values. Variants are written so they never conflict. */
export function cn(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}
