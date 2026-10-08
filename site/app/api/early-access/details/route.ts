// POST /api/early-access/details: step 2 of the invite form, "Help us get
// your box right". Adds the optional answers to the request step 1 saved,
// found by the token step 1's reply carried. The reply is the same whether or
// not a request took them (see addDetails), apart from answers that need fixing.
import { clientIP, track } from "@shiptiffin/sdk/analytics";
import { boxDeps, rateLimiter } from "@/lib/early-access-box";
import { MESSAGES, addDetails, errorCode, parseDetails, type ErrorCode, type FieldErrors, type SignupReply } from "@/lib/early-access";

export const dynamic = "force-dynamic";

const allow = rateLimiter(10, 10 * 60 * 1000);

export async function POST(request: Request) {
  const fail = (status: number, code: ErrorCode, errors?: FieldErrors) =>
    Response.json({ ok: false, code, errors, message: MESSAGES[code] } satisfies SignupReply, { status });

  if (Number(request.headers.get("content-length") ?? 0) > 16_384) return fail(413, "long");
  let form: FormData;
  try {
    form = await request.formData();
  } catch {
    return fail(400, "invalid");
  }
  const parsed = parseDetails(form);
  if (!parsed.ok) return fail(400, errorCode(parsed.errors), parsed.errors);
  if (!allow(clientIP(request) ?? "unknown")) return fail(429, "busy");
  const deps = boxDeps(request);
  if (!deps) return fail(503, "unavailable");
  try {
    const r = await addDetails(String(form.get("d") ?? ""), parsed, deps);
    if (r === "saved") void track("invite_details", { projects: parsed.details.projects ?? "unsaid" }, { request });
  } catch (err) {
    console.error("invite request: saving details failed", err);
    return fail(503, "unavailable");
  }
  return Response.json({ ok: true } satisfies SignupReply);
}
