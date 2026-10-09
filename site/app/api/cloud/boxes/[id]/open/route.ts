// GET /api/cloud/boxes/:id/open: "Open your dashboard". Until the box says
// its owner signed in, the one-time sign-in link the box made (it works
// once; the box refuses it after 24 hours), or, when that expired, a request
// to the box for a new one; after that, the box's own sign-in.
import { ActionError, openDashboard } from "@/lib/cloud/actions";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function GET(request: Request, { params }: { params: Promise<{ id: string }> }) {
  const acct = await currentAccount({ fresh: true });
  const { id } = await params;
  // Signed out (the "Open your dashboard" button in the box-ready email): sign in, then come back here.
  const next = /^[\w-]{1,64}$/.test(id) ? `/api/cloud/boxes/${id}/open` : "/account";
  if (!acct) return Response.redirect(new URL(`/sign-in?next=${encodeURIComponent(next)}`, request.url), 303);
  try {
    const url = await openDashboard(acct, id);
    return new Response(null, { status: 303, headers: { Location: url, "Cache-Control": "no-store", "Referrer-Policy": "no-referrer" } });
  } catch (e) {
    const msg = e instanceof ActionError ? e.message : "Couldn't open the dashboard.";
    return new Response(msg, { status: e instanceof ActionError ? e.status : 500 });
  }
}
