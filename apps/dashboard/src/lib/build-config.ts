import type { ManifestApp } from "@/api/client";
import type { StagedEdit } from "@/lib/staged";

/**
 * An app's build and deploy settings as the Build and deploy form edits
 * them, and the manifest edits that save them. Every field is optional in
 * tiffin.config.ts and defaults to what the box detects; an override is a
 * string, a field left to detection is undefined.
 */
export type Builder = "" | "dockerfile" | "prebuilt";

export type BuildDraft = {
  framework: string;
  /** The app's folder: git.path for a GitHub app, else path ("" is the top). */
  root: string;
  builder: Builder;
  dockerfile: string;
  target: string;
  install?: string;
  build?: string;
  output?: string;
  /** The start command (the manifest's `command`). */
  start?: string;
  runtime: "" | "node";
  /** "" is the default ("/"). */
  healthcheck: string;
  release: string;
  /** One pattern per line. */
  watch: string;
};

export type Command = "install" | "build" | "output" | "start";

export const COMMANDS: Command[] = ["install", "build", "output", "start"];

export const commandLabel: Record<Command, string> = {
  install: "Install command",
  build: "Build command",
  output: "Output directory",
  start: "Start command",
};

const PYTHON = new Set(["fastapi", "python"]);

const NAMES: Record<string, string> = { next: "Next.js", hono: "Hono", bun: "a Bun or Node server", static: "a static site", fastapi: "FastAPI", python: "a Python server" };
const frameworkName = (f: string) => NAMES[f] ?? f;
export const isPython = (framework: string) => PYTHON.has(framework);

export function draftOf(spec: ManifestApp): BuildDraft {
  const b = spec.builder === "dockerfile" || spec.builder === "prebuilt" ? spec.builder : "";
  return {
    framework: spec.framework,
    root: (spec.git ? (spec.git.path ?? "") : spec.path === "." ? "" : (spec.path ?? "")).replace(/^\/+|\/+$/g, ""),
    builder: b,
    dockerfile: spec.dockerfile ?? "",
    target: spec.target ?? "",
    install: spec.install || undefined,
    build: spec.build || undefined,
    output: spec.output || undefined,
    start: spec.command || undefined,
    runtime: spec.runtime === "node" ? "node" : "",
    healthcheck: spec.healthcheck && spec.healthcheck !== "/" ? spec.healthcheck : "",
    release: spec.release ?? "",
    watch: (spec.watch ?? []).join("\n"),
  };
}

/** Which rows a draft shows: what its framework and builder can use. */
export function rowsFor(d: BuildDraft, role?: string) {
  const isStatic = d.framework === "static";
  const image = d.builder !== "";
  return {
    builder: !isStatic,
    install: true,
    build: true,
    /** Commands an image brings itself: shown, but set in the Dockerfile. */
    commandsInImage: image,
    output: !image && (isStatic || d.framework === "next"),
    start: !isStatic,
    runtime: !isStatic && !image && d.framework !== "hono" && !isPython(d.framework),
    healthcheck: !isStatic && role !== "worker",
    release: !isStatic,
  };
}

/** What the box does when a command isn't overridden, said the way the field shows it. */
export function detected(d: BuildDraft): Record<Command, string> {
  if (d.builder === "dockerfile") return { install: "Set in the Dockerfile", build: "Set in the Dockerfile", output: "", start: "The image’s own command (CMD)" };
  if (d.builder === "prebuilt") return { install: "Built elsewhere", build: "Built elsewhere", output: "", start: "The image’s own command (CMD)" };
  const node = d.runtime === "node";
  if (isPython(d.framework))
    return { install: "uv sync, by your project files (Railpack)", build: "None", output: "", start: d.framework === "fastapi" ? "uvicorn, its app found for you, on $PORT" : "Detected from your project, on $PORT" };
  const install = node ? "npm install, or your lockfile’s" : "bun install, or your lockfile’s";
  if (d.framework === "static") return { install: "bun install", build: "bun run build, if there is one", output: "dist, build, out or public", start: "" };
  const build = node ? "npm run build" : "bun --bun run build";
  if (d.framework === "next") return { install, build, output: "out, for a static export", start: node ? "next start" : "bun --bun next start" };
  return { install, build, output: "", start: node ? "npm run start" : "bun --bun run start" };
}

/** The draft's problems, by field: what Save would be refused for. */
export function problems(d: BuildDraft): Partial<Record<keyof BuildDraft, string>> {
  const out: Partial<Record<keyof BuildDraft, string>> = {};
  const rel = /^(?!\/)(?!.*(^|\/)\.\.?(\/|$))[^\s]+$/;
  for (const c of COMMANDS) {
    const v = d[c];
    if (v !== undefined && !v.trim()) out[c] = "Type a command, or turn Override off to use the detected one.";
  }
  if (d.output && d.output !== "." && !rel.test(d.output)) out.output = "A folder inside the app, like dist.";
  if (d.root && !rel.test(d.root)) out.root = "A folder inside the repository, like apps/web.";
  if (d.builder === "dockerfile" && d.dockerfile && !rel.test(d.dockerfile)) out.dockerfile = "A file inside the app’s folder, like Dockerfile or docker/web.Dockerfile.";
  if (d.target && !/^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$/.test(d.target)) out.target = "A stage name from the Dockerfile (FROM … AS name).";
  if (d.healthcheck && !d.healthcheck.startsWith("/")) out.healthcheck = "A path that starts with /, like /api/health.";
  for (const w of watchList(d.watch)) {
    if (w.replace(/^!/, "").split("/").includes("..")) out.watch = `“${w}” reaches outside the repository.`;
  }
  return out;
}

export const watchList = (s: string) =>
  s
    .split("\n")
    .map((l) => l.trim())
    .filter((l, i, all) => l && all.indexOf(l) === i);

const q = (s: string) => `“${s}”`;

/**
 * The manifest edits that turn spec into the draft, each with the words
 * History and the toast show. A builder that brings its own image clears the
 * settings it would refuse (install, build, output, runtime, packages).
 */
export function editsFor(app: string, spec: ManifestApp, d: BuildDraft): StagedEdit[] {
  const edits: StagedEdit[] = [];
  const at = (...p: string[]) => ["apps", app, ...p];
  const set = (path: string[], from: unknown, to: unknown, what: string, undo: string) => {
    if (JSON.stringify(from ?? null) !== JSON.stringify(to ?? null)) edits.push({ kind: "set", path, from, to, what, undo });
  };
  const image = d.builder !== "";
  const was = draftOf(spec);

  if (d.framework !== spec.framework) set(at("framework"), spec.framework, d.framework, `Set ${app}’s framework to ${frameworkName(d.framework)}`, `${app} builds as ${frameworkName(spec.framework)} again`);

  if (d.root !== was.root) {
    const where = d.root || "the top of the repository";
    if (spec.git) set(at("git", "path"), spec.git.path, d.root || undefined, `Set ${app}’s root directory to ${where}`, `${app} builds from ${was.root || "the top of the repository"} again`);
    else set(at("path"), spec.path, d.root || ".", `Set ${app}’s root directory to ${where}`, `${app} builds from ${was.root || "its top folder"} again`);
  }

  const builderWords: Record<Builder, string> = { "": "automatic (Railpack)", dockerfile: "its Dockerfile", prebuilt: "prebuilt images only" };
  if (d.builder !== was.builder) set(at("builder"), spec.builder, d.builder || undefined, `Set ${app}’s builder to ${builderWords[d.builder]}`, `${app}’s builder goes back to ${builderWords[was.builder]}`);
  const df = d.builder === "dockerfile" ? d.dockerfile.trim().replace(/^\.\//, "") : "";
  const dfTo = df && df !== "Dockerfile" ? df : undefined;
  set(
    at("dockerfile"),
    spec.dockerfile,
    dfTo,
    dfTo ? `Set ${app}’s Dockerfile to ${dfTo}` : `Set ${app}’s Dockerfile to the default`,
    `${app} builds from ${spec.dockerfile || "Dockerfile"} again`,
  );
  const target = d.builder === "dockerfile" ? d.target.trim() : "";
  set(
    at("target"),
    spec.target,
    target || undefined,
    target ? `Set ${app}’s build stage to ${target}` : `Set ${app}’s build stage to the last one`,
    `${app} builds ${spec.target ? `the ${spec.target} stage` : "the last stage"} again`,
  );

  const cmd = (c: Command, field: keyof ManifestApp, label: string) => {
    const keep = c === "start" || !image;
    const to = keep && d[c] !== undefined ? d[c]!.trim() : undefined;
    const from = spec[field] as string | undefined;
    set(
      at(field),
      from,
      to || undefined,
      to ? `Set ${app}’s ${label} to ${q(to)}` : `Set ${app}’s ${label} back to the detected one`,
      from ? `${app}’s ${label} goes back to ${q(from)}` : `${app} uses the detected ${label} again`,
    );
  };
  cmd("install", "install", "install command");
  cmd("build", "build", "build command");
  cmd("output", "output", "output directory");
  cmd("start", "command", "start command");
  const outputOK = !image && (d.framework === "static" || d.framework === "next");
  if (!outputOK && spec.output) set(at("output"), spec.output, undefined, `Set ${app}’s output directory back to the detected one`, `${app} serves ${spec.output} again`);

  const runtime = !image && d.framework !== "static" && d.framework !== "hono" && !isPython(d.framework) && d.runtime === "node" ? "node" : undefined;
  set(at("runtime"), spec.runtime, runtime, runtime ? `Set ${app}’s runtime to Node.js` : `Set ${app}’s runtime to Bun`, `${app} goes back to ${spec.runtime === "node" ? "Node.js" : "Bun"}`);
  if (d.builder === "dockerfile" && spec.packages?.length) set(at("packages"), spec.packages, undefined, `Remove ${app}’s packages: its Dockerfile installs them`, `${app}’s image gets ${spec.packages.join(", ")} again`);

  const hc = d.framework === "static" ? "" : d.healthcheck.trim();
  if (hc !== was.healthcheck) set(at("healthcheck"), spec.healthcheck, hc || undefined, `Set ${app}’s health check to ${hc || "/"}`, `${app}’s health check goes back to ${spec.healthcheck || "/"}`);
  const rel = d.framework === "static" ? "" : d.release.trim();
  set(at("release"), spec.release, rel || undefined, rel ? `Set ${app}’s release command to ${q(rel)}` : `Remove ${app}’s release command`, spec.release ? `${app} runs ${q(spec.release)} before releases again` : `${app} runs nothing before releases again`);
  const watch = watchList(d.watch);
  set(
    at("watch"),
    spec.watch ?? undefined,
    watch.length ? watch : undefined,
    watch.length ? `Set ${app}’s watch paths to ${watch.join(", ")}` : `Remove ${app}’s watch paths`,
    spec.watch?.length ? `${app} deploys only for changes under ${spec.watch.join(", ")} again` : `${app} deploys on every push again`,
  );
  return edits;
}

/** Whether the draft differs from the spec (what Save would change). */
export const isDirty = (app: string, spec: ManifestApp, d: BuildDraft) => editsFor(app, spec, d).length > 0;

/** Build overrides picked while importing a repository (New project, Add app). */
export type BuildOverrides = { builder?: "dockerfile"; dockerfile?: string; install?: string; build?: string; output?: string; command?: string };

/** The manifest fields for picked overrides: only the ones set, trimmed. */
export function pickBuild(b?: BuildOverrides, framework?: string): Partial<ManifestApp> {
  if (!b) return {};
  const out: Record<string, string> = {};
  const image = b.builder === "dockerfile" && framework !== "static";
  if (image) out.builder = "dockerfile";
  const df = b.dockerfile?.trim().replace(/^\.\//, "");
  if (image && df && df !== "Dockerfile") out.dockerfile = df;
  for (const k of ["install", "build", "output", "command"] as const) {
    const v = b[k]?.trim();
    if (!v) continue;
    if (image && k !== "command") continue;
    if (k === "output" && framework !== "static" && framework !== "next") continue;
    if (k === "command" && framework === "static") continue;
    out[k] = v;
  }
  return out as Partial<ManifestApp>;
}
