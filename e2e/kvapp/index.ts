// An app on @shiptiffin/sdk/kv (vendored by `tiffin sdk add`), on both of its
// connections: Bun's RedisClient and the SDK's own RESP client (what Node uses).
import { KVError, kv } from "@shiptiffin/sdk/kv";

Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  async fetch(req) {
    const u = new URL(req.url);
    if (u.pathname !== "/sdk") return new Response("ok\n");
    const driver = u.searchParams.get("driver") === "resp" ? "resp" : "bun";
    const s = kv({ driver });
    const k = `${driver}:`;
    try {
      await s.set(k + "greeting", { hello: "world" }, { ex: 600 });
      const got = await s.get(k + "greeting");
      const visits = await s.incr(k + "visits");
      await s.hset(k + "h", { a: 1, b: "two" });
      const hash = await s.hgetall(k + "h");
      await s.zadd(k + "z", { score: 2, member: "b" }, { score: 1, member: "a" });
      const top = await s.zrange(k + "z", 0, -1, { rev: true, withScores: true });
      const p = s.pipeline();
      p.set(k + "n", 0);
      p.incrby(k + "n", 5);
      p.get(k + "n");
      const piped = await p.exec();
      const keys = (await s.keys(k + "*")).sort();
      const burst = await Promise.all(Array.from({ length: 200 }, () => s.rateLimit(k + "burst", { limit: 50, window: "1 m" })));
      const allowed = burst.filter((r) => r.allowed).length;
      const other = kv({ driver, prefix: "p_upstash:" });
      const outside = await other.get("greeting").then(
        () => "read",
        (e) => (e instanceof KVError ? e.code : String(e)),
      );
      other.close();
      return Response.json({ got, visits, hash, top, piped, keys, allowed, outside });
    } catch (err) {
      return Response.json({ error: String(err) }, { status: 500 });
    } finally {
      s.close();
    }
  },
});
