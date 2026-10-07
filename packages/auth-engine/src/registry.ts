// Every auth-enabled project's instance, built on first use and rebuilt when
// its config changes. Idle projects hold no database connections (the pools
// close idle connections after 30 s), so one engine serves many projects in
// little memory.
import type pg from "pg";
import { createInstance, createPool, type Instance } from "./auth";
import { fingerprint, readConfig, type EngineConfig, type ProjectConfig, type ProxyConfig } from "./config";
import { createProxyInstance, type ProxyInstance } from "./social";

export class Registry {
  private config: EngineConfig = { version: 1, listen: [], projects: {} };
  /** Called after every config change (main.ts rebinds extra listeners). */
  onChange: ((c: EngineConfig) => void) | null = null;
  private mtimeMs = 0;
  private instances = new Map<string, { fp: string; inst: Instance }>();
  private pools = new Map<string, { url: string; pool: pg.Pool }>();
  private retired = new Set<pg.Pool>();
  private byHost = new Map<string, string>();
  /** Each project's config fingerprint, computed once per config change rather than per request. */
  private fps = new Map<string, string>();
  private proxyFp = "";
  private lastStat = 0;
  private proxyInst: { fp: string; inst: ProxyInstance } | null = null;

  constructor(private readonly path: string | null, initial?: EngineConfig) {
    if (initial) this.set(initial);
    else this.reload();
  }

  /** Re-reads the config file. Instances whose config changed are rebuilt lazily. */
  reload(): string[] {
    if (!this.path) return Object.keys(this.config.projects);
    const { config, mtimeMs } = readConfig(this.path);
    this.mtimeMs = mtimeMs;
    this.set(config);
    return Object.keys(config.projects);
  }

  /** Replaces the config (tests, and reload). */
  set(config: EngineConfig) {
    this.config = config;
    this.onChange?.(config);
    this.byHost.clear();
    this.fps.clear();
    for (const [name, p] of Object.entries(config.projects)) {
      this.fps.set(name, fingerprint(p));
      for (const h of p.hosts) this.byHost.set(h.toLowerCase(), name);
    }
    this.proxyFp = config.proxy ? new Bun.CryptoHasher("sha256").update(JSON.stringify(config.proxy)).digest("hex") : "";
    for (const [name, e] of this.instances) {
      if (this.fps.get(name) !== e.fp) this.instances.delete(name);
    }
    for (const [name, e] of this.pools) {
      const p = config.projects[name];
      if (!p || p.databaseUrl !== e.url) {
        this.pools.delete(name);
        this.retire(e.pool);
      }
    }
  }

  /** Picks up config edits even if nobody called reload (checked at most once a second). */
  maybeReload() {
    if (!this.path) return;
    const now = Date.now();
    if (now - this.lastStat < 1000) return;
    this.lastStat = now;
    try {
      const m = Bun.file(this.path).lastModified;
      if (m && m !== this.mtimeMs) this.reload();
    } catch (err) {
      console.error(JSON.stringify({ level: "error", msg: "auth config reload failed", err: String(err) }));
    }
  }

  listen(): string[] {
    return this.config.listen;
  }

  projects(): string[] {
    return Object.keys(this.config.projects).sort();
  }

  projectConfig(project: string): ProjectConfig | undefined {
    return this.config.projects[project];
  }

  /** The host box-wide sign-ins call back to (the dashboard's), if the box has box-wide keys. */
  proxyHost(): string | undefined {
    return this.config.proxy?.host.toLowerCase();
  }

  proxyConfig(): ProxyConfig | undefined {
    return this.config.proxy;
  }

  /** The callback endpoint for box-wide keys, rebuilt when they change. */
  proxy(): ProxyInstance | undefined {
    const p = this.config.proxy;
    if (!p) {
      this.proxyInst = null;
      return undefined;
    }
    const fp = this.proxyFp;
    if (this.proxyInst?.fp === fp) return this.proxyInst.inst;
    const inst = createProxyInstance(p);
    this.proxyInst = { fp, inst };
    return inst;
  }

  projectForHost(host: string | null | undefined): string | undefined {
    if (!host) return undefined;
    return this.byHost.get(host.split(":")[0]!.toLowerCase());
  }

  /** The project's database pool (shared by its Better Auth instance and the admin API). */
  pool(project: string): pg.Pool | undefined {
    const p = this.config.projects[project];
    if (!p) return undefined;
    const e = this.pools.get(project);
    if (e && e.url === p.databaseUrl) return e.pool;
    const pool = createPool(project, p.databaseUrl);
    this.pools.set(project, { url: p.databaseUrl, pool });
    return pool;
  }

  // Let requests in flight finish before closing an old pool.
  private retire(pool: pg.Pool) {
    this.retired.add(pool);
    setTimeout(() => {
      this.retired.delete(pool);
      void pool.end().catch(() => {});
    }, 30_000).unref?.();
  }

  get(project: string): Instance | undefined {
    const p = this.config.projects[project];
    if (!p) return undefined;
    const fp = this.fps.get(project)!;
    const e = this.instances.get(project);
    if (e && e.fp === fp) return e.inst;
    const inst = createInstance(project, p, this.pool(project)!);
    this.instances.set(project, { fp, inst });
    return inst;
  }

  /** Drops a project's instance (after its schema changed); the next request builds a fresh one. */
  invalidate(project: string) {
    this.instances.delete(project);
  }

  async closeAll() {
    this.instances.clear();
    const all = [...[...this.pools.values()].map((e) => e.pool), ...this.retired];
    this.pools.clear();
    this.retired.clear();
    await Promise.all(all.map((p) => p.end().catch(() => {})));
  }
}
