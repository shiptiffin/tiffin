import { solveChallenge } from "altcha-lib";
import type { Challenge } from "altcha-lib";
import { deriveKey } from "altcha-lib/algorithms/sha";
import type { ProjectConfig } from "../src/config";
import { outbox } from "../src/mail";

export const HOST = "shop.tiffin.localhost";
export const ORIGIN = `https://${HOST}:8443`;

export function projectConfig(databaseUrl: string, over: Partial<ProjectConfig> = {}): ProjectConfig {
  return {
    secret: "test-secret-0123456789abcdef0123456789abcdef",
    databaseUrl,
    smtpUrl: "",
    emailFrom: "shop@tiffin.localhost",
    appName: "Shop",
    hosts: [HOST],
    primaryUrl: ORIGIN,
    origins: [ORIGIN],
    methods: ["email", "magic-link", "otp", "passkey", "google", "github"],
    organizations: true,
    social: {},
    captcha: true,
    rateLimit: false,
    acceptInvitePath: "/accept-invite",
    requireEmailVerification: true,
    ...over,
  };
}

/** A tiny browser: keeps cookies, sends Origin, talks to one handler. */
export class Client {
  cookies = new Map<string, string>();
  constructor(
    private handle: (r: Request) => Promise<Response>,
    public headers: Record<string, string> = {},
  ) {}

  cookieHeader() {
    return [...this.cookies].map(([k, v]) => `${k}=${v}`).join("; ");
  }

  async raw(pathOrUrl: string, init: { method?: string; body?: unknown; headers?: Record<string, string> } = {}): Promise<Response> {
    const url = pathOrUrl.startsWith("http") ? pathOrUrl : `${ORIGIN}/api/auth${pathOrUrl}`;
    const u = new URL(url);
    const h: Record<string, string> = { host: u.host, origin: `${u.protocol}//${u.host}`, ...this.headers, ...init.headers };
    if (init.body !== undefined) h["content-type"] = "application/json";
    if (this.cookies.size) h.cookie = this.cookieHeader();
    const res = await this.handle(new Request(url, { method: init.method ?? (init.body !== undefined ? "POST" : "GET"), headers: h, body: init.body !== undefined ? JSON.stringify(init.body) : undefined, redirect: "manual" }));
    for (const c of res.headers.getSetCookie()) {
      const [pair] = c.split(";");
      const i = pair!.indexOf("=");
      const k = pair!.slice(0, i).trim();
      const v = pair!.slice(i + 1).trim();
      if (!v || /max-age=0/i.test(c)) this.cookies.delete(k);
      else this.cookies.set(k, v);
    }
    return res;
  }

  async json<T = any>(path: string, init: { method?: string; body?: unknown; headers?: Record<string, string> } = {}): Promise<{ status: number; body: T }> {
    const res = await this.raw(path, init);
    const text = await res.text();
    let body: any = null;
    try {
      body = text ? JSON.parse(text) : null;
    } catch {
      body = text;
    }
    return { status: res.status, body };
  }

  /** Solves the proof-of-work challenge the way the browser components do. */
  async captcha(): Promise<string> {
    const { body } = await this.json<Challenge>("/altcha/challenge");
    const solution = await solveChallenge({ challenge: body, deriveKey });
    if (!solution) throw new Error("could not solve challenge");
    return Buffer.from(JSON.stringify({ challenge: body, solution })).toString("base64");
  }

  async withCaptcha<T = any>(path: string, body: unknown) {
    return this.json<T>(path, { body, headers: { "x-captcha-response": await this.captcha() } });
  }
}

export function lastMail(to: string, kind?: string) {
  for (let i = outbox.length - 1; i >= 0; i--) {
    const m = outbox[i]!;
    if (m.to === to && (!kind || m.kind === kind)) return m;
  }
  throw new Error(`no ${kind ?? ""} mail to ${to}; outbox: ${outbox.map((m) => `${m.kind}->${m.to}`).join(", ")}`);
}

export function linkIn(text: string): string {
  const m = text.match(/https?:\/\/\S+/);
  if (!m) throw new Error("no link in: " + text);
  return m[0];
}

/** Signs up and verifies a person; returns their signed-in client. */
export async function person(handle: (r: Request) => Promise<Response>, email: string, name: string, password = "correct horse battery") {
  const c = new Client(handle);
  const r = await c.withCaptcha("/sign-up/email", { email, password, name });
  if (r.status !== 200) throw new Error(`sign-up ${email}: ${r.status} ${JSON.stringify(r.body)}`);
  const v = await c.raw(linkIn(lastMail(email, "verify").text));
  if (v.status >= 400) throw new Error(`verify ${email}: ${v.status} ${await v.text()}`);
  return c;
}
