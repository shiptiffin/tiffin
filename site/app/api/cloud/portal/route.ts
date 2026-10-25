// POST /api/cloud/portal: Stripe's billing portal (card, invoices, cancel).
import { track } from "@shiptiffin/sdk/analytics";
import { ActionError, billingPortal } from "@/lib/cloud/actions";
import { json, problem, sameOrigin } from "@/lib/cloud/http";
import { currentAccount } from "@/lib/cloud/session";
import { baseUrl } from "@/lib/early-access-box";

export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  if (!sameOrigin(request)) return problem(403, "Cross-site request refused.");
  const acct = await currentAccount({ fresh: true });
  if (!acct) return problem(401, "Sign in first.");
  try {
    const url = await billingPortal(acct, baseUrl(request));
    void track("portal_opened", undefined, { request });
    return json({ ok: true, url });
  } catch (e) {
    if (e instanceof ActionError) return problem(e.status, e.message);
    console.error("portal", e instanceof Error ? e.message : e);
    return problem(502, "Billing isn't available right now. Try again in a minute.");
  }
}
