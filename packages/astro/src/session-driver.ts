/**
 * `@shiptiffin/astro/session-driver`: Astro sessions in the box's Valkey.
 *
 * Astro loads this module as `session.driver.entrypoint` and calls its default
 * export with `session.driver.config` (serialisable, baked into the build). The
 * connection is read at runtime (REDIS_URL, VALKEY_PREFIX), never from config,
 * so no credential is written into the build.
 *
 *   keys:  <VALKEY_PREFIX><namespace><session id>     (namespace "sess:" by default)
 *   value: Astro's serialised session (a devalue string), stored as is
 *   TTL:   `ttl` seconds, renewed on every read and write (sliding)
 *
 * Without REDIS_URL at runtime the driver keeps sessions in this process's
 * memory and logs that once: sessions then work, but are lost on restart and
 * not shared between instances.
 */
import { DEFAULT_SESSION_TTL } from "./defaults";
import { boxStore, type Store } from "./store";

export { DEFAULT_SESSION_TTL };

/** What Astro's session runtime calls (astro's SessionDriver). */
export interface SessionDriver {
  getItem(key: string): Promise<unknown>;
  setItem(key: string, value: unknown): Promise<void>;
  removeItem(key: string): Promise<void>;
}

export interface SessionDriverConfig {
  /** Key namespace after VALKEY_PREFIX (default "sess:"). */
  namespace?: string;
  /** Seconds a session lives after its last read or write (default 30 days). 0: no expiry. */
  ttl?: number;
}

/** A session driver over the given store (the SDK's raw KV). */
export function createSessionDriver(store: Store, config: SessionDriverConfig = {}): SessionDriver {
  const ns = config.namespace ?? "sess:";
  const ttl = Math.max(0, Math.floor(config.ttl ?? DEFAULT_SESSION_TTL));
  const name = (id: string) => `${ns}${id}`;
  let getex = true; // GETEX (Valkey/Redis 6.2+); falls back to GET + EXPIRE
  return {
    async getItem(id) {
      if (ttl > 0 && getex) {
        try {
          return (await store.command<string | null>("GETEX", store.key(name(id)), "EX", ttl)) ?? null;
        } catch (e) {
          if (!/unknown command|ERR unknown/i.test(String((e as Error)?.message))) throw e;
          getex = false;
        }
      }
      const v = await store.get(name(id));
      if (v != null && ttl > 0) await store.command("EXPIRE", store.key(name(id)), ttl);
      return v ?? null;
    },
    async setItem(id, value) {
      const s = typeof value === "string" ? value : JSON.stringify(value);
      await store.set(name(id), s, ttl > 0 ? { ex: ttl } : {});
    },
    async removeItem(id) {
      await store.del(name(id));
    },
  };
}

/** Sessions in this process only: the fallback without REDIS_URL. */
export function memorySessionDriver(config: SessionDriverConfig = {}): SessionDriver {
  const ttl = Math.max(0, Math.floor(config.ttl ?? DEFAULT_SESSION_TTL)) * 1000;
  const m = new Map<string, { v: unknown; exp: number }>();
  return {
    async getItem(id) {
      const e = m.get(id);
      if (!e) return null;
      if (e.exp && e.exp < Date.now()) {
        m.delete(id);
        return null;
      }
      if (ttl) e.exp = Date.now() + ttl;
      return e.v;
    },
    async setItem(id, v) {
      m.set(id, { v, exp: ttl ? Date.now() + ttl : 0 });
    },
    async removeItem(id) {
      m.delete(id);
    },
  };
}

/** Astro's driver factory: the box's Valkey, or memory (with a log line) without REDIS_URL. */
export default function tiffinSessionDriver(config?: SessionDriverConfig): SessionDriver {
  const store = boxStore();
  if (store) return createSessionDriver(store, config);
  console.warn("[@shiptiffin/astro] REDIS_URL is not set: sessions are kept in this process's memory (lost on restart, not shared between instances).");
  return memorySessionDriver(config);
}
