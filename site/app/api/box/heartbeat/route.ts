// POST /api/box/heartbeat: a managed box's daily check-in (internal/mod/managed).
// Authorization: Bearer <licence>. The box sends its version and the names of
// failing checks; the answer says whether automatic updates may install.
import { heartbeat } from "@/lib/cloud/actions";
import { tablesReady } from "@/lib/cloud/db";
import { publicKeyFromSeed, verifyLicence } from "@/lib/cloud/licence";

export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  const pub = publicKeyFromSeed(process.env.CLOUD_LICENCE_KEY);
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
  return Response.json(await heartbeat(l.box, report), { headers: { "Cache-Control": "no-store" } });
}
