// Sign-ups and deletions reach the box's analytics collector, and never fail
// the request when it can't be reached.
import { afterAll, expect, test } from "bun:test";
import pg from "pg";
import { clientIP, firstVisitCookie, reportDeleted, reportSignup } from "../src/analytics";
import { buildOptions } from "../src/auth";
import { requestFacts } from "../src/context";
import { projectConfig } from "./helpers";

const got: { path: string; auth: string | null; body: Record<string, unknown> }[] = [];
const collector = Bun.serve({
  port: 0,
  async fetch(req) {
    got.push({ path: new URL(req.url).pathname, auth: req.headers.get("authorization"), body: (await req.json()) as Record<string, unknown> });
    return Response.json({ ok: true }, { status: 202 });
  },
});
afterAll(() => collector.stop(true));
const sink = { url: `http://127.0.0.1:${collector.port}/signup`, key: "k1" };

test("helpers read the forwarded address and the first-visit cookie", () => {
  const req = new Request("https://shop.box.test/api/auth/sign-up/email", {
    headers: { "x-forwarded-for": "10.0.0.9, 203.0.113.7", cookie: "a=1; tiffin_first_visit=AbCdEfGhIjKlMnOpQrStUv; b=2" },
  });
  expect(clientIP(req)).toBe("203.0.113.7");
  expect(firstVisitCookie(req)).toBe("AbCdEfGhIjKlMnOpQrStUv");
  expect(firstVisitCookie(new Request("https://x.test/"))).toBe("");
});

test("a sign-up is reported with the visitor's request facts", async () => {
  got.length = 0;
  const req = new Request("https://shop.box.test/api/auth/sign-up/email", {
    headers: { "x-forwarded-for": "203.0.113.7", "user-agent": "Mozilla/5.0 test", "sec-gpc": "1" },
  });
  await reportSignup(sink, req, "shop.box.test", "user_1");
  expect(got).toHaveLength(1);
  expect(got[0]!.path).toBe("/signup");
  expect(got[0]!.auth).toBe("Bearer k1");
  expect(got[0]!.body).toMatchObject({ userId: "user_1", host: "shop.box.test", ip: "203.0.113.7", ua: "Mozilla/5.0 test", gpc: true, firstVisit: "" });
});

test("no sink, or an unreachable collector, does nothing and doesn't throw", async () => {
  await reportSignup(undefined, new Request("https://x.test/"), "x.test", "u");
  await reportSignup({ url: "http://127.0.0.1:9/signup", key: "k" }, new Request("https://x.test/"), "x.test", "u");
});

test("the create hook marks the request's new account; the delete hook reports it", async () => {
  got.length = 0;
  const pool = new pg.Pool({ connectionString: "postgres://unused/x" });
  const hooks = buildOptions("shop", projectConfig("postgres://unused/x", { analytics: sink }), pool).databaseHooks!.user! as unknown as {
    create: { after: (u: { id: string }) => Promise<void> };
    delete: { after: (u: { id: string }) => Promise<void> };
  };
  const facts: { newUserId?: string } = {};
  await requestFacts.run(facts, () => hooks.create.after({ id: "user_9" }));
  expect(facts.newUserId).toBe("user_9");
  await hooks.delete.after({ id: "user_9" });
  for (let i = 0; i < 50 && got.length === 0; i++) await Bun.sleep(10);
  expect(got[0]).toMatchObject({ path: "/signup/delete", body: { userId: "user_9" } });
  await pool.end();
});
