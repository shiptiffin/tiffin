// POST /api/early-access: someone joins the sign-up list on /start (name,
// email, note). A browser without JavaScript gets a redirect (303) to the
// next page; the form's script asks for JSON. Every accepted request gets the same answer,
// whether the address was new, already there or a bot's, so the form can't be
// used to find out who has asked.
import { clientIP, track } from "@shiptiffin/sdk/analytics";
import { boxDeps, rateLimiter } from "@/lib/early-access-box";
import { MESSAGES, errorCode, parseForm, signUp, type ErrorCode, type FieldErrors, type SignupReply } from "@/lib/early-access";

export const dynamic = "force-dynamic";

const allow = rateLimiter(5, 10 * 60 * 1000);

export async function POST(request: Request) {
  const json = (request.headers.get("accept") ?? "").includes("application/json");
  const fail = (status: number, code: ErrorCode, errors?: FieldErrors) =>
    json
      ? Response.json({ ok: false, code, errors, message: MESSAGES[code] } satisfies SignupReply, { status })
      : new Response(null, { status: 303, headers: { location: `/start?error=${code}#form` } });

  if (Number(request.headers.get("content-length") ?? 0) > 16_384) return fail(413, "long");
  let form: FormData;
  try {
    form = await request.formData();
  } catch {
    return fail(400, "email");
  }
  const parsed = parseForm(form);
  if (!parsed.ok) return fail(400, errorCode(parsed.errors), parsed.errors);

  if (!allow(clientIP(request) ?? "unknown")) return fail(429, "busy");
  const deps = boxDeps(request);
  if (!deps) {
    console.error("invite request: DATABASE_URL is not set; add services.postgres to the website project");
    return fail(503, "unavailable");
  }
  let details: string;
  try {
    const outcome = await signUp(parsed, deps);
    details = outcome.details;
    if (outcome.kind !== "bot") void track("invite_request", { role: parsed.answers.role ?? "unsaid" }, { request });
  } catch (err) {
    console.error("invite request: saving failed", err);
    return fail(503, "unavailable");
  }
  return json
    ? Response.json({ ok: true, details } satisfies SignupReply, { headers: { "cache-control": "no-store" } })
    : new Response(null, { status: 303, headers: { location: "/early-access/check-email" } });
}
