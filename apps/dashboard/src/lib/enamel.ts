import { queryOptions, useQueries, useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";

/**
 * Project colours. Six muted enamels at matched lightness; a project's enamel
 * paints only its tier rim, its swatch (sidebar, breadcrumbs, ⌘K) and its
 * share of the memory bar. The box's own parts stay steel.
 */
export const ENAMELS = ["leaf", "teal", "indigo", "plum", "chilli", "turmeric"] as const;
export type Enamel = (typeof ENAMELS)[number];

export const enamelNames: Record<Enamel, string> = {
  leaf: "Leaf",
  teal: "Teal",
  indigo: "Indigo",
  plum: "Plum",
  chilli: "Chilli",
  turmeric: "Turmeric",
};

/** The CSS colour for an enamel: var(--enamel-indigo). */
export const enamelVar = (e: Enamel) => `var(--enamel-${e})`;

/** Same as the server's default (FNV-1a of the name): a project's colour before anyone picks one. */
export function defaultEnamel(project: string): Enamel {
  let h = 0x811c9dc5;
  for (const b of new TextEncoder().encode(project)) {
    h ^= b;
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return ENAMELS[h % ENAMELS.length];
}

export const appearanceQuery = (project: string) =>
  queryOptions({
    queryKey: ["appearance", project],
    queryFn: () => api.appearance(project),
    staleTime: 5 * 60_000,
    retry: false,
  });

const asEnamel = (v: string | undefined, project: string): Enamel =>
  (ENAMELS as readonly string[]).includes(v ?? "") ? (v as Enamel) : defaultEnamel(project);

/** A project's enamel; the name-based default until the API answers (or if it can't). */
export function useEnamel(project: string | undefined): Enamel {
  const { data } = useQuery({ ...appearanceQuery(project ?? ""), enabled: !!project });
  return asEnamel(data?.enamel, project ?? "");
}

/** Enamels for many projects at once. */
export function useEnamels(projects: string[]): Record<string, Enamel> {
  const res = useQueries({ queries: projects.map((p) => appearanceQuery(p)) });
  return Object.fromEntries(projects.map((p, i) => [p, asEnamel(res[i]?.data?.enamel, p)]));
}
