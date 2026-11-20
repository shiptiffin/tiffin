// POST /api/cloud/boxes/:id/create {token, name, serverType, location, sshKeys}:
// queues the setup. The key travels sealed with the job and is forgotten
// when it ends; the owner's SSH public keys (up to 10) travel as they are.
// Called by /start the moment its payment form completes, so it confirms the
// payment with Stripe first; 402 means Stripe hasn't confirmed it yet (the
// page tries again for a moment).
import { after } from "next/server";
import { track } from "@shiptiffin/sdk/analytics";
import { ActionError, createBox, drainOutbox, type CreateInput } from "@/lib/cloud/actions";
import { actionLimit, json, problem, readJson, sameOrigin } from "@/lib/cloud/http";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function POST(request: Request, { params }: { params: Promise<{ id: string }> }) {
  if (!sameOrigin(request)) return problem(403, "Cross-site request refused.");
  const acct = await currentAccount({ fresh: true });
  if (!acct) return problem(401, "Sign in first.");
  if (!actionLimit(acct.id)) return problem(429, "Too many tries. Wait a few minutes.");
  const body = await readJson<CreateInput>(request, 40_000);
  if (!body) return problem(400, "Bad request.");
  try {
    const { retry, paidNow } = await createBox(acct, (await params).id, body);
    if (paidNow) {
      // Paid: counted once per Checkout session, and what the payment queued (its email) goes out.
      void track("checkout_completed", undefined, { request });
      after(() => drainOutbox().catch((e) => console.error("outbox", e instanceof Error ? e.message : e)));
    }
    void track("box_setup_started", { retry }, { request });
    return json({ ok: true });
  } catch (e) {
    if (e instanceof ActionError) return problem(e.status, e.message);
    console.error("create box", e instanceof Error ? e.message : e);
    return problem(500, "Setup couldn't start. Try again in a minute.");
  }
}
