import { describe, expect, test } from "bun:test";
import { fileURLToPath } from "node:url";
import { isNodeDefaultSession, plan, type ConfigView, type PlanInput, type TiffinAstroOptions } from "../src/index";
import type { ProjectScan } from "../src/scan";

const root = new URL("file:///app/");
const base = (c: Partial<ConfigView> = {}): ConfigView => ({
  root,
  srcDir: new URL("src/", root),
  cacheDir: new URL("node_modules/.astro/", root),
  output: "static",
  trailingSlash: "ignore",
  security: { allowedDomains: [] },
  build: { serverEntry: "entry.mjs" },
  ...c,
});
const noScan: ProjectScan = { onDemandPages: [], serverIslands: [], actions: false, cacheCalls: [] };
const ssrScan: ProjectScan = { ...noScan, onDemandPages: ["/app/src/pages/api/x.ts"] };
const BOX = { TIFFIN_URL: "https://web-shop.box.shiptiffin.app", REDIS_URL: "redis://u:p@127.0.0.1:6379", VALKEY_PREFIX: "p_shop:" };
const nodeAdapter = { name: "@astrojs/node" };

function run(opts: TiffinAstroOptions, o: Partial<PlanInput> & { config?: ConfigView } = {}) {
  return plan(opts, {
    config: o.config ?? base(),
    command: o.command ?? "build",
    env: o.env ?? {},
    scan: o.scan ?? noScan,
    resolves: o.resolves ?? (() => true),
    entry: (n) => `/pkg/dist/${n}.js`,
  });
}

describe("adapter", () => {
  test("adds @astrojs/node when a page renders on demand and no adapter is set", () => {
    const p = run({}, { scan: ssrScan });
    expect(p.addNode).toBe(true);
    expect(p.node).toBe(true);
  });
  test('output: "server", server islands and actions need it too', () => {
    expect(run({}, { config: base({ output: "server" }) }).addNode).toBe(true);
    expect(run({}, { scan: { ...noScan, serverIslands: ["x.astro"] } }).addNode).toBe(true);
    expect(run({}, { scan: { ...noScan, actions: true } }).addNode).toBe(true);
  });
  test("a fully prerendered site gets no adapter, no routes, no server features", () => {
    const p = run({}, { env: BOX });
    expect(p.addNode).toBe(false);
    expect(p.inject).toBeUndefined();
    expect(p.update.session).toBeUndefined();
    expect(p.shutdown).toBeUndefined();
    expect(p.update.site).toBe("https://web-shop.box.shiptiffin.app"); // still useful for sitemaps, canonical URLs
  });
  test("keeps the project's own adapter; warns once for a non-node one", () => {
    const own = run({}, { scan: ssrScan, config: base({ adapter: nodeAdapter }) });
    expect(own.addNode).toBe(false);
    expect(own.node).toBe(true);
    expect(own.warn).toEqual([]);
    const other = run({}, { scan: ssrScan, config: base({ adapter: { name: "@astrojs/vercel" } }) });
    expect(other.addNode).toBe(false);
    expect(other.node).toBe(false);
    expect(other.warn.filter((w) => w.includes("@astrojs/vercel"))).toHaveLength(1);
    expect(other.shutdown).toBeUndefined();
  });
  test("adapter: false and a missing @astrojs/node", () => {
    expect(run({ adapter: false }, { scan: ssrScan }).addNode).toBe(false);
    const p = run({}, { scan: ssrScan, resolves: (s) => s !== "@astrojs/node" });
    expect(p.addNode).toBe(false);
    expect(p.warn.join()).toContain("npm i @astrojs/node");
  });
});

describe("proxy trust and site", () => {
  test("adds the app's hosts, after the project's own, without repeats", () => {
    const config = base({ security: { allowedDomains: [{ hostname: "shop.example", protocol: "https" }] } });
    const p = run({}, { config, env: { ...BOX, TIFFIN_HOSTS: "shop.example,www.shop.example" } });
    expect(p.update.security).toEqual({ allowedDomains: [{ hostname: "www.shop.example", protocol: "https" }] });
  });
  test("falls back to TIFFIN_URL's host", () => {
    expect(run({}, { env: BOX }).update.security).toEqual({ allowedDomains: [{ hostname: "web-shop.box.shiptiffin.app", protocol: "https" }] });
  });
  test("off the box: nothing changes", () => {
    const p = run({}, { scan: ssrScan });
    expect(p.update.security).toBeUndefined();
    expect(p.update.site).toBeUndefined();
    expect(p.info.join("\n")).toContain("not on the box");
  });
  test("the project's site wins; proxy: false / site: false", () => {
    expect(run({}, { env: BOX, config: base({ site: "https://shop.example" }) }).update.site).toBeUndefined();
    const p = run({ proxy: false, site: false }, { env: BOX });
    expect(p.update.security).toBeUndefined();
    expect(p.update.site).toBeUndefined();
  });
});

describe("sessions", () => {
  const nodeDefault = (c: ConfigView): ConfigView => ({
    ...c,
    session: { driver: { entrypoint: "unstorage/drivers/fs-lite", config: { base: fileURLToPath(new URL("sessions", c.cacheDir)) } } },
  });
  test("replaces @astrojs/node's filesystem default with Valkey", () => {
    const config = nodeDefault(base({ adapter: nodeAdapter }));
    expect(isNodeDefaultSession(config)).toBe(true);
    const p = run({}, { config, env: BOX, scan: ssrScan });
    expect(p.update.session).toEqual({ driver: { entrypoint: "/pkg/dist/session-driver.js", config: { namespace: "sess:", ttl: 30 * 24 * 3600 } } });
  });
  test("sets it before @astrojs/node runs when the integration adds the adapter", () => {
    expect(run({}, { env: BOX, scan: ssrScan }).update.session).toBeDefined();
  });
  test("the project's session.ttl and the option's ttl/namespace", () => {
    const config = { ...nodeDefault(base({ adapter: nodeAdapter })), session: { ...nodeDefault(base()).session as object, ttl: 3600 } } as ConfigView;
    expect((run({}, { config, env: BOX }).update.session as any).driver.config.ttl).toBe(3600);
    expect((run({ sessions: { ttl: 60, namespace: "s2:" } }, { config, env: BOX }).update.session as any).driver.config).toEqual({ namespace: "s2:", ttl: 60 });
  });
  test("keeps a driver the project chose, and session: false", () => {
    const own = base({ adapter: nodeAdapter, session: { driver: { entrypoint: "./my-driver.ts" } } });
    expect(run({}, { config: own, env: BOX }).update.session).toBeUndefined();
    expect(run({}, { config: base({ adapter: nodeAdapter, session: false }), env: BOX }).update.session).toBeUndefined();
  });
  test("no REDIS_URL, no SDK, or sessions: false", () => {
    const config = nodeDefault(base({ adapter: nodeAdapter }));
    expect(run({}, { config, env: { TIFFIN_URL: BOX.TIFFIN_URL } }).update.session).toBeUndefined();
    const nosdk = run({}, { config, env: BOX, resolves: (s) => s !== "@shiptiffin/sdk/kv" });
    expect(nosdk.update.session).toBeUndefined();
    expect(nosdk.warn.join()).toContain("npm i @shiptiffin/sdk");
    expect(run({ sessions: false }, { config, env: BOX }).update.session).toBeUndefined();
  });
});

describe("route cache", () => {
  const server = base({ adapter: nodeAdapter });
  test("auto: only when the project caches", () => {
    expect(run({}, { config: server, env: BOX }).update.cache).toBeUndefined();
    expect(run({}, { config: server, env: BOX, scan: { ...noScan, cacheCalls: ["x.ts"] } }).update.cache).toBeDefined();
    const rules = base({ adapter: nodeAdapter, routeRules: { "/blog/[...slug]": { maxAge: 60 } } });
    expect(run({}, { config: rules, env: BOX }).update.cache).toEqual({ provider: { name: "tiffin-valkey", entrypoint: "/pkg/dist/cache-provider.js", config: {} } });
  });
  test("true / options object / false / project provider / no Valkey", () => {
    expect(run({ cache: true }, { config: server, env: BOX }).update.cache).toBeDefined();
    expect((run({ cache: { maxTtl: 60 } }, { config: server, env: BOX }).update.cache as any).provider.config).toEqual({ maxTtl: 60 });
    expect(run({ cache: false }, { config: server, env: BOX, scan: { ...noScan, cacheCalls: ["x"] } }).update.cache).toBeUndefined();
    expect(run({ cache: true }, { config: base({ adapter: nodeAdapter, cache: { provider: { entrypoint: "astro/cache/memory" } } }), env: BOX }).update.cache).toBeUndefined();
    expect(run({ cache: true }, { config: server, env: { TIFFIN_URL: BOX.TIFFIN_URL } }).update.cache).toBeUndefined();
  });
});

describe("health, shutdown, ASTRO_KEY, auth", () => {
  const server = base({ adapter: nodeAdapter });
  test("injects /_health on server builds only", () => {
    expect(run({}, { config: server }).inject).toEqual({ pattern: "/_health", entrypoint: "/pkg/dist/health.js" });
    expect(run({ health: { database: true, path: "/healthz" } }, { config: server }).inject).toEqual({ pattern: "/healthz", entrypoint: "/pkg/dist/health-db.js" });
    expect(run({ health: "/api/health/" }, { config: server }).inject).toBeUndefined();
    expect(run({ health: false }, { config: server }).inject).toBeUndefined();
    const always = run({}, { config: base({ adapter: nodeAdapter, trailingSlash: "always" }) });
    expect(always.info.join()).toContain('healthcheck: "/_health/"');
  });
  test("graceful shutdown renames the adapter entry on node builds", () => {
    const p = run({}, { config: server });
    expect(p.update.build).toEqual({ serverEntry: "astro-entry.mjs" });
    expect(p.shutdown).toBe(25);
    expect(run({ shutdown: { timeout: 10 } }, { config: server }).shutdown).toBe(10);
    expect(run({}, { config: server, command: "dev" }).shutdown).toBeUndefined();
    expect(run({ shutdown: false }, { config: server }).update.build).toBeUndefined();
    expect(run({}, { config: base({ adapter: nodeAdapter, build: { serverEntry: "main.mjs" } }) }).update.build).toBeUndefined();
  });
  test("warns about server islands without ASTRO_KEY", () => {
    const islands = { ...noScan, serverIslands: ["a.astro"] };
    expect(run({}, { config: server, scan: islands }).warn.join()).toContain("ASTRO_KEY");
    expect(run({}, { config: server, scan: islands, env: { ASTRO_KEY: "k" } }).warn.join()).not.toContain("ASTRO_KEY");
    expect(run({ astroKey: false }, { config: server, scan: islands }).warn.join()).not.toContain("ASTRO_KEY");
  });
  test("auth is reserved", () => {
    expect(run({ auth: { protect: ["/app/*"] } }).warn.join()).toContain("@shiptiffin/sdk/astro");
  });
});
