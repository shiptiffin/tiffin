// GET /early-access/confirm?t=…: the link in the confirmation email.
import { track } from "@shiptiffin/sdk/analytics";
import { boxDeps } from "@/lib/early-access-box";
import { confirm } from "@/lib/early-access";

export const dynamic = "force-dynamic";

export async function GET(request: Request) {
  const token = new URL(request.url).searchParams.get("t") ?? "";
  const deps = boxDeps(request);
  if (!deps) return to("/early-access?error=unavailable");
  try {
    const r = await confirm(token, deps);
    if (r === "invalid") return to("/early-access/link-invalid");
    if (r === "confirmed") void track("invite_confirm", undefined, { request });
    return to("/early-access/confirmed");
  } catch (err) {
    console.error("invite request: confirm failed", err);
    return to("/early-access?error=unavailable");
  }
}

const to = (path: string) =>
  new Response(null, { status: 303, headers: { location: path, "cache-control": "no-store", "referrer-policy": "no-referrer" } });
