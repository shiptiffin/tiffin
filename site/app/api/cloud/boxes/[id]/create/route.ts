// POST /api/cloud/boxes/:id/create {token, name, serverType, location, keepKey}:
// queues the setup. The key travels sealed with the job and is forgotten
// when it ends, unless keepKey.
import { ActionError, createBox, type CreateInput } from "@/lib/cloud/actions";
import { actionLimit, json, problem, readJson, sameOrigin } from "@/lib/cloud/http";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function POST(request: Request, { params }: { params: Promise<{ id: string }> }) {
  if (!sameOrigin(request)) return problem(403, "Cross-site request refused.");
  const acct = await currentAccount({ fresh: true });
  if (!acct) return problem(401, "Sign in first.");
  if (!actionLimit(acct.id)) return problem(429, "Too many tries. Wait a few minutes.");
  const body = await readJson<CreateInput>(request);
  if (!body) return problem(400, "Bad request.");
  try {
    await createBox(acct, (await params).id, body);
    return json({ ok: true });
  } catch (e) {
    if (e instanceof ActionError) return problem(e.status, e.message);
    console.error("create box", e instanceof Error ? e.message : e);
    return problem(500, "Setup couldn't start. Try again in a minute.");
  }
}
