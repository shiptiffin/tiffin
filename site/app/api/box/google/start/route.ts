// POST /api/box/google/start: a managed box asks for Google's consent page
// (lib/cloud/google-relay.ts). Authorization: Bearer <licence>. The box
// sends its own state and the S256 hash of its verifier; the answer is the
// page to open in the browser that pressed Connect with Google.
import { consentURL, signState, validStart, boxCallback } from "@/lib/cloud/google-relay";
import { body, refuse, relayCaller } from "@/lib/cloud/google-relay-box";

export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  const who = await relayCaller(request);
  if (who instanceof Response) return who;
  const { state, challenge } = await body(request);
  if (!validStart(state, challenge)) return refuse(400, "invalid_request", "state and challenge are required.");
  if (!boxCallback(who.licence.domain)) return refuse(400, "invalid_request", "This box's licence has no domain to come back to.");
  const signed = signState(who.app, { b: who.licence.box, d: who.licence.domain, s: state, c: challenge as string });
  return Response.json({ url: consentURL(who.app, signed) }, { headers: { "Cache-Control": "no-store" } });
}
