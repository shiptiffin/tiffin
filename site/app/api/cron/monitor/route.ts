// POST /api/cron/monitor: the box's cron (tiffin.config.ts, every 5 minutes),
// signed with the project's signing secret. Runs the monitor, then sends
// what the outbox holds (emails, retried; Stripe cancel and refund actions).
import { verifyRequest } from "@shiptiffin/sdk/verify";
import { drainOutbox, runMonitor } from "@/lib/cloud/actions";
import { tablesReady } from "@/lib/cloud/db";

export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  const secret = process.env.TIFFIN_QUEUE_SIGNING_SECRET;
  if (!secret || !(await verifyRequest(request, secret))) return new Response("unsigned", { status: 401 });
  if (!(await tablesReady())) return Response.json({ skipped: "not ready" });
  const monitor = await runMonitor();
  const outbox = await drainOutbox();
  return Response.json({ ...monitor, outbox });
}
