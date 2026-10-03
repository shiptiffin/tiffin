// Demo server: a real engine on a throwaway Postgres, seeded with a signed-in
// owner ("Ada"), a team org with members and pending invites, and a link
// invite; serves the gallery page. Pass --signed-out to skip the session.
import index from "./index.html";
import { adminHandler } from "../../auth-engine/src/admin";
import { outbox } from "../../auth-engine/src/mail";
import { Registry } from "../../auth-engine/src/registry";
import { publicHandler } from "../../auth-engine/src/server";
import { Client, projectConfig } from "../../auth-engine/test/helpers";
import { freshDatabase } from "../../auth-engine/test/pg";

const port = Number(process.env.PORT ?? 4317);
const origin = `http://localhost:${port}`;
const db = await freshDatabase("react_demo");
const reg = new Registry(null, {
  version: 1,
  listen: [],
  projects: {
    shop: projectConfig(db, {
      appName: "Shop",
      hosts: ["localhost"],
      primaryUrl: origin,
      origins: [origin],
      methods: ["email", "magic-link", "otp", "passkey", "google", "github"],
      social: { google: { clientId: "x", clientSecret: "y" }, github: { clientId: "x", clientSecret: "y" } },
    }),
  },
});
const engine = publicHandler(reg);
await adminHandler(reg)(new Request("http://admin/projects/shop/migrate", { method: "POST" }));

// Seed through the public API, like real people would.
const call = (c: Client, path: string, body?: unknown) => c.json(path, body === undefined ? {} : { body });
const at = (url: string) => url.replace("https://shop.tiffin.localhost:8443", origin);
async function person(email: string, name: string) {
  const c = new Client(engine, {});
  (c as any).raw = rawAt.bind(c);
  await c.withCaptcha("/sign-up/email", { email, password: "correct horse battery", name });
  const m = [...outbox].reverse().find((x) => x.to === email && x.kind === "verify")!;
  await c.raw(at(m.text.match(/https?:\/\/\S+/)![0]));
  return c;
}
// helpers.Client targets the test origin; point it at this server instead.
const baseRaw = Client.prototype.raw;
async function rawAt(this: Client, p: string, init: any = {}) {
  return baseRaw.call(this, p.startsWith("http") ? p : `${origin}/api/auth${p}`, init);
}
const ada = await person("ada@example.com", "Ada Lovelace");
const team = await call(ada, "/organization/create", { name: "Analytical Engines", slug: "analytical-engines" });
await call(ada, "/organization/set-active", { organizationId: team.body.id });
const charles = await person("charles@example.com", "Charles Babbage");
for (const [email, role] of [["charles@example.com", "admin"], ["mary@example.com", "member"], ["lord.byron@example.com", "viewer"]] as const) {
  const inv = await call(ada, "/organization/invite-member", { email, role, organizationId: team.body.id });
  if (email === "charles@example.com") await call(charles, "/organization/accept-invitation", { invitationId: inv.body.id });
}
const link = await call(ada, "/invite-link/create", { organizationId: team.body.id, role: "member" });
const session = process.argv.includes("--signed-out") ? "" : ada.cookieHeader();

Bun.serve({
  port,
  development: true,
  routes: {
    "/": index,
    "/demo/state": () => Response.json({ link: link.body.token }),
  },
  async fetch(req) {
    const url = new URL(req.url);
    if (!url.pathname.startsWith("/api/auth")) return new Response("not found", { status: 404 });
    // The demo browser is "Ada" unless it has its own cookie.
    const h = new Headers(req.headers);
    if (session && !h.get("cookie")?.includes("session_token")) h.set("cookie", session);
    if (!h.get("origin")) h.set("origin", origin);
    return engine(new Request(req, { headers: h }));
  },
});
console.log(`demo on ${origin}`);
