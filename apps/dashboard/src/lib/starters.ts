import { queryOptions } from "@tanstack/react-query";
import type { Manifest } from "@/api/client";
import { request } from "@/api/client";
import type { components } from "@/api/schema";
import thumbApi from "@/assets/illustrations/starter-api.webp";
import thumbNext from "@/assets/illustrations/starter-next.webp";
import thumbStatic from "@/assets/illustrations/starter-static.webp";

/**
 * Starters: the templates that ship inside the box (GET /v1/templates), and
 * what the dashboard needs to start a project or an app from one without a
 * terminal: a thumbnail, a manifest built from the starter's fragment, and
 * the rules a project name has to follow.
 */
export type Starter = components["schemas"]["Starter"];
export type StarterId = components["schemas"]["RuntimeTemplateBody"]["template"];

export const startersQuery = queryOptions({
  queryKey: ["templates"],
  queryFn: async () => (await request<{ templates: Starter[] | null }>("GET", "/v1/templates")).templates ?? [],
  staleTime: Infinity,
  retry: false,
});

/**
 * Thumbnails by starter id: a single open tier (a static site), a closed tin
 * with an order slip (an API), three open tiers side by side (a full-stack
 * app). The guestbook demo is full-stack too. Transparent; put them in an
 * .art-well so dark mode gives them a paper ground.
 */
export const starterThumb: Record<string, string> = {
  "static-site": thumbStatic,
  "hono-postgres": thumbApi,
  guestbook: thumbNext,
  "next-postgres": thumbNext,
};

/**
 * The starters people pick from, in order: a full-stack site, an API, a static
 * site. (The guestbook ships as a demo; it isn't offered as a starting point.)
 */
export const starterOrder = ["next-postgres", "hono-postgres", "static-site"];
/** Only the starters worth starting from, in their order. */
export const pickable = <T extends { id: string }>(list: T[]) =>
  list.filter((s) => starterOrder.includes(s.id)).sort((a, b) => starterOrder.indexOf(a.id) - starterOrder.indexOf(b.id));

/** What each starter is called in the picker. */
export const starterTitle: Record<string, string> = {
  "next-postgres": "Next.js app",
  "hono-postgres": "API",
  "static-site": "Static site",
  guestbook: "Guestbook",
};

export const frameworkNames: Record<string, string> = {
  next: "Next.js",
  hono: "Hono",
  bun: "Bun",
  static: "Static site",
  node: "Node",
};
export const frameworkName = (f?: string) => (f ? (frameworkNames[f] ?? f) : "App");

/** What each starter is for, in one line. */
export const starterLine: Record<string, string> = {
  "next-postgres": "A website with pages and a database.",
  "hono-postgres": "Endpoints for your mobile app or frontend.",
  "static-site": "A landing page, docs or portfolio. Live instantly.",
  guestbook: "A page, an API, a database, a KV store and analytics in one app.",
  git: "Your own code from GitHub or any git URL.",
  empty: "Start with nothing and add pieces as you go.",
};

/** The file to change first in each starter's source (what tiffin pull writes). */
export const starterEdit: Record<string, string> = {
  "next-postgres": "app/page.jsx",
  "hono-postgres": "index.ts",
  "static-site": "public/index.html",
  guestbook: "public/index.html",
};

/** Hostnames the box keeps for itself. */
const RESERVED = new Set(["dashboard", "s3", "www", "api", "t", "mail", "auth", "git"]);

export type NameCheck = { ok: true } | { ok: false; why: string };

/** A project (and its route) name: a lowercase slug the box can use as a hostname. */
export function checkName(name: string, taken: { projects: string[]; routes: string[] }): NameCheck {
  if (!name) return { ok: false, why: "Give it a name." };
  if (!/^[a-z]/.test(name)) return { ok: false, why: "Start with a letter." };
  if (/[^a-z0-9-]/.test(name)) return { ok: false, why: "Lowercase letters, digits and dashes only." };
  if (name.length > 40) return { ok: false, why: "Keep it to 40 characters." };
  if (name.endsWith("-")) return { ok: false, why: "End with a letter or a digit." };
  if (taken.projects.includes(name)) return { ok: false, why: `There’s already a project called ${name}.` };
  if (RESERVED.has(name)) return { ok: false, why: `The box keeps ${name}.… for itself. Pick another name.` };
  if (taken.routes.includes(name)) return { ok: false, why: `An app already answers at ${name}.…` };
  return { ok: true };
}

/** What people type, made into a slug as they type it: "My Shop!" → "my-shop". */
export function slugify(s: string): string {
  return s
    .toLowerCase()
    .replace(/[\s_.]+/g, "-")
    .replace(/[^a-z0-9-]/g, "")
    .replace(/-{2,}/g, "-")
    .replace(/^[-0-9]+/, "")
    .slice(0, 40);
}

/** A first name to suggest for a starter, free on this box. */
export function suggestName(starter: Starter | undefined, taken: { projects: string[]; routes: string[] }): string {
  return freeName(starter ? ({ "static-site": "site", "hono-postgres": "notes", guestbook: "guestbook", "next-postgres": "web" }[starter.id] ?? starter.app) : "project", taken);
}

/** base, or the first free variant of it (base-app, base-2…). */
export function freeName(base: string, taken: { projects: string[]; routes: string[] }): string {
  const ok = (n: string) => checkName(n, taken).ok;
  if (ok(base)) return base;
  for (const extra of ["app", "box", "live", "one", "two"]) if (ok(`${base}-${extra}`)) return `${base}-${extra}`;
  for (let i = 2; i < 50; i++) if (ok(`${base}-${i}`)) return `${base}-${i}`;
  return "";
}

/**
 * The standalone starters: a project with only one part, no app. Its sidebar
 * shows only that part, so it works like that part's console. A schedule
 * project starts empty and gets its first schedule on its Jobs page.
 */
export type SoloPart = "postgres" | "valkey" | "storage" | "jobs";
export const soloParts: Array<{ part: SoloPart; title: string; line: string; name: string; services?: Manifest["services"] }> = [
  { part: "postgres", title: "Just a database", line: "Tables you can edit here, SQL, and a URL for your own tools.", name: "data", services: { postgres: {} } },
  { part: "valkey", title: "Just KV", line: "A Redis-compatible key-value store, also a cache.", name: "kv", services: { valkey: { maxMemoryMB: 64 } } },
  { part: "storage", title: "Just files", line: "S3-compatible buckets for uploads and assets.", name: "uploads", services: { storage: { buckets: { files: { public: false } } } } as Manifest["services"] },
  { part: "jobs", title: "Just a schedule", line: "Call any web address on a timer, retried until it answers.", name: "schedules" },
];

export type Source =
  | { kind: "starter"; starter: Starter }
  | { kind: "part"; part: SoloPart }
  | { kind: "empty" }
  | { kind: "git"; url: string; ref: string; path: string; framework: string; postgres: boolean }
  | { kind: "github"; repo: string; branch: string; path: string; framework: string; postgres: boolean; env: Array<{ k: string; v: string }> };

/** The app a source puts in the project (none for an empty project). */
export function appFor(source: Source): { name: string; framework: string } | null {
  if (source.kind === "starter") return { name: source.starter.app, framework: source.starter.framework };
  if (source.kind === "git" || source.kind === "github") return { name: "web", framework: source.framework };
  return null;
}

/**
 * The whole manifest for a new project: the starter’s fragment. Its app sets no
 * routes, so the box serves it at the project’s own name (guestbook.<domain>),
 * never on another project’s hostname.
 */
export function newProjectManifest(project: string, source: Source): Manifest {
  const m: Manifest = { project, version: 1 };
  if (source.kind === "starter") {
    const f = source.starter.fragment;
    const apps: Record<string, Record<string, unknown>> = {};
    for (const [name, spec] of Object.entries(f.apps)) {
      apps[name] = { ...spec };
    }
    m.apps = apps as unknown as Manifest["apps"];
    if (f.services && Object.keys(f.services).length) m.services = structuredClone(f.services) as Manifest["services"];
    if (f.env && Object.keys(f.env).length) m.env = { ...f.env };
  } else if (source.kind === "part") {
    const s = soloParts.find((x) => x.part === source.part)?.services;
    if (s) m.services = structuredClone(s);
  } else if (source.kind === "git") {
    m.apps = { web: { framework: source.framework } } as unknown as Manifest["apps"];
    if (source.postgres) m.services = { postgres: {} };
  } else if (source.kind === "github") {
    const path = source.path.trim().replace(/^\/+|\/+$/g, "");
    const git = { repo: source.repo, branch: source.branch, ...(path ? { path } : {}) };
    m.apps = { web: { framework: source.framework, git } } as unknown as Manifest["apps"];
    if (source.postgres) m.services = { postgres: {} };
  }
  return m;
}

/** A git URL the box will accept: https, a host, a path. */
export function checkGitUrl(url: string): NameCheck {
  if (!url.trim()) return { ok: false, why: "Paste the repository’s https address." };
  try {
    const u = new URL(url.trim());
    if (u.protocol !== "https:") return { ok: false, why: "Only https addresses: the box clones public repositories without credentials." };
    if (u.username || u.password) return { ok: false, why: "Leave credentials out; the box only clones public repositories." };
    if (u.pathname.split("/").filter(Boolean).length < 2) return { ok: false, why: "That looks like a host, not a repository (owner/name)." };
    return { ok: true };
  } catch {
    return { ok: false, why: "That isn’t a web address." };
  }
}

/** The repository's name, as a project name: https://github.com/acme/shop-web → shop-web. */
export function nameFromGit(url: string): string {
  try {
    const parts = new URL(url.trim()).pathname.split("/").filter(Boolean);
    return slugify((parts[1] ?? "").replace(/\.git$/, ""));
  } catch {
    return "";
  }
}

type Deploy = components["schemas"]["RuntimeDeploy"];
const appPath = (p: string, app: string) => `/v1/projects/${encodeURIComponent(p)}/apps/${encodeURIComponent(app)}`;

/** Deploys a starter's source to an app that already exists with the starter's framework. */
export const deployTemplate = (project: string, app: string, template: string) =>
  request<Deploy>("POST", `${appPath(project, app)}/deploys/template`, { template });

/** Deploys one commit of a public https repository to an app that already exists. */
export const deployGit = (project: string, app: string, body: { url: string; ref?: string; path?: string }) =>
  request<Deploy>("POST", `${appPath(project, app)}/deploys/git`, {
    url: body.url.trim(),
    ...(body.ref?.trim() ? { ref: body.ref.trim() } : {}),
    ...(body.path?.trim() ? { path: body.path.trim().replace(/^\/+|\/+$/g, "") } : {}),
  });

/**
 * An app added from a starter or a git URL through the plan tray has nothing
 * deployed yet; this remembers (for this tab) what it should be built from,
 * so its page can offer that deploy as the next step.
 */
export type NextDeploy = { template: string } | { git: { url: string; ref?: string; path?: string } };
const NEXT_KEY = "tiffin.next-deploy";
function nextAll(): Record<string, NextDeploy> {
  try {
    return JSON.parse(sessionStorage.getItem(NEXT_KEY) ?? "{}") as Record<string, NextDeploy>;
  } catch {
    return {};
  }
}
export function rememberNextDeploy(project: string, app: string, d: NextDeploy | null) {
  const all = nextAll();
  if (d) all[`${project}/${app}`] = d;
  else delete all[`${project}/${app}`];
  try {
    sessionStorage.setItem(NEXT_KEY, JSON.stringify(all));
  } catch {
    /* storage blocked: the app page just won't suggest it */
  }
}
export const nextDeployFor = (project: string, app: string): NextDeploy | undefined => nextAll()[`${project}/${app}`];
