// POST /api/early-access/remove?t=…: takes an address off the list (deletes
// its row). Used by the button on /early-access/remove and by mail apps'
// one-click unsubscribe (List-Unsubscribe-Post, RFC 8058).
import { boxDeps } from "@/lib/early-access-box";
import { remove } from "@/lib/early-access";

export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  let token = new URL(request.url).searchParams.get("t") ?? "";
  let oneClick = false;
  try {
    const form = await request.formData();
    token ||= String(form.get("t") ?? "");
    oneClick = form.get("List-Unsubscribe") === "One-Click";
  } catch {
    // no body: the token is in the address
  }
  const deps = boxDeps(request);
  if (!deps) return oneClick ? new Response("unavailable", { status: 503 }) : redirect("/early-access?error=unavailable");
  try {
    await remove(token, deps);
  } catch (err) {
    console.error("early access: remove failed", err);
    return oneClick ? new Response("unavailable", { status: 503 }) : redirect("/early-access?error=unavailable");
  }
  // Removed, or nothing had this token (already removed): the same either way.
  return oneClick ? new Response("removed") : redirect("/early-access/removed");
}

const redirect = (to: string) => new Response(null, { status: 303, headers: { location: to } });
