// POST /api/cloud/boxes/:id/check {token}: checks a Hetzner key and lists
// what it can order, at the customer's prices. The key is not stored.
import { ActionError, checkKey } from "@/lib/cloud/actions";
import { checkLimit, json, problem, readJson, sameOrigin } from "@/lib/cloud/http";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function POST(request: Request, { params }: { params: Promise<{ id: string }> }) {
  if (!sameOrigin(request)) return problem(403, "Cross-site request refused.");
  const acct = await currentAccount();
  if (!acct) return problem(401, "Sign in first.");
  if (!checkLimit(acct.id)) return problem(429, "Too many tries. Wait a few minutes.");
  const body = await readJson<{ token?: string }>(request);
  if (!body?.token) return problem(400, "Paste your Hetzner token.");
  try {
    const r = await checkKey(acct, (await params).id, body.token);
    return json(r, r.ok ? 200 : 400);
  } catch (e) {
    if (e instanceof ActionError) return problem(e.status, e.message);
    console.error("hetzner check", e instanceof Error ? e.message : e);
    return problem(502, "We couldn't check the key just now. Try again in a minute.");
  }
}
