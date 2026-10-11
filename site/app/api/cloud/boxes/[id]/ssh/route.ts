// POST /api/cloud/boxes/:id/ssh {token}: opens SSH (to keys only) on a box
// set up without it. Queues the open-ssh job; the token travels sealed with
// it and is forgotten when it ends.
import { ActionError, openSSH } from "@/lib/cloud/actions";
import { actionLimit, json, problem, readJson, sameOrigin } from "@/lib/cloud/http";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function POST(request: Request, { params }: { params: Promise<{ id: string }> }) {
  if (!sameOrigin(request)) return problem(403, "Cross-site request refused.");
  const acct = await currentAccount({ fresh: true });
  if (!acct) return problem(401, "Sign in first.");
  if (!actionLimit(acct.id)) return problem(429, "Too many tries. Wait a few minutes.");
  const body = await readJson<{ token?: string }>(request);
  if (!body) return problem(400, "Bad request.");
  try {
    return json({ ok: true, message: await openSSH(acct, (await params).id, body.token) });
  } catch (e) {
    if (e instanceof ActionError) return problem(e.status, e.message);
    console.error("open ssh", e instanceof Error ? e.message : e);
    return problem(500, "That didn't work. Try again in a minute.");
  }
}
