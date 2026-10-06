import type { ProjectState } from "@/api/client";
import { PARTS } from "./names";

/**
 * A project's sections: which parts it has, where each one's page is, which
 * part a page belongs to, and where to land when you switch projects (the
 * same section when the other project has it, else its Overview).
 */
export type Part = "apps" | "postgres" | "valkey" | "storage" | "email" | "auth" | "analytics" | "jobs";

export const PART_PAGE: Record<Part, { to: string; label: string }> = {
  apps: { to: "/projects/$project/apps", label: "Apps" },
  postgres: { to: "/projects/$project/data", label: PARTS.postgres.name },
  valkey: { to: "/projects/$project/data/kv", label: PARTS.valkey.name },
  storage: { to: "/projects/$project/storage", label: PARTS.storage.name },
  email: { to: "/projects/$project/email", label: PARTS.email.name },
  auth: { to: "/projects/$project/users", label: PARTS.auth.name },
  analytics: { to: "/projects/$project/analytics", label: PARTS.analytics.name },
  jobs: { to: "/projects/$project/jobs", label: PARTS.jobs.name },
};

/** The part a page under /projects/<p>/ belongs to, by its first path segments (none: Overview, Usage, History, Settings). */
export function partOfPath(tail: string): Part | undefined {
  const [a, b] = tail.split("/");
  if ((a === "data" && b === "kv") || a === "kv") return "valkey";
  return ({ data: "postgres", storage: "storage", email: "email", users: "auth", orgs: "auth", analytics: "analytics", queues: "jobs", workflows: "jobs", jobs: "jobs", schedules: "jobs", apps: "apps" } as Record<string, Part>)[a];
}

/** The parts a project has. A project with no apps can always use Jobs (schedules and queues that call any address). */
export function partsOf(state?: ProjectState, implied = true): Set<Part> {
  const res = state?.resources ?? [];
  const out = new Set<Part>();
  const apps = res.filter((r) => r.address.startsWith("app/"));
  if (apps.length) out.add("apps");
  for (const k of ["postgres", "valkey", "storage", "email", "auth", "analytics"] as const) if (res.some((r) => r.address === `service/${k}`)) out.add(k);
  if (res.some((r) => /^(cron|queue|topic)\//.test(r.address)) || apps.some((a) => (a.spec as { role?: string } | null)?.role === "worker")) out.add("jobs");
  if (implied && state && !apps.length) out.add("jobs");
  return out;
}

/**
 * The one part of a standalone project ("just a database", "just KV",
 * "just files", "just a schedule"): no apps and exactly one part. Its sidebar
 * shows only that part, and it opens there rather than on Overview.
 */
export function standalonePart(state?: ProjectState): Part | null {
  const parts = [...partsOf(state, false)];
  return parts.length === 1 && ["postgres", "valkey", "storage", "jobs"].includes(parts[0]) ? parts[0] : null;
}

/** Where a project opens: its one part when it is standalone, else its Overview. */
export function projectHome(project: string, state?: ProjectState): { to: string; params: { project: string } } {
  const one = standalonePart(state);
  return { to: one ? PART_PAGE[one].to : "/projects/$project", params: { project } };
}

/**
 * Where switching to `target` lands, coming from `path`: the deepest page of
 * the same section that has no names in it (bookshop › Database › SQL →
 * blog › Database › SQL; a table or a job is left behind), when the target
 * has that part. `pages` are the router's project page paths.
 */
export function landing(path: string, target: string, state: ProjectState | undefined, pages: string[]): { to: string; params: { project: string }; missing?: Part } {
  const tail = path.match(/^\/projects\/[^/]+\/?(.*)$/)?.[1];
  if (tail === undefined || tail === "") return projectHome(target, state);
  const part = partOfPath(tail);
  if (part && !partsOf(state).has(part)) return { to: "/projects/$project", params: { project: target }, missing: part };
  const segs = tail.split("/").filter(Boolean);
  let best = "/projects/$project";
  for (const p of pages) {
    const rest = p.replace(/^\/projects\/\$project\/?/, "").split("/").filter(Boolean);
    if (p.startsWith("/projects/$project") && !rest.some((s) => s.startsWith("$")) && rest.length <= segs.length && rest.every((s, i) => s === segs[i]) && p.length > best.length) best = p;
  }
  return { to: best, params: { project: target } };
}
