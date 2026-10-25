// Sign-ups for the box's analytics: the engine tells the collector when a
// request made an account (so a Signup event joins the visit, and with
// marketing attribution the account keeps where that visit came from) and
// when an account is deleted (its saved source goes with it). Reports go
// over loopback with the project's key; they never hold up or fail a sign-in.
import type { ProjectConfig } from "./config";

type Sink = NonNullable<ProjectConfig["analytics"]>;

const timeoutMs = 2000;

/** The client IP the box's edge forwarded: the last X-Forwarded-For entry. */
export function clientIP(req: Request): string {
  const xff = req.headers.get("x-forwarded-for");
  if (xff) return xff.split(",").at(-1)!.trim();
  return req.headers.get("x-real-ip") ?? "";
}

/** The tiffin_first_visit cookie (set by the box's edge when the project asks for it). */
export function firstVisitCookie(req: Request): string {
  for (const part of (req.headers.get("cookie") ?? "").split(";")) {
    const [k, ...v] = part.trim().split("=");
    if (k === "tiffin_first_visit") return v.join("=");
  }
  return "";
}

async function post(sink: Sink, url: string, body: unknown): Promise<void> {
  try {
    await fetch(url, {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${sink.key}` },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(timeoutMs),
    });
  } catch (err) {
    console.error(JSON.stringify({ level: "warn", msg: "analytics sign-up report failed", err: String(err) }));
  }
}

/** Reports that req made the account userId. */
export function reportSignup(sink: Sink | undefined, req: Request, host: string, userId: string): Promise<void> {
  if (!sink) return Promise.resolve();
  return post(sink, sink.url, {
    userId,
    host,
    ip: clientIP(req),
    ua: req.headers.get("user-agent") ?? "",
    gpc: req.headers.get("sec-gpc") === "1",
    firstVisit: firstVisitCookie(req),
    at: new Date().toISOString(),
  });
}

/** Reports that the account userId was deleted. */
export function reportDeleted(sink: Sink | undefined, userId: string): Promise<void> {
  if (!sink) return Promise.resolve();
  return post(sink, sink.url.replace(/\/signup$/, "/signup/delete"), { userId });
}
