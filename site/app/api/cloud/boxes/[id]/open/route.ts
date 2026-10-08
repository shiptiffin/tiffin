// GET /api/cloud/boxes/:id/open: "Open your dashboard". While we still hold
// the new box's setup sign-in key it mints a one-time sign-in link; after
// that it is just the dashboard's address.
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
