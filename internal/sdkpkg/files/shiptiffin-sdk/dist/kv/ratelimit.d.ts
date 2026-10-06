/** "10 s", "1m", "15 m", "24 h", "1 d", "500 ms", or a number of seconds. */
export type Window = number | `${number}${"ms" | "s" | "m" | "h" | "d"}` | `${number} ${"ms" | "s" | "m" | "h" | "d"}`;
export interface RateLimitOptions {
    /** Calls allowed per window. */
    limit: number;
    /** The window: seconds, or a string like "10 s", "1 m", "24 h". */
    window: Window;
    /**
     * "sliding" (default): the previous window's count weighs in by how much of
     * it still overlaps, so a burst at a window's edge can't double the limit.
     * "fixed": a plain counter that resets one window after its first call.
     */
    algorithm?: "sliding" | "fixed";
    /** How much this call uses (default 1). */
    cost?: number;
}
export interface RateLimitResult {
    /** Whether this call may go ahead (Upstash's `success`). */
    allowed: boolean;
    limit: number;
    /** Calls left in the window after this one. */
    remaining: number;
    /** Unix time in ms: when a refused call may retry, or when the current window ends. */
    reset: number;
    /** Seconds to wait before retrying: 0 when allowed (for a Retry-After header). */
    retryAfter: number;
}
export declare const RATE_LIMIT_LUA = "local t = redis.call('TIME')\nlocal now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)\nlocal limit, w, cost = tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3])\nif ARGV[4] == 'fixed' then\n  local c = tonumber(redis.call('GET', KEYS[1]) or '0')\n  if c + cost > limit then return {0, c, redis.call('PTTL', KEYS[1])} end\n  c = redis.call('INCRBY', KEYS[1], cost)\n  local ttl = redis.call('PTTL', KEYS[1])\n  if ttl < 0 then redis.call('PEXPIRE', KEYS[1], w) ttl = w end\n  return {1, c, ttl}\nend\nlocal idx = math.floor(now / w)\nlocal h = redis.call('HMGET', KEYS[1], 'w', 'c', 'p')\nlocal c, p = tonumber(h[2]) or 0, tonumber(h[3]) or 0\nlocal hw = tonumber(h[1])\nif hw ~= idx then\n  if hw == idx - 1 then p = c else p = 0 end\n  c = 0\nend\nlocal into = now - idx * w\nif p * (w - into) / w + c + cost > limit then return {0, c, p, into} end\nc = c + cost\nredis.call('HSET', KEYS[1], 'w', idx, 'c', c, 'p', p)\nredis.call('PEXPIRE', KEYS[1], w * 2)\nreturn {1, c, p, into}";
export declare const RATE_LIMIT_SHA: string;
/** A window in milliseconds. */
export declare function windowMs(w: Window): number;
/** The result from the script's reply, with times relative to now. */
export declare function rateLimitResult(r: unknown[], limit: number, w: number, cost: number, fixed: boolean): RateLimitResult;
