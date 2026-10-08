import type { RegisteredRouter } from "@tanstack/react-router";
import type { ProjectState } from "@/api/client";
import { PARTS } from "./names";

/** Every page of one project whose only path param is $project: `{ to, params: { project } }` type-checks for each. */
export type ProjectPage = Exclude<
  Extract<keyof RegisteredRouter["routesByPath"], "/projects/$project" | `/projects/$project/${string}`>,
  `/projects/$project/${string}$${string}`
>;

/**
 * A project's sections: which parts it has, where each one's page is, which
 * part a page belongs to, and where to land when you switch projects (the
 * same section when the other project has it, else its Overview).
 */
export type Part = "apps" | "postgres" | "valkey" | "storage" | "email" | "auth" | "analytics" | "jobs";

export const PART_PAGE: Record<Part, { to: ProjectPage; label: string }> = {
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

/**
 * Every project always has these: Database, KV, Files, Email, Analytics and Jobs (manifest.AlwaysOn on the box;
 * Jobs needs nothing set up). Auth is the one that is added.
 */
export const ALWAYS: readonly Part[] = ["postgres", "valkey", "storage", "email", "analytics", "jobs"];

/** The parts a project has: the always-there ones, its apps and Auth when it has them. */
export function partsOf(state?: ProjectState): Set<Part> {
  const res = state?.resources ?? [];
  const out = new Set<Part>(state ? ALWAYS : []);
  if (res.some((r) => r.address.startsWith("app/"))) out.add("apps");
  if (res.some((r) => r.address === "service/auth")) out.add("auth");
  return out;
}

/** Where a project opens: its Overview. */
export function projectHome(project: string): { to: ProjectPage; params: { project: string } } {
  return { to: "/projects/$project", params: { project } };
}

/**
 * Where switching to `target` lands, coming from `path`: the deepest page of
 * the same section that has no names in it (bookshop › Database › SQL →
 * blog › Database › SQL; a table or a job is left behind), when the target
 * has that part. `pages` are the router's project page paths.
 */
export function landing(path: string, target: string, state: ProjectState | undefined, pages: string[]): { to: ProjectPage; params: { project: string }; missing?: Part } {
  const tail = path.match(/^\/projects\/[^/]+\/?(.*)$/)?.[1];
  if (tail === undefined || tail === "") return projectHome(target);
  const part = partOfPath(tail);
  if (part && !partsOf(state).has(part)) return { to: "/projects/$project", params: { project: target }, missing: part };
  const segs = tail.split("/").filter(Boolean);
  let best: string = "/projects/$project";
  for (const p of pages) {
    const rest = p.replace(/^\/projects\/\$project\/?/, "").split("/").filter(Boolean);
    if (p.startsWith("/projects/$project") && !rest.some((s) => s.startsWith("$")) && rest.length <= segs.length && rest.every((s, i) => s === segs[i]) && p.length > best.length) best = p;
  }
  // best is one of the router's own paths with no param but $project (filtered above).
  return { to: best as ProjectPage, params: { project: target } };
}
