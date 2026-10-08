import { queryOptions } from "@tanstack/react-query";
import type { Manifest } from "@/api/client";
import { request } from "@/api/client";
import type { components } from "@/api/schema";
import { pickBuild, type BuildOverrides } from "@/lib/build-config";
import thumbApi from "@/assets/illustrations/starter-api.webp";
import thumbWeb from "@/assets/illustrations/starter-web.webp";
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

/** What a starter makes: the choice people start from. */
export type StarterKind = Starter["kind"];

/**
 * The kinds, named for what you make rather than the framework, in the order
 * they're offered, each with its drawing (a browser window with its database,
 * a single page, a plug meeting a socket), its one line and a first name for
 * the project. The framework is a quiet choice inside the kind; each kind's
 * default comes from the API (`default`). Drawings are transparent: put them
 * in an .art-well so dark mode gives them a paper ground.
 */
export const KINDS: Array<{ kind: StarterKind; title: string; line: string; thumb: string; name: string }> = [
  { kind: "web", title: "Web app", line: "A website with pages and a database.", thumb: thumbWeb, name: "web" },
  { kind: "static", title: "Static site", line: "A landing page, docs or portfolio. No server to run.", thumb: thumbStatic, name: "site" },
  { kind: "api", title: "API", line: "Endpoints for your mobile app or frontend.", thumb: thumbApi, name: "notes" },
];
export const kindOf = (k: string) => KINDS.find((x) => x.kind === k);
export const isKind = (k: string | undefined): k is StarterKind => !!k && KINDS.some((x) => x.kind === k);

/** A starter's drawing: its kind's. */
export const thumbOf = (s: Pick<Starter, "kind">) => kindOf(s.kind)?.thumb ?? thumbWeb;

/** The frameworks a kind offers: its listed starters, the default first. */
export const frameworksOf = (list: Starter[], kind: StarterKind) =>
  list.filter((s) => s.listed && s.kind === kind).sort((a, b) => Number(b.default) - Number(a.default));

/** A kind's default starter. */
export const defaultOf = (list: Starter[], kind: StarterKind) => frameworksOf(list, kind)[0];

/** The starter an id names. */
export const starterFor = (list: Starter[], id: string | undefined) => (id ? list.find((s) => s.id === id) : undefined);

/** The starters worth starting from, in kind order, each kind's default first. */
export const pickable = (list: Starter[]) => KINDS.flatMap((k) => frameworksOf(list, k.kind));

export const frameworkNames: Record<string, string> = {
  next: "Next.js",
  hono: "Hono",
  bun: "Bun",
  static: "Static site",
  node: "Node",
  fastapi: "FastAPI",
  python: "Python",
};
export const frameworkName = (f?: string) => (f ? (frameworkNames[f] ?? f) : "App");

/** One line for the other ways to bring code. */
export const starterLine: Record<string, string> = {
  git: "A one-time copy of any public repository: GitLab, Codeberg, anywhere.",
  none: "Just the parts below. Add an app whenever you’re ready.",
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
  if (name.includes("--")) return { ok: false, why: "Use single dashes." };
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

/** A first name to suggest for a starter, free on this box: its kind's (the guestbook demo keeps its own). */
export function suggestName(starter: Starter | undefined, taken: { projects: string[]; routes: string[] }): string {
  const base = !starter ? "project" : starter.listed ? (kindOf(starter.kind)?.name ?? starter.app) : starter.app;
  return freeName(base, taken);
}

/** base, or the first free variant of it (base-app, base-2…). */
export function freeName(base: string, taken: { projects: string[]; routes: string[] }): string {
  const ok = (n: string) => checkName(n, taken).ok;
  if (ok(base)) return base;
  for (const extra of ["app", "box", "live", "one", "two"]) if (ok(`${base}-${extra}`)) return `${base}-${extra}`;
  for (let i = 2; i < 50; i++) if (ok(`${base}-${i}`)) return `${base}-${i}`;
  return "";
}

export type Source =
  | { kind: "starter"; starter: Starter }
  | { kind: "none" }
  | { kind: "git"; url: string; ref: string; path: string; framework: string; preset: string }
  | { kind: "github"; repo: string; branch: string; path: string; framework: string; preset: string; env: Array<{ k: string; v: string }>; build?: BuildOverrides };

/** The app a source puts in the project (none for an empty project). */
export function appFor(source: Source): { name: string; framework: string } | null {
  if (source.kind === "starter") return { name: source.starter.app, framework: source.starter.framework };
  if (source.kind === "git" || source.kind === "github") return { name: "web", framework: source.framework };
  return null;
}

/**
 * The whole manifest for a new project: its app (from the source) and the
 * services a starter adds (Database, KV, Files, Email and Analytics are
 * always there). A starter's app sets no routes, so the box serves it at the
 * project's own name (guestbook.<domain>), never on another project's
 * hostname.
 */
export function newProjectManifest(project: string, source: Source): Manifest {
  const m: Manifest = { project, version: 1 };
  const services: Record<string, unknown> = {};
  if (source.kind === "starter") {
    const f = source.starter.fragment;
    const apps: Record<string, Record<string, unknown>> = {};
    for (const [name, spec] of Object.entries(f.apps)) {
      apps[name] = { ...spec };
    }
    m.apps = apps as unknown as Manifest["apps"];
    Object.assign(services, structuredClone(f.services ?? {}));
    if (f.env && Object.keys(f.env).length) m.env = { ...f.env };
  } else if (source.kind === "git") {
    m.apps = { web: { framework: source.framework } } as unknown as Manifest["apps"];
  } else if (source.kind === "github") {
    const path = source.path.trim().replace(/^\/+|\/+$/g, "");
    const git = { repo: source.repo, branch: source.branch, ...(path ? { path } : {}) };
    m.apps = { web: { framework: source.framework, git, ...pickBuild(source.build, source.framework) } } as unknown as Manifest["apps"];
  }
  if (Object.keys(services).length) m.services = services as Manifest["services"];
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
