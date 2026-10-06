import type { NextRequest } from "next/server";
import { requireRole } from "@shiptiffin/sdk/next/auth";

// Members only: an API key capped at viewer gets 403.
export async function GET(req: NextRequest) {
  const org = req.nextUrl.searchParams.get("org") ?? undefined;
  const s = await requireRole("member", { signIn: false, ...(org ? { organizationId: org } : {}) });
  return Response.json({ org: s.organization.name, role: s.organization.role, via: s.via });
}
