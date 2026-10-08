// Small helpers for the control plane's route handlers.
import { rateLimiter } from "../early-access-box";

export const json = (body: unknown, status = 200) =>
  Response.json(body, { status, headers: { "Cache-Control": "no-store" } });

export const problem = (status: number, message: string, extra?: Record<string, unknown>) => json({ ok: false, message, ...extra }, status);

/** The client's address as the box's edge saw it. */
export function clientIp(req: Request): string {
  return req.headers.get("x-forwarded-for")?.split(",")[0]?.trim() || req.headers.get("x-real-ip") || "unknown";
}

/** Same-origin check for browser POSTs (cookies ride along on cross-site forms). */
export function sameOrigin(req: Request): boolean {
  const origin = req.headers.get("origin");
  if (!origin) return req.headers.get("sec-fetch-site") !== "cross-site";
  try {
    return new URL(origin).host === (req.headers.get("x-forwarded-host") ?? req.headers.get("host") ?? new URL(req.url).host);
  } catch {
    return false;
  }
}

export const checkLimit = rateLimiter(20, 10 * 60_000); // Hetzner key checks per account
export const actionLimit = rateLimiter(60, 10 * 60_000);
export const abuseLimit = rateLimiter(5, 60 * 60_000);

export async function readJson<T>(req: Request, max = 16_384): Promise<T | null> {
  const text = await req.text();
  if (text.length > max) return null;
  try {
    return JSON.parse(text) as T;
  } catch {
    return null;
  }
}
