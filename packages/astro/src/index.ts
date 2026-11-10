/**
 * `@shiptiffin/astro`: an Astro integration that makes a site with server
 * routes run well on a ShipTiffin box.
 *
 *   // astro.config.mjs
 *   import tiffin from "@shiptiffin/astro";
 *   export default defineConfig({ integrations: [tiffin()] });
 *
 * Each feature has an option that is on by default, and each one steps aside
 * (with a log line) when what it needs is missing, so `astro dev` and builds
 * off the box keep working. Settings the project made itself always win.
 */
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import type { AstroIntegration, AstroIntegrationLogger } from "astro";
import type { ValkeyCacheConfig } from "./cache-provider";
import { BUILDING } from "./building";
import { appHosts, missingDomains, type AllowedDomain } from "./hosts";
import { scanProject, type ProjectScan } from "./scan";
import { DEFAULT_SESSION_TTL } from "./defaults";
import type { SessionDriverConfig } from "./session-driver";
import { ADAPTER_ENTRY, shutdownEntrySource } from "./shutdown";

export type { AllowedDomain } from "./hosts";
export type { ValkeyCacheConfig } from "./cache-provider";
export type { SessionDriverConfig } from "./session-driver";

export interface TiffinAstroOptions {
  /**
   * Use `@astrojs/node` in standalone mode when the project has no adapter and
   * has a route rendered on demand (`export const prerender = false`,
   * `output: "server"`, `server:defer` or Astro Actions). Default true.
   */
  adapter?: boolean;
  /**
   * Trust the box's edge for the app's own hosts: adds them (TIFFIN_HOSTS, else
   * the host of TIFFIN_URL) to `security.allowedDomains`, after the project's
   * own entries. Default true.
   */
  proxy?: boolean;
  /** Set `site` from TIFFIN_URL when the project has none. Default true. */
  site?: boolean;
  /**
   * Astro sessions in the box's Valkey (REDIS_URL, VALKEY_PREFIX + "sess:")
   * when the project has not chosen a session driver. Default true.
   * `ttl`: seconds a session lives after its last use (default: the project's
   * `session.ttl`, else 30 days).
   */
  sessions?: boolean | SessionDriverConfig;
  /**
   * Route caching (`Astro.cache`, `routeRules`) in the box's Valkey, shared by
   * every instance. "auto" (the default) turns it on when the project uses
   * `cache.set()`/`cache.invalidate()` or has `routeRules` with `maxAge`/`swr`/`tags`;
   * true always; an object is true with these settings.
   */
  cache?: boolean | "auto" | ValkeyCacheConfig;
  /**
   * A health route for the box's health check. Default true: `/_health`
   * (`/_health/` with trailingSlash "always"), 200 JSON. A string: your own
   * route at that path; nothing is added. `{ database: true }` also checks
   * that Postgres at DATABASE_URL answers (503 when it doesn't).
   */
  health?: boolean | string | { path?: string; database?: boolean };
  /**
   * Graceful shutdown for @astrojs/node standalone: on SIGTERM stop accepting
   * connections and finish in-flight requests for up to `timeout` seconds
   * (default 25; the box waits 30 before it kills the app). Default true.
   */
  shutdown?: boolean | { timeout?: number };
  /** Warn at build when server islands are used and ASTRO_KEY is not set. Default true. */
  astroKey?: boolean;
  /**
   * Reserved, not implemented yet: sign-in for on-demand pages. Today use
   * `authMiddleware` from `@shiptiffin/sdk/astro` in src/middleware.ts.
   */
  auth?: { protect?: string[]; signIn?: string };
}

/** The part of Astro's resolved config the plan reads. */
export interface ConfigView {
  root: URL;
  srcDir: URL;
  cacheDir: URL;
  site?: string | undefined;
  output?: string;
  trailingSlash?: string;
  adapter?: { name: string } | undefined;
  session?: false | { driver?: unknown; ttl?: number | undefined; cookie?: unknown } | undefined;
  security?: { allowedDomains?: AllowedDomain[] };
  cache?: { provider?: unknown } | undefined;
  routeRules?: Record<string, { maxAge?: number; swr?: number; tags?: string[] }> | undefined;
  build?: { serverEntry?: string };
}

export interface Plan {
  /** Passed to updateConfig. */
  update: Record<string, unknown>;
  /** Add @astrojs/node (standalone). */
  addNode: boolean;
  /** The adapter is (or will be) @astrojs/node. */
  node: boolean;
  /** Route to inject. */
  inject?: { pattern: string; entrypoint: string };
  /** Write the graceful-shutdown entry.mjs after the build, with this timeout (s). */
  shutdown?: number;
  info: string[];
  warn: string[];
}

export interface PlanInput {
  config: ConfigView;
  command: string;
  env: Record<string, string | undefined>;
  scan: ProjectScan;
  /** Whether a module resolves from the project root. */
  resolves: (spec: string) => boolean;
  /** Absolute paths of this package's runtime modules. */
  entry: (name: "session-driver" | "cache-provider" | "health" | "health-db") => string;
}

const NODE = "@astrojs/node";

function routeRulesCache(rules: ConfigView["routeRules"]): boolean {
  return Object.values(rules ?? {}).some((r) => r && (r.maxAge !== undefined || r.swr !== undefined || r.tags !== undefined));
}

/** Whether the session driver is the one @astrojs/node sets when the project set none. */
export function isNodeDefaultSession(config: ConfigView): boolean {
  const d = config.session && (config.session.driver as { config?: { base?: string } } | undefined);
  if (!d || typeof d !== "object") return false;
  return d.config?.base === fileURLToPath(new URL("sessions", config.cacheDir));
}

/** What tiffin() changes, from the config, the environment and the source. Pure: no I/O. */
export function plan(opts: TiffinAstroOptions, input: PlanInput): Plan {
  const { config, env, scan, command } = input;
  const p: Plan = { update: {}, addNode: false, node: false, info: [], warn: [] };
  // 1. Adapter
  const needsServer =
    config.output === "server" || scan.onDemandPages.length > 0 || scan.serverIslands.length > 0 || scan.actions;
  if (config.adapter) {
    p.node = config.adapter.name === NODE;
    if (p.node) p.info.push(`adapter: ${NODE} (yours).`);
    else p.warn.push(`adapter "${config.adapter.name}" is not ${NODE}: left as is; the box runs @astrojs/node standalone servers, and graceful shutdown is skipped.`);
  } else if (opts.adapter === false) {
    p.info.push("adapter: off (option).");
  } else if (!needsServer) {
    p.info.push("adapter: none needed (every page is prerendered); the box serves the site as files.");
  } else if (!input.resolves(NODE)) {
    p.warn.push(`adapter: this site renders pages on demand but ${NODE} is not installed. Run: npm i ${NODE}`);
  } else {
    p.addNode = true;
    p.node = true;
    const why = scan.onDemandPages.length ? `${scan.onDemandPages.length} on-demand page(s)` : config.output === "server" ? 'output: "server"' : scan.serverIslands.length ? "server islands" : "Astro Actions";
    p.info.push(`adapter: ${NODE} (standalone), for ${why}.`);
  }
  const server = Boolean(config.adapter) || p.addNode;

  // 2. Proxy trust and 3. site
  const hosts = appHosts(env);
  if (opts.proxy !== false) {
    if (hosts.domains.length === 0) {
      p.info.push("proxy: TIFFIN_HOSTS and TIFFIN_URL are not set (not on the box); X-Forwarded-Host stays untrusted.");
    } else {
      const add = missingDomains(config.security?.allowedDomains, hosts.domains);
      if (add.length) p.update.security = { allowedDomains: add };
      p.info.push(`proxy: trusting forwarded headers for ${hosts.domains.map((d) => `${d.protocol}://${d.hostname}${d.port ? ":" + d.port : ""}`).join(", ")} (from ${hosts.source}).`);
    }
  }
  if (opts.site !== false) {
    if (config.site) p.info.push(`site: ${config.site} (yours).`);
    else if (hosts.url) {
      p.update.site = hosts.url;
      p.info.push(`site: ${hosts.url} (from TIFFIN_URL).`);
    } else p.info.push("site: TIFFIN_URL is not set; site left unset.");
  }

  const hasRedis = Boolean(env.REDIS_URL);
  const sdk = () => input.resolves("@shiptiffin/sdk/kv");

  // 4. Sessions
  const sess = opts.sessions;
  if (sess !== false) {
    const userDriver = config.session !== false && config.session?.driver && !isNodeDefaultSession(config);
    if (config.session === false) p.info.push("sessions: off in your config (session: false).");
    else if (userDriver) p.info.push("sessions: your session driver is kept.");
    else if (!server) p.info.push("sessions: no adapter, so no sessions.");
    else if (!hasRedis) p.info.push("sessions: REDIS_URL is not set (not on the box); Astro's default driver is kept.");
    else if (!sdk()) p.warn.push("sessions: @shiptiffin/sdk is not installed, so sessions can't use Valkey. Run: npm i @shiptiffin/sdk");
    else {
      const o = typeof sess === "object" ? sess : {};
      const userTtl = config.session ? config.session.ttl : undefined;
      const driverConfig: SessionDriverConfig = { namespace: o.namespace ?? "sess:", ttl: o.ttl ?? userTtl ?? DEFAULT_SESSION_TTL };
      p.update.session = { driver: { entrypoint: input.entry("session-driver"), config: driverConfig } };
      p.info.push(`sessions: Valkey (VALKEY_PREFIX + "${driverConfig.namespace}", ${driverConfig.ttl} s since last use).`);
    }
  }

  // 5. Route cache
  const c = opts.cache ?? "auto";
  if (c !== false) {
    const wanted = c === "auto" ? scan.cacheCalls.length > 0 || routeRulesCache(config.routeRules) : true;
    if (config.cache?.provider) p.info.push("cache: your cache provider is kept.");
    else if (!wanted) p.info.push("cache: not used (no cache.set() or routeRules); no provider added.");
    else if (!server) p.info.push("cache: no adapter, so no route cache.");
    else if (!hasRedis) p.info.push("cache: REDIS_URL is not set (not on the box); no provider added.");
    else if (!sdk()) p.warn.push("cache: @shiptiffin/sdk is not installed, so the route cache can't use Valkey. Run: npm i @shiptiffin/sdk");
    else {
      const cfg = typeof c === "object" ? c : {};
      p.update.cache = { provider: { name: "tiffin-valkey", entrypoint: input.entry("cache-provider"), config: cfg } };
      p.info.push("cache: route cache in Valkey, shared by every instance.");
    }
  }

  // 6. Health route
  const h = opts.health ?? true;
  if (h === false) p.info.push("health: off (option).");
  else if (typeof h === "string") p.info.push(`health: your route ${h}; set healthcheck: "${h}" in tiffin.config.ts.`);
  else if (!server) p.info.push("health: no adapter, so no health route (the box checks the static site itself).");
  else {
    const o = typeof h === "object" ? h : {};
    const path = o.path ?? "/_health";
    p.inject = { pattern: path, entrypoint: input.entry(o.database ? "health-db" : "health") };
    const shown = config.trailingSlash === "always" && !path.endsWith("/") ? path + "/" : path;
    p.info.push(`health: ${shown}${o.database ? " (with a Postgres check)" : ""}; set healthcheck: "${shown}" in tiffin.config.ts.`);
  }

  // 7. Graceful shutdown
  const sd = opts.shutdown ?? true;
  if (sd !== false && p.node && command === "build") {
    const entry = config.build?.serverEntry ?? "entry.mjs";
    if (entry !== "entry.mjs") p.info.push(`shutdown: build.serverEntry is "${entry}" (yours), so the graceful-shutdown entry is not added.`);
    else {
      p.update.build = { serverEntry: ADAPTER_ENTRY };
      p.shutdown = typeof sd === "object" && sd.timeout !== undefined ? sd.timeout : 25;
    }
  }

  // 8. ASTRO_KEY
  if (opts.astroKey !== false && command === "build" && scan.serverIslands.length > 0 && !env.ASTRO_KEY) {
    p.warn.push("server islands are used but ASTRO_KEY is not set: each build makes its own key, so pages from an older deploy can't load islands from a newer one. The box sets ASTRO_KEY; off the box, run `astro create-key` and set it for every build.");
  }

  // 9. Auth (reserved)
  if (opts.auth) p.warn.push('auth: this option is reserved and does nothing yet. Use authMiddleware({ protect, signIn }) from "@shiptiffin/sdk/astro" in src/middleware.ts.');

  return p;
}

/** This package's runtime modules, next to this file (.ts from source in tests, .js when built). */
function entry(name: string): string {
  const ext = import.meta.url.endsWith(".ts") ? ".ts" : ".js";
  return fileURLToPath(new URL(`./${name}${ext}`, import.meta.url));
}

function resolver(root: URL): (spec: string) => boolean {
  const req = createRequire(new URL("package.json", root));
  return (spec) => {
    try {
      req.resolve(spec);
      return true;
    } catch {
      return false;
    }
  };
}

/** The integration. */
export default function tiffin(options: TiffinAstroOptions = {}): AstroIntegration {
  let serverDir: URL | undefined;
  let shutdown: number | undefined;
  let log: AstroIntegrationLogger | undefined;
  return {
    name: "@shiptiffin/astro",
    hooks: {
      "astro:config:setup": async ({ config, command, updateConfig, injectRoute, logger }) => {
        log = logger;
        const view = config as unknown as ConfigView;
        const resolves = resolver(config.root);
        const p = plan(options, {
          config: view,
          command,
          env: process.env,
          scan: scanProject(fileURLToPath(config.srcDir)),
          resolves,
          entry: entry as PlanInput["entry"],
        });
        const update: Record<string, unknown> = { ...p.update };
        if (p.addNode) {
          const req = createRequire(new URL("package.json", config.root));
          const mod = (await import(pathToFileURL(req.resolve(NODE)).href)) as { default: (o: { mode: "standalone" }) => AstroIntegration };
          const node = mod.default({ mode: "standalone" });
          update.adapter = node;
          update.integrations = [node];
        }
        // Bundle this package's runtime modules into the server build, so only
        // @shiptiffin/sdk has to resolve at runtime.
        update.vite = {
          plugins: [
            {
              name: "@shiptiffin/astro:no-external",
              configEnvironment(name: string) {
                if (name === "ssr" || name === "prerender" || name === "astro") return { resolve: { noExternal: ["@shiptiffin/astro"] } };
                return undefined;
              },
            },
          ],
        };
        updateConfig(update);
        if (p.inject) injectRoute({ pattern: p.inject.pattern, entrypoint: p.inject.entrypoint, prerender: false });
        shutdown = p.shutdown;
        for (const m of p.info) logger.info(m);
        for (const m of p.warn) logger.warn(m);
      },
      "astro:config:done": ({ config }) => {
        serverDir = config.build.server;
      },
      "astro:build:start": () => {
        (globalThis as Record<symbol, unknown>)[BUILDING] = true;
      },
      "astro:build:done": () => {
        delete (globalThis as Record<symbol, unknown>)[BUILDING];
        if (shutdown === undefined || !serverDir) return;
        const dir = fileURLToPath(serverDir);
        const inner = join(dir, ADAPTER_ENTRY);
        const outer = join(dir, "entry.mjs");
        if (!existsSync(inner)) return; // a static build: no server
        if (existsSync(outer) && !readFileSync(outer, "utf8").startsWith("// Written by @shiptiffin/astro")) {
          log?.warn(`shutdown: ${outer} exists and is not ours; left alone.`);
          return;
        }
        writeFileSync(outer, shutdownEntrySource(shutdown));
        log?.info(`shutdown: dist/server/entry.mjs drains in-flight requests on SIGTERM (up to ${shutdown} s).`);
      },
    },
  };
}

export { tiffin };
