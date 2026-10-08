// GET /api/cloud/boxes/:id: the box and its latest job, for the progress screen.
import { boxFor, latestJob } from "@/lib/cloud/db";
import { json, problem } from "@/lib/cloud/http";
import { dashboardUrl } from "@/lib/cloud/names";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

export async function GET(_: Request, { params }: { params: Promise<{ id: string }> }) {
  const acct = await currentAccount();
  if (!acct) return problem(401, "Sign in first.");
  const box = await boxFor((await params).id, acct.id);
  if (!box) return problem(404, "No such box.");
  const job = await latestJob(box.id);
  return json({
    ok: true,
    box: { id: box.id, name: box.name, status: box.status, plan: box.plan_status, dashboard: box.name ? dashboardUrl(box.name) : null },
    job: job && { kind: job.kind, status: job.status, steps: job.steps, error: job.error },
  });
}
