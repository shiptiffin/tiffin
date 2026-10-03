import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SMTPServer } from "smtp-server";
import { createElement } from "react";
import { render, send } from "../src/email";

// A stand-in for the box's SMTP server: AUTH PLAIN shop/pw, records mail.
const got: { from: string; to: string[]; raw: string }[] = [];
let server: SMTPServer;
let url = "";

beforeAll(async () => {
  server = new SMTPServer({
    authOptional: false,
    allowInsecureAuth: true,
    disabledCommands: ["STARTTLS"],
    onAuth(auth, _s, cb) {
      if (auth.username === "shop" && auth.password === "pw") return cb(null, { user: "shop" });
      cb(new Error("bad credentials"));
    },
    onRcptTo(addr, _s, cb) {
      if (addr.address.startsWith("gone")) return cb(Object.assign(new Error("on the suppression list"), { responseCode: 550 }));
      cb();
    },
    onData(stream, s, cb) {
      let raw = "";
      stream.on("data", (c: Buffer) => (raw += c.toString()));
      stream.on("end", () => {
        got.push({ from: s.envelope.mailFrom ? (s.envelope.mailFrom as { address: string }).address : "", to: s.envelope.rcptTo.map((r) => r.address), raw });
        cb();
      });
    },
  });
  await new Promise<void>((res) => server.listen(0, "127.0.0.1", () => res()));
  const port = (server.server.address() as { port: number }).port;
  url = `smtp://shop:pw@127.0.0.1:${port}`;
});
afterAll(() => new Promise<void>((res) => server.close(() => res())));

function Welcome({ name }: { name: string }) {
  return createElement("html", null, createElement("body", null, createElement("h1", null, `Welcome, ${name}!`), createElement("a", { href: "https://shop.example/start" }, "Get started")));
}

describe("email", () => {
  test("render turns a react-email component into HTML and text", async () => {
    const html = await render(createElement(Welcome, { name: "Ada" }));
    expect(html).toContain("<h1>Welcome, Ada!</h1>");
    const text = await render(createElement(Welcome, { name: "Ada" }), { plainText: true });
    expect(text).toContain("WELCOME, ADA!");
    expect(text).not.toContain("<h1>");
  });

  test("send renders react and submits over SMTP_URL with EMAIL_FROM", async () => {
    const res = await send(
      { to: ["ada@example.com", "gone@example.com"], subject: "Welcome", react: createElement(Welcome, { name: "Ada" }) },
      { env: { SMTP_URL: url, EMAIL_FROM: "shop@tiffin.localhost" } },
    );
    expect(res.accepted).toEqual(["ada@example.com"]);
    expect(res.rejected).toEqual(["gone@example.com"]);
    const m = got.at(-1)!;
    expect(m.from).toBe("shop@tiffin.localhost");
    expect(m.to).toEqual(["ada@example.com"]);
    expect(m.raw).toContain("Subject: Welcome");
    expect(m.raw).toContain("multipart/alternative");
    expect(m.raw).toContain("text/html");
    expect(m.raw).toContain("text/plain");
  });

  test("plain text, attachments and explicit from", async () => {
    await send(
      { to: "bob@example.com", from: "Shop <hello@shop.test>", subject: "Invoice", text: "Attached.", attachments: [{ filename: "a.txt", content: "abc" }] },
      { smtpUrl: url, env: {} },
    );
    const m = got.at(-1)!;
    expect(m.from).toBe("hello@shop.test");
    expect(m.raw).toContain('filename=a.txt');
  });

  test("clear errors without settings", async () => {
    await expect(send({ to: "a@b.c", subject: "x", text: "y" }, { env: {} })).rejects.toThrow(/SMTP_URL is not set/);
    await expect(send({ to: "a@b.c", subject: "x" }, { env: { SMTP_URL: url, EMAIL_FROM: "a@b.c" } })).rejects.toThrow(/html, text or react/);
    await expect(send({ to: "a@b.c", subject: "x", text: "y" }, { env: { SMTP_URL: url.replace(":pw@", ":wrong@"), EMAIL_FROM: "a@b.c" } })).rejects.toThrow();
  });
});
