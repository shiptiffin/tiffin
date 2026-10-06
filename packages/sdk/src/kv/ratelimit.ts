// The rate limiter's Lua script and arithmetic. One key per limited name,
// passed in KEYS (so the project's ACL prefix holds), and the server's clock
// (TIME), so app instances with drifting clocks agree. A refused call does not
// count against the limit.
import { createHash } from "node:crypto";

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

export const RATE_LIMIT_LUA = `local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local limit, w, cost = tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3])
if ARGV[4] == 'fixed' then
  local c = tonumber(redis.call('GET', KEYS[1]) or '0')
  if c + cost > limit then return {0, c, redis.call('PTTL', KEYS[1])} end
  c = redis.call('INCRBY', KEYS[1], cost)
  local ttl = redis.call('PTTL', KEYS[1])
  if ttl < 0 then redis.call('PEXPIRE', KEYS[1], w) ttl = w end
  return {1, c, ttl}
end
local idx = math.floor(now / w)
local h = redis.call('HMGET', KEYS[1], 'w', 'c', 'p')
local c, p = tonumber(h[2]) or 0, tonumber(h[3]) or 0
local hw = tonumber(h[1])
if hw ~= idx then
  if hw == idx - 1 then p = c else p = 0 end
  c = 0
end
local into = now - idx * w
if p * (w - into) / w + c + cost > limit then return {0, c, p, into} end
c = c + cost
redis.call('HSET', KEYS[1], 'w', idx, 'c', c, 'p', p)
redis.call('PEXPIRE', KEYS[1], w * 2)
return {1, c, p, into}`;

export const RATE_LIMIT_SHA = createHash("sha1").update(RATE_LIMIT_LUA).digest("hex");

const UNIT: Record<string, number> = { ms: 1, s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000 };

/** A window in milliseconds. */
export function windowMs(w: Window): number {
  if (typeof w === "number") return Math.max(1, Math.round(w * 1000));
  const m = /^\s*(\d+(?:\.\d+)?)\s*(ms|s|m|h|d)\s*$/.exec(w);
  if (!m) throw new TypeError(`rateLimit: window "${w}" is not like "10 s", "1 m" or "24 h"`);
  return Math.max(1, Math.round(Number(m[1]) * UNIT[m[2]!]!));
}

/** The result from the script's reply, with times relative to now. */
export function rateLimitResult(r: unknown[], limit: number, w: number, cost: number, fixed: boolean): RateLimitResult {
  const n = r.map(Number);
  const allowed = n[0] === 1;
  const now = Date.now();
  if (fixed) {
    const [, c = 0, pttl = w] = n;
    const left = pttl < 0 ? w : pttl;
    return { allowed, limit, remaining: Math.max(0, limit - c), reset: now + left, retryAfter: allowed ? 0 : Math.max(1, Math.ceil(left / 1000)) };
  }
  const [, c = 0, p = 0, into = 0] = n;
  const x = into / w; // how far into the current window
  const used = p * (1 - x) + c;
  const endOfWindow = (1 - x) * w;
  if (allowed) return { allowed, limit, remaining: Math.max(0, Math.floor(limit - used)), reset: now + endOfWindow, retryAfter: 0 };
  // When does the call fit? While the current count alone fits, once enough
  // of the previous window has slid out; otherwise in the next window, as
  // this window's count slides out in turn.
  const room = limit - cost;
  let wait: number;
  if (room < 0) wait = 2 * w;
  else if (c <= room && p > 0) wait = Math.max(0, (1 - (room - c) / p - x) * w);
  else wait = endOfWindow + (c > 0 ? Math.max(0, 1 - room / c) * w : 0);
  return { allowed, limit, remaining: Math.max(0, Math.floor(limit - used)), reset: now + Math.ceil(wait), retryAfter: Math.max(1, Math.ceil(wait / 1000)) };
}
