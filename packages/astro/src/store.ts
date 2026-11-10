/**
 * The box's Valkey for the runtime modules (session driver, cache provider):
 * `@shiptiffin/sdk/kv` with REDIS_URL and VALKEY_PREFIX, raw strings (no
 * JSON), one connection per process shared with the app's own `kv()` calls.
 */
import { kv } from "@shiptiffin/sdk/kv";

/** The subset of the SDK's KV store these modules use (its raw() view). */
export interface Store {
  /** The full key: VALKEY_PREFIX + name. */
  key(name: string): string;
  get(key: string): Promise<unknown>;
  mget(...keys: string[]): Promise<unknown[]>;
  set(key: string, value: string, opts?: { ex?: number; px?: number; nx?: boolean }): Promise<boolean>;
  del(...keys: string[]): Promise<number>;
  /** A command as is: no prefix added. */
  command<T = unknown>(cmd: string, ...args: (string | number)[]): Promise<T>;
}

let shared: Store | null | undefined;

/** The project's store, or undefined when REDIS_URL is not set. */
export function boxStore(): Store | undefined {
  if (shared === undefined) {
    const env = typeof process === "undefined" ? {} : process.env;
    shared = env.REDIS_URL ? (kv().raw() as unknown as Store) : null;
  }
  return shared ?? undefined;
}

/** The app/environment scope for keys that must not cross previews: "<app>:<env>:". */
export function appScope(env: Record<string, string | undefined> = typeof process === "undefined" ? {} : process.env): string {
  const app = (env.TIFFIN_APP || "app").replace(/[^\w.-]/g, "_");
  const where = env.TIFFIN_PREVIEW ? `pr-${env.TIFFIN_PREVIEW.replace(/[^\w.-]/g, "_")}` : "prod";
  return `${app}:${where}:`;
}
