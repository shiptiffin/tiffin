// GET /sign-in/done?next=…[&new=1]: where a sign-in (email link, Google or
// GitHub) lands. Counts it (new=1: the sign-in made the account), then goes
// on to `next`.
import { track } from "@shiptiffin/sdk/analytics";
import { safeNext } from "@/lib/cloud/names";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function GET(request: Request) {
  const sp = new URL(request.url).searchParams;
  if (await currentAccount()) void track("signed_in", { new: sp.get("new") === "1" }, { request });
  return new Response(null, { status: 303, headers: { location: safeNext(sp.get("next") ?? undefined), "cache-control": "no-store" } });
}
