import { useQueryClient } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { useSyncExternalStore } from "react";
import { q } from "@/api/queries";
import { partA } from "./names";
import { landing, projectHome, type Part } from "./sections";

/**
 * Going to another project from the switcher or ⌘K keeps your section (see
 * sections.ts `landing`). When the other project lacks the part, you land on
 * its Overview, which says so quietly (`useArrival`).
 */
type Arrival = { project: string; part: Part };
let arrival: Arrival | null = null;
const subs = new Set<() => void>();

export function useSwitchToProject() {
  const router = useRouter();
  const qc = useQueryClient();
  return async (project: string) => {
    const state = await qc.query(q.project(project)).catch(() => undefined);
    const to = landing(router.state.location.pathname, project, state, Object.keys(router.routesByPath));
    const a = to.missing ? { project, part: to.missing } : null;
    arrival = a;
    subs.forEach((f) => f());
    if (a)
      setTimeout(() => {
        if (arrival !== a) return;
        arrival = null;
        subs.forEach((f) => f());
      }, 30_000);
    await router.navigate({ to: to.to, params: to.params });
  };
}

/** The part you were on ("a KV store"), when you just switched to a project without it. */
export function useArrival(project: string): string | null {
  const a = useSyncExternalStore(
    (f) => (subs.add(f), () => subs.delete(f)),
    () => arrival,
  );
  return a && a.project === project ? (a.part === "apps" ? "an app" : partA(a.part)) : null;
}

/** Where a link to a project goes: its Overview. */
export function useProjectHome(project: string) {
  return projectHome(project);
}
