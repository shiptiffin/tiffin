// POST /api/cloud/checkout {renew?: box id}: the Stripe Checkout session for
// a new box (clientSecret: the payment form /start shows in the page), or to
// renew one whose subscription ended (url: Stripe's own page). One session
// per box, reused until it expires.
import { track } from "@shiptiffin/sdk/analytics";
import { baseUrl } from "@/lib/early-access-box";
import { ActionError, startCheckout } from "@/lib/cloud/actions";
import { signupOpen } from "@/lib/cloud/config";
import { json, problem, readJson, sameOrigin } from "@/lib/cloud/http";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  if (!sameOrigin(request)) return problem(403, "Cross-site request refused.");
  if (!(await signupOpen())) return problem(503, "Sign-up opens soon.");
  const acct = await currentAccount({ fresh: true });
  if (!acct) return problem(401, "Sign in first.");
  const body = (await readJson<{ renew?: string }>(request)) ?? {};
  const renew = typeof body.renew === "string" ? body.renew : undefined;
  try {
    const h = await startCheckout(acct, baseUrl(request), renew);
    void track("checkout_started", { renew: Boolean(renew) }, { request });
    return json(renew ? { ok: true, url: h.url } : { ok: true, clientSecret: h.clientSecret });
  } catch (e) {
    if (e instanceof ActionError) return problem(e.status, e.message);
    console.error("checkout", e instanceof Error ? e.message : e);
    return problem(502, "Payment isn't available right now. Try again in a minute.");
  }
}
