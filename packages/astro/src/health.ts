/**
 * `@shiptiffin/astro/health`: the route the integration injects (default
 * `/_health`). Answers 200 JSON as soon as the server renders on demand, so
 * the box's health check exercises the Astro server, not a file it serves.
 */
export const prerender = false;

const started = Date.now();

export function healthBody(extra: Record<string, unknown> = {}): Record<string, unknown> {
  const env = typeof process === "undefined" ? {} : process.env;
  return {
    ok: true,
    app: env.TIFFIN_APP ?? null,
    deploy: env.TIFFIN_DEPLOY ?? null,
    instance: env.TIFFIN_INSTANCE ?? null,
    uptime: Math.round((Date.now() - started) / 1000),
    ...extra,
  };
}

export const headers = { "content-type": "application/json", "cache-control": "no-store" };

export function GET(): Response {
  return new Response(JSON.stringify(healthBody()), { status: 200, headers });
}

export const HEAD = GET;
