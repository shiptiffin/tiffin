// Records how its cron path is called, the way a Vercel app checks it.
const seen: { method: string; auth: string | null; ua: string | null }[] = [];

Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  fetch(req) {
    const { pathname } = new URL(req.url);
    if (pathname === "/api/cron/ping") {
      seen.push({ method: req.method, auth: req.headers.get("authorization"), ua: req.headers.get("user-agent") });
      const ok = req.headers.get("authorization") === `Bearer ${process.env.CRON_SECRET}`;
      return new Response(ok ? "ok" : "unauthorized", { status: ok ? 200 : 401 });
    }
    if (pathname === "/seen") return Response.json(seen);
    return new Response("vx-api " + pathname);
  },
});
