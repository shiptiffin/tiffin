import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { createServer, type AddressInfo } from "node:net";
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

  test("a send while the mail server restarts goes through once it is back", async () => {
    // A free port with nothing on it yet: the box's server is restarting.
    const probe = createServer();
    await new Promise<void>((res) => probe.listen(0, "127.0.0.1", () => res()));
    const port = (probe.address() as AddressInfo).port;
    await new Promise<void>((res) => probe.close(() => res()));
    const got: string[] = [];
    const later = new SMTPServer({
      authOptional: false,
      allowInsecureAuth: true,
      disabledCommands: ["STARTTLS"],
      onAuth: (_a, _s, cb) => cb(null, { user: "shop" }),
      onData(stream, _s, cb) {
        let raw = "";
        stream.on("data", (c: Buffer) => (raw += c.toString()));
        stream.on("end", () => (got.push(raw), cb()));
      },
    });
    setTimeout(() => later.listen(port, "127.0.0.1"), 800);
    try {
      const res = await send({ to: "ada@example.com", subject: "Back", text: "hi" }, { smtpUrl: `smtp://shop:pw@127.0.0.1:${port}`, env: { EMAIL_FROM: "shop@b.co" } });
      expect(res.accepted).toEqual(["ada@example.com"]);
      expect(got.length).toBe(1);
    } finally {
      await new Promise<void>((res) => later.close(() => res()));
    }
    // With retries off, the same outage is an error at once.
    await expect(send({ to: "a@b.co", subject: "x", text: "y" }, { smtpUrl: `smtp://shop:pw@127.0.0.1:${port}`, env: { EMAIL_FROM: "shop@b.co" }, retryFor: 0 })).rejects.toThrow();
  });

  test("a message the server took is never sent twice, even if the connection drops before its answer", async () => {
    // A bare SMTP server that takes the message, then hangs up without replying.
    let messages = 0;
    const flaky = createServer((sock) => {
      let data = false;
      let buf = "";
      sock.write("220 box\r\n");
      sock.on("data", (c) => {
        buf += c.toString();
        let i: number;
        while (!data && (i = buf.indexOf("\r\n")) >= 0) {
          const line = buf.slice(0, i);
          buf = buf.slice(i + 2);
          const cmd = line.slice(0, 4).toUpperCase();
          if (cmd === "EHLO") sock.write("250-box\r\n250 AUTH PLAIN\r\n");
          else if (cmd === "AUTH") sock.write("235 ok\r\n");
          else if (cmd === "DATA") (sock.write("354 go\r\n"), (data = true));
          else if (cmd === "QUIT") sock.end("221 bye\r\n");
          else sock.write("250 ok\r\n");
        }
        if (data && buf.includes("\r\n.\r\n")) {
          messages++;
          sock.destroy();
        }
      });
    });
    await new Promise<void>((res) => flaky.listen(0, "127.0.0.1", () => res()));
    const port = (flaky.address() as AddressInfo).port;
    try {
      await expect(send({ to: "a@b.co", subject: "Once", text: "y" }, { smtpUrl: `smtp://shop:pw@127.0.0.1:${port}`, env: { EMAIL_FROM: "shop@b.co" } })).rejects.toThrow();
      await new Promise((r) => setTimeout(r, 600));
      expect(messages).toBe(1);
    } finally {
      await new Promise<void>((res) => flaky.close(() => res()));
    }
  });

  test("clear errors without settings", async () => {
    await expect(send({ to: "a@b.c", subject: "x", text: "y" }, { env: {} })).rejects.toThrow(/SMTP_URL is not set/);
    await expect(send({ to: "a@b.c", subject: "x" }, { env: { SMTP_URL: url, EMAIL_FROM: "a@b.c" } })).rejects.toThrow(/html, text or react/);
    await expect(send({ to: "a@b.c", subject: "x", text: "y" }, { env: { SMTP_URL: url.replace(":pw@", ":wrong@"), EMAIL_FROM: "a@b.c" } })).rejects.toThrow();
  });
});
