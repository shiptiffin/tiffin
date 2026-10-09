// GET /api/cloud/names?name=acme: whether a box name can be used, for the
// form while it's typed. Create checks again (the name is unique in the
// database), so this only saves a wasted try.
import { nameTaken } from "@/lib/cloud/db";
import { checkLimit, json, problem } from "@/lib/cloud/http";
import { boxDomain, nameProblem } from "@/lib/cloud/names";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function GET(request: Request) {
  const acct = await currentAccount();
  if (!acct) return problem(401, "Sign in first.");
  if (!checkLimit(`names:${acct.id}`)) return problem(429, "Too many tries. Wait a few minutes.");
  const name = (new URL(request.url).searchParams.get("name") ?? "").trim().toLowerCase();
  const why = nameProblem(name);
  if (why) return json({ ok: true, available: false, message: why });
  if (await nameTaken(name)) return json({ ok: true, available: false, message: `${boxDomain(name)} is taken. Try another name.` });
  return json({ ok: true, available: true, message: `${boxDomain(name)} is free.` });
}
