// POST /api/cloud/checkout: a Stripe Checkout page for a new box.
import { baseUrl } from "@/lib/early-access-box";
import { startCheckout } from "@/lib/cloud/actions";
import { signupOpen } from "@/lib/cloud/config";
import { json, problem, sameOrigin } from "@/lib/cloud/http";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  if (!sameOrigin(request)) return problem(403, "Cross-site request refused.");
  if (!(await signupOpen())) return problem(503, "Sign-up opens soon.");
  const acct = await currentAccount();
  if (!acct) return problem(401, "Sign in first.");
  try {
    return json({ ok: true, url: await startCheckout(acct, baseUrl(request)) });
  } catch (e) {
    console.error("checkout", e instanceof Error ? e.message : e);
    return problem(502, "Payment isn't available right now. Try again in a minute.");
  }
}
