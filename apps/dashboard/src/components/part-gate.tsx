import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { q } from "@/api/queries";
import { Crumbs, Page, PageHeader } from "@/components/page";
import { Button } from "@/components/ui/button";
import { useMe } from "@/lib/me";
import { PARTS } from "@/lib/names";
import { partsOf } from "@/lib/sections";
import { change } from "@/lib/staged";

/** What Auth is for, said on its page before it's added. */
const WHY = "Sign-in for your users: email, magic links, passkeys, Google, GitHub, organizations and roles.";

/**
 * Auth's page when the project doesn't have it yet: what it is, and Add.
 * It is the one part that is added: it answers /api/auth on every app
 * address, which an app with its own sign-in uses. Database, KV, Files,
 * Email, Analytics and Jobs are always there, so their pages just work.
 */
export function PartGate({ project, part, children }: { project: string; part: "auth"; children: ReactNode }) {
  const p = useQuery(q.project(project));
  const { can } = useMe();
  if (!p.data || partsOf(p.data).has(part)) return <>{children}</>;
  const name = PARTS[part].name;
  return (
    <Page>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: name }]} />} title={name} lede={WHY} />
      <div className="mt-8 max-w-[40rem] rounded-[12px] border border-dashed border-rule-3 px-6 py-7">
        <p className="text-[0.9375rem] font-[550] text-ink">{project} doesn’t have sign-in yet.</p>
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
