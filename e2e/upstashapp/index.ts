// An app written for Upstash: @upstash/redis and @upstash/ratelimit, set up
// from env only (UPSTASH_REDIS_REST_URL/TOKEN), unchanged for the box.
import { Ratelimit } from "@upstash/ratelimit";
import { Redis } from "@upstash/redis";

const redis = Redis.fromEnv();
const ratelimit = new Ratelimit({ redis, limiter: Ratelimit.slidingWindow(3, "60 s"), prefix: "rl" });

Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  async fetch(req) {
    const u = new URL(req.url);
    try {
      if (u.pathname === "/kv") {
        await redis.set("greeting", { hello: "world" });
        const got = await redis.get("greeting");
        const p = redis.pipeline();
        p.set("n", 0);
        p.incr("n");
        p.incrby("n", 5);
        p.get("n");
        const piped = await p.exec();
        const tx = redis.multi();
        tx.set("t", "a b");
        tx.append("t", "!");
        tx.get("t");
        const multi = await tx.exec();
        await redis.hset("h", { a: 1, b: "two" });
        const hash = await redis.hgetall("h");
        return Response.json({ got, piped, multi, hash });
      }
      if (u.pathname === "/limit") {
        const r = await ratelimit.limit(u.searchParams.get("id") ?? "user-1");
        return Response.json({ success: r.success, remaining: r.remaining });
      }
      if (u.pathname === "/env") {
        const e = process.env;
        return Response.json({ url: e.UPSTASH_REDIS_REST_URL, kv: e.KV_REST_API_URL === e.UPSTASH_REDIS_REST_URL && !!e.KV_REST_API_READ_ONLY_TOKEN });
      }
      return new Response("ok\n");
    } catch (err) {
      return Response.json({ error: String(err) }, { status: 500 });
    }
  },
});
