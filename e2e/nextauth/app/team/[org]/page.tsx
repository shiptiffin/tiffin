import { requireRole } from "@shiptiffin/sdk/next/auth";

export default async function Team({ params }: { params: Promise<{ org: string }> }) {
  const { org } = await params;
  const { organization } = await requireRole("viewer", { organizationId: org });
  return <main id="team">{organization.name}</main>;
}
