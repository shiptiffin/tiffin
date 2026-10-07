import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { q } from "@/api/queries";
import { Crumbs, Page, PageHeader } from "@/components/page";
import { Button } from "@/components/ui/button";
import { useMe } from "@/lib/me";
import { PARTS } from "@/lib/names";
import { partsOf, type Part } from "@/lib/sections";
import { change } from "@/lib/staged";

/** What each built-in part is for, said on its page before it's added. */
const WHY: Partial<Record<Part, string>> = {
  postgres: "Postgres 18 for this project: tables you can edit here, SQL, copies for previews, and restore points.",
  valkey: "A Redis-compatible key-value store, also a cache. Next.js apps use it as one shared cache across copies.",
  storage: "S3-compatible buckets for uploads and assets, with public links, presigned links and a 7-day trash.",
  email: "Send email from your apps. Until you set a relay, every message lands in the dev inbox here.",
  auth: "Sign-in for your users: email, magic links, passkeys, Google, GitHub, organizations and roles.",
  analytics: "Cookieless analytics for this project's apps: visitors, pages, referrers and Web Vitals.",
};

/**
 * A part's page when the project doesn't have that part yet: what it is,
 * and Add. Every part shows in the sidebar, so this is where an absent one
 * lands instead of an error.
 */
export function PartGate({ project, part, children }: { project: string; part: Exclude<Part, "apps" | "jobs">; children: ReactNode }) {
  const p = useQuery(q.project(project));
  const { can } = useMe();
  if (!p.data || partsOf(p.data, false).has(part)) return <>{children}</>;
  const name = PARTS[part].name;
  return (
    <Page>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: name }]} />} title={name} lede={WHY[part]} />
      <div className="mt-8 max-w-[40rem] rounded-[12px] border border-dashed border-rule-3 px-6 py-7">
        <p className="text-[0.9375rem] font-[550] text-ink">{project} doesn’t have {PARTS[part].a} yet.</p>
        <p className="mt-1 text-sm text-ink-3">It’s built in: adding it takes a few seconds, and History can undo it.</p>
        {can("apply:reversible") && (
          <Button variant="primary" className="mt-4" onClick={() => change(project, { kind: "service", service: part, from: "off", to: "on" }, { immediate: true })}>
            Add {name}
          </Button>
        )}
      </div>
    </Page>
  );
}
