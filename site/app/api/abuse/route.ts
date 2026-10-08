// POST /api/abuse: a report about a <name>.shiptiffin.app address (form on /abuse).
import { clientIP } from "@shiptiffin/sdk/analytics";
import { reportAbuse } from "@/lib/cloud/actions";
import { tablesReady } from "@/lib/cloud/db";
import { abuseLimit } from "@/lib/cloud/http";

export const dynamic = "force-dynamic";

const back = (q: string) => new Response(null, { status: 303, headers: { location: `/abuse?${q}` } });

export async function POST(request: Request) {
  if (Number(request.headers.get("content-length") ?? 0) > 16_384) return back("error=long");
  let form: FormData;
  try {
    form = await request.formData();
  } catch {
    return back("error=form");
  }
  if (String(form.get("website") ?? "")) return back("sent=1"); // the honeypot
  const target = String(form.get("target") ?? "").trim();
  const details = String(form.get("details") ?? "").trim();
  const email = String(form.get("email") ?? "").trim() || null;
  if (!target || !details) return back("error=missing");
  if (!abuseLimit(clientIP(request) ?? "unknown")) return back("error=busy");
  if (!(await tablesReady())) return back("error=unavailable");
  try {
    await reportAbuse(target, email, details);
  } catch (e) {
    console.error("abuse report", e instanceof Error ? e.message : e);
    return back("error=unavailable");
  }
  return back("sent=1");
}
