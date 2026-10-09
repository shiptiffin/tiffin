// GET /api/cloud/boxes/:id: the box and its latest job, for the progress
// screens (setup on /start; deleting on /account, with ?job=delete_server).
import { boxFor, latestJob } from "@/lib/cloud/db";
import { json, problem } from "@/lib/cloud/http";
import { dashboardUrl } from "@/lib/cloud/names";
import { currentAccount } from "@/lib/cloud/session";

export const dynamic = "force-dynamic";

const KINDS = new Set(["provision", "delete_server"]);

export async function GET(req: Request, { params }: { params: Promise<{ id: string }> }) {
  const acct = await currentAccount();
  if (!acct) return problem(401, "Sign in first.");
  const box = await boxFor((await params).id, acct.id);
  if (!box) return problem(404, "No such box.");
  const kind = new URL(req.url).searchParams.get("job");
  const job = await latestJob(box.id, kind && KINDS.has(kind) ? [kind] : undefined);
  return json({
    ok: true,
    box: {
      id: box.id,
      name: box.name,
      status: box.status,
      plan: box.plan_status,
      dashboard: box.name ? dashboardUrl(box.name) : null,
      deletedAt: box.deleted_at,
      dataDeleted: box.data_deleted,
    },
    job: job && { kind: job.kind, status: job.status, steps: job.steps, error: job.error },
  });
}
