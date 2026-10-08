// POST /api/admin: owner-only actions (the abuse kill switch, restoring an
// address, marking reports, the money-back refund). Owners are the account
// ids in CLOUD_ADMIN_USER_IDS, with a verified email.
import { ActionError, adminAction } from "@/lib/cloud/actions";
import { isAdmin } from "@/lib/cloud/config";
import { json, problem, readJson, sameOrigin } from "@/lib/cloud/http";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  if (!sameOrigin(request)) return problem(403, "Cross-site request refused.");
  const acct = await currentAccount({ fresh: true });
  if (!isAdmin(acct)) return problem(403, "Owners only.");
  const body = await readJson<Parameters<typeof adminAction>[0]>(request);
  if (!body?.action) return problem(400, "Bad request.");
  try {
    return json({ ok: true, message: await adminAction(body) });
  } catch (e) {
    if (e instanceof ActionError) return problem(e.status, e.message);
    console.error("admin", e instanceof Error ? e.message : e);
    return problem(500, "That didn't work.");
  }
}
