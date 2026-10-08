// GET /api/cloud/boxes/:id/open: "Open your dashboard". Until the box says
// its owner signed in, the one-time sign-in link the box made (it works
// once; the box refuses it after 24 hours), or, when that expired, a request
// to the box for a new one; after that, the box's own sign-in.
import { ActionError, openDashboard } from "@/lib/cloud/actions";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function GET(request: Request, { params }: { params: Promise<{ id: string }> }) {
  const acct = await currentAccount();
  const { id } = await params;
  if (!acct) return Response.redirect(new URL(`/sign-in?next=/account`, request.url), 303);
  try {
    const url = await openDashboard(acct, id);
    return new Response(null, { status: 303, headers: { Location: url, "Cache-Control": "no-store", "Referrer-Policy": "no-referrer" } });
  } catch (e) {
    const msg = e instanceof ActionError ? e.message : "Couldn't open the dashboard.";
    return new Response(msg, { status: e instanceof ActionError ? e.status : 500 });
  }
}
