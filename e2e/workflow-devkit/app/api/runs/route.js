import { getRun, start } from "workflow/api";
import { failing, twoSteps } from "../../../workflows/e2e";

export async function POST(req) {
  const { kind, tag, wait } = await req.json();
  const run = kind === "fatal" ? await start(failing, [tag]) : await start(twoSteps, [tag, wait]);
  return Response.json({ runId: run.runId });
}

export async function GET(req) {
  const run = getRun(new URL(req.url).searchParams.get("id"));
  const status = await run.status;
  return Response.json({ status, value: status === "completed" ? await run.returnValue : null });
}
