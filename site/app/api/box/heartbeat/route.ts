// POST /api/box/heartbeat: a managed box's check-in, every six hours
// (internal/mod/managed). Authorization: Bearer <licence>. The box sends its
// version and the names of failing checks; the answer says whether automatic
// updates may install. A check-in counts (keeps the address, brings it back)
// only with the current setup's licence, sent from the box's own address.
import { clientIP } from "@shiptiffin/sdk/analytics";
import { heartbeat } from "@/lib/cloud/actions";
import { tablesReady } from "@/lib/cloud/db";
import { licencePublicFrom, verifyLicence } from "@/lib/cloud/licence";

export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  const pub = licencePublicFrom(process.env.CLOUD_LICENCE_PUBLIC);
  if (!pub || !(await tablesReady())) return new Response("not ready", { status: 503 });
  const token = request.headers.get("authorization")?.replace(/^Bearer\s+/i, "") ?? "";
  const l = verifyLicence(pub, token);
  if (!l) return new Response("bad licence", { status: 401 });
  const text = await request.text();
  if (text.length > 16_384) return new Response("too big", { status: 413 });
  let report: Record<string, unknown> = {};
  try {
    report = JSON.parse(text || "{}");
  } catch {}
  if (typeof report.boxID === "string" && report.boxID !== l.box) return new Response("licence and box differ", { status: 400 });
  // The address the box's edge saw (the last X-Forwarded-For entry is the edge's own).
  return Response.json(await heartbeat(l, clientIP(request), report), { headers: { "Cache-Control": "no-store" } });
}
