// The box's mail server refusing a recipient for good (5xx, e.g. a
// suppressed address) must not fail the request that sent the mail.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { createServer, type Server } from "node:net";
import { adminHandler } from "../src/admin";
import { send } from "../src/mail";
import { Registry } from "../src/registry";
import { publicHandler } from "../src/server";
import { Client, projectConfig } from "./helpers";
import { freshDatabase, stopCluster } from "./pg";

/** A tiny SMTP server: refuses RCPT for addresses starting with "blocked" (550) or "busy" (451). */
function smtp(): Promise<{ server: Server; url: string }> {
  return new Promise((resolve) => {
    const server = createServer((sock) => {
      sock.write("220 test ESMTP\r\n");
      let buf = "";
      let data = false;
      sock.on("data", (chunk) => {
        buf += chunk.toString();
        let i: number;
        while ((i = buf.indexOf("\r\n")) >= 0) {
          const line = buf.slice(0, i);
          buf = buf.slice(i + 2);
          if (data) {
            if (line === ".") {
              data = false;
              sock.write("250 queued\r\n");
            }
            continue;
          }
          const cmd = line.slice(0, 4).toUpperCase();
          if (cmd === "EHLO" || cmd === "HELO") sock.write("250-test\r\n250 OK\r\n");
          else if (cmd === "MAIL") sock.write("250 OK\r\n");
          else if (cmd === "RCPT") {
            if (/<blocked/i.test(line)) sock.write("550 5.7.1 Recipient is on the suppression list\r\n");
            else if (/<busy/i.test(line)) sock.write("451 4.3.0 Try again later\r\n");
            else sock.write("250 OK\r\n");
          } else if (cmd === "DATA") {
            data = true;
            sock.write("354 go\r\n");
          } else if (cmd === "QUIT") {
            sock.end("221 bye\r\n");
          } else sock.write("250 OK\r\n");
        }
      });
    });
    server.listen(0, "127.0.0.1", () => {
      const a = server.address() as { port: number };
      resolve({ server, url: `smtp://127.0.0.1:${a.port}?ignoreTLS=true` });
    });
  });
}

let mail: { server: Server; url: string };
let reg: Registry;
let handle: (r: Request) => Promise<Response>;

beforeAll(async () => {
  mail = await smtp();
  const db = await freshDatabase("mail_test");
  reg = new Registry(null, { version: 1, listen: [], projects: { shop: projectConfig(db, { smtpUrl: mail.url, captcha: false, methods: ["email", "magic-link", "otp"] }) } });
  handle = publicHandler(reg);
  expect((await adminHandler(reg)(new Request("http://admin/projects/shop/migrate", { method: "POST" }))).status).toBe(200);
}, 60_000);

afterAll(async () => {
  mail.server.close();
  await reg.closeAll();
  await stopCluster();
});

const MAIL = { subject: "s", text: "t", html: "<p>t</p>", kind: "magic-link" };

describe("a refused recipient", () => {
  test("send: a permanent refusal is logged, not thrown; a temporary one still fails", async () => {
    await send("shop", mail.url, "shop@example.com", { ...MAIL, to: "ok@example.com" });
    await send("shop", mail.url, "shop@example.com", { ...MAIL, to: "blocked@example.com" });
    await expect(send("shop", mail.url, "shop@example.com", { ...MAIL, to: "busy@example.com" })).rejects.toBeDefined();
  });

  test("magic link, OTP and reset answer the same for a refused address as for any other", async () => {
    for (const to of ["blocked@example.com", "fine@example.com"]) {
      const c = new Client(handle);
      const m = await c.json("/sign-in/magic-link", { body: { email: to, callbackURL: "/" } });
      expect(m.status).toBe(200);
      const o = await c.json("/email-otp/send-verification-otp", { body: { email: to, type: "sign-in" } });
      expect(o.status).toBe(200);
      const r = await c.json("/request-password-reset", { body: { email: to, redirectTo: "/reset" } });
      expect(r.status).toBe(200);
    }
  });
});

describe("no mail service in production", () => {
  test("email sign-up, links, codes and resets are refused on production hosts; previews and passkeys work", async () => {
    const db = await freshDatabase("mail_blocked");
    const preview = "pr-1--shop.tiffin.localhost";
    const r2 = new Registry(null, {
      version: 1,
      listen: [],
      projects: {
        shop: projectConfig(db, {
          captcha: false,
          methods: ["email", "magic-link", "otp", "passkey"],
          hosts: ["shop.tiffin.localhost", preview],
          origins: ["https://shop.tiffin.localhost:8443", `https://${preview}:8443`],
          emailBlocked: true,
          previewHosts: [preview],
          requireEmailVerification: false,
        }),
      },
    });
    const h = publicHandler(r2);
    expect((await adminHandler(r2)(new Request("http://admin/projects/shop/migrate", { method: "POST" }))).status).toBe(200);
    const c = new Client(h);
    for (const [path, body] of [
      ["/sign-up/email", { email: "a@example.com", password: "correct horse battery", name: "A" }],
      ["/sign-in/magic-link", { email: "a@example.com", callbackURL: "/" }],
      ["/email-otp/send-verification-otp", { email: "a@example.com", type: "sign-in" }],
      ["/request-password-reset", { email: "a@example.com", redirectTo: "/r" }],
    ] as const) {
      const r = await c.json(path, { body });
      expect(r.status).toBe(503);
      expect(r.body.code).toBe("EMAIL_NOT_SET_UP");
      expect(r.body.message).toBe("This app can't send email yet: connect a mail service in Settings › Email.");
    }
    expect((await c.json("/tiffin/config")).body.emailReady).toBe(false);
    expect((await c.json("/passkey/generate-authenticate-options")).status).toBe(200);
    // A preview keeps the dev inbox.
    const p = new Client(h);
    const at = (path: string) => `https://${preview}:8443/api/auth${path}`;
    const up = await p.json(at("/sign-up/email"), { body: { email: "b@example.com", password: "correct horse battery", name: "B" } });
    expect(up.status).toBe(200);
    expect((await p.json(at("/tiffin/config"))).body.emailReady).toBe(true);
    await r2.closeAll();
  }, 60_000);
});
