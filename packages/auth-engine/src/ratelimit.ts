// Rate-limit counters, one store per project. Better Auth's "memory" store is
// a single module-wide map keyed by client address and path, so in this one
// engine every project would share it: three sign-in attempts on one app
// would answer 429 on every other app for that address. The rules are Better
// Auth's (it passes the window and max for each path); only the counting
// lives here, kept apart per project and pruned on a timer rather than on
// every request.

type Rule = { window: number; max: number };
type Entry = { count: number; last: number; window: number };

/** Most tracked addresses per project; past it, the oldest are dropped. */
const MAX_KEYS = 50_000;
const PRUNE_EVERY_MS = 30_000;

export type RateLimitStore = {
  consume(key: string, rule: Rule): Promise<{ allowed: boolean; retryAfter: number | null }>;
  size(): number;
};

export function newRateLimitStore(now: () => number = Date.now): RateLimitStore {
  const hits = new Map<string, Entry>();
  let nextPrune = 0;
  const prune = (t: number) => {
    for (const [k, e] of hits) if (t - e.last >= e.window) hits.delete(k);
    let over = hits.size - MAX_KEYS;
    for (const k of hits.keys()) {
      if (over-- <= 0) break;
      hits.delete(k);
    }
  };
  return {
    // The same decision as Better Auth's memory store: the window runs from
    // the last allowed request.
    async consume(key, rule) {
      const t = now();
      if (t >= nextPrune || hits.size > MAX_KEYS) {
        prune(t);
        nextPrune = t + PRUNE_EVERY_MS;
      }
      const window = rule.window * 1000;
      const e = hits.get(key);
      if (!e || t - e.last >= window) {
        hits.delete(key); // re-inserted last: the map stays oldest-first for pruning
        hits.set(key, { count: 1, last: t, window });
        return { allowed: true, retryAfter: null };
      }
      if (e.count >= rule.max) return { allowed: false, retryAfter: Math.ceil((e.last + window - t) / 1000) };
      e.count++;
      e.last = t;
      e.window = window;
      return { allowed: true, retryAfter: null };
    },
    size: () => hits.size,
  };
}

const stores = new Map<string, RateLimitStore>();

/** The project's store; it outlives the project's Better Auth instance, which is rebuilt on config changes. */
export function rateLimitStore(project: string): RateLimitStore {
  let s = stores.get(project);
  if (!s) {
    s = newRateLimitStore();
    stores.set(project, s);
  }
  return s;
}
