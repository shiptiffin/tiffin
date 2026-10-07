// Box-wide environment variables never reach a project's auth: Better Auth
// would take BETTER_AUTH_SECRETS over the `secret` option (and fill other
// gaps from the environment), keying every project on the box alike.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { createHmac } from "node:crypto";
import { betterAuth } from "better-auth";
import { adminHandler } from "../src/admin";
import { IGNORED_ENV } from "../src/auth";
import { Registry } from "../src/registry";
import { publicHandler } from "../src/server";
import { Client, projectConfig } from "./helpers";
import { freshDatabase, stopCluster } from "./pg";

const SECRET = "project-secret-0123456789abcdef0123456789abcdef";
const BOX_SECRET = "box-wide-secret-fedcba9876543210fedcba9876543210";
const EVIL = "https://evil.example";

function setBoxEnv() {
  process.env.BETTER_AUTH_SECRETS = `1:${BOX_SECRET}`;
  process.env.BETTER_AUTH_SECRET = BOX_SECRET;
  process.env.AUTH_SECRET = BOX_SECRET;
  process.env.BETTER_AUTH_TRUSTED_ORIGINS = EVIL;
  process.env.BETTER_AUTH_URL = EVIL;
}

let url: string;
const regs: Registry[] = [];
const registry = () => {
  const r = new Registry(null, {
    version: 1,
    listen: [],
    projects: { shop: projectConfig(url, { secret: SECRET, methods: ["email"], captcha: false, requireEmailVerification: false }) },
  });
  regs.push(r);
  return r;
};

beforeAll(async () => {
  url = await freshDatabase("env_test");
  const r = await adminHandler(registry())(new Request("http://admin/projects/shop/migrate", { method: "POST" }));
  expect(r.status).toBe(200);
}, 90_000);

afterAll(async () => {
  for (const name of IGNORED_ENV) delete process.env[name];
  for (const r of regs) await r.closeAll();
  await stopCluster();
});

describe("box environment", () => {
  test("left alone, Better Auth would key a project with BETTER_AUTH_SECRETS (and trust its origins)", async () => {
    setBoxEnv();
    const plain = betterAuth({ secret: SECRET, telemetry: { enabled: false }, logger: { disabled: true } });
    const ctx = await plain.$context;
    expect(ctx.secret).toBe(BOX_SECRET);
    expect(ctx.trustedOrigins).toContain(EVIL);
    for (const name of IGNORED_ENV) delete process.env[name];
  });

  test("the engine ignores it: each project keeps its own secret and origins", async () => {
    setBoxEnv();
    const reg = registry(); // instances built with the variables set
    const handle = publicHandler(reg);
    const inst = reg.get("shop")!;
    for (const name of IGNORED_ENV) expect(process.env[name]).toBeUndefined();
    const ctx = await inst.auth.$context;
    expect(ctx.secret).toBe(SECRET);
    expect(ctx.secretConfig).toBe(SECRET);
    expect(ctx.trustedOrigins).not.toContain(EVIL);

    // A session made now is signed with the project's secret.
    const c = new Client(handle);
    const up = await c.json("/sign-up/email", { body: { email: "ada@example.com", password: "correct horse battery", name: "Ada" } });
    expect(up.status).toBe(200);
    const [, cookie] = [...c.cookies].find(([k]) => k.endsWith("session_token"))!;
    const [token, sig] = decodeURIComponent(cookie).split(".");
    expect(sig).toBe(createHmac("sha256", SECRET).update(token!).digest("base64"));
    expect((await c.json("/tiffin/session")).body.user.email).toBe("ada@example.com");

  });
});
