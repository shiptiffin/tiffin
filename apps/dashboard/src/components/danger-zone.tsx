import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import type { DataPart } from "@/api/client";
import { q } from "@/api/queries";
import { DeleteProject } from "@/components/delete-project";
import { Button } from "@/components/ui/button";
import { useMe } from "@/lib/me";
import { PARTS } from "@/lib/names";
import { dataChange } from "@/lib/staged";
import { relative } from "@/lib/time";

/** What Delete all data takes from each part, in a line. */
const DATA: Array<{ part: DataPart; line: string }> = [
  { part: "postgres", line: "Every table and row goes. You can restore it for 7 days." },
  { part: "valkey", line: "Every key goes. You can restore it for 7 days." },
  { part: "storage", line: "Every file in every bucket goes; the buckets stay. You can restore it for 7 days." },
];

const dateFmt = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "long" });

/**
 * A project's Danger zone: one bordered card of rows (title and what happens on
 * the left, the button on the right; on a phone the button wraps under).
 * Delete all data in Database, KV and Files, Restore while what was deleted
 * is kept (7 days), and Delete project last. Each button opens a confirm that
 * says exactly what goes. People who aren't admins see the rows without buttons.
 */
export function DangerZone({ project }: { project: string }) {
  const p = useQuery(q.project(project));
  const { admin } = useMe();
  const restorable = p.data?.restorable ?? [];
  const only = <p className="text-[0.8125rem] text-ink-3">Only an admin of this box can do this</p>;
  return (
    <div className="divide-y divide-rule rounded-[12px] border border-danger-rule">
      {DATA.map(({ part, line }) => (
        <Row key={part} title={`Delete all data in ${PARTS[part].name}`} line={line}>
          {admin ? (
            <Button variant="danger-outline" onClick={() => void dataChange(project, { part, action: "empty" })}>
              Delete data
            </Button>
          ) : (
            only
          )}
        </Row>
      ))}
      {restorable.map((r) => (
        <Row
          key={`restore-${r.part}`}
          title={`${PARTS[r.part as DataPart].name} data deleted ${relative(r.deletedAt)}`}
          line={`Restore puts it back in place of what ${PARTS[r.part as DataPart].name} holds now. After ${dateFmt.format(new Date(r.until))} it’s gone for good.`}
        >
          {admin ? <Button onClick={() => void dataChange(project, { part: r.part as DataPart, action: "restore" })}>Restore</Button> : only}
        </Row>
      ))}
      <Row title="Delete this project" line="Its apps, data and addresses go for good. Activity keeps the record.">
        {admin ? <DeleteProject project={project} variant="danger-outline" label="Delete project" /> : only}
      </Row>
    </div>
  );
}

function Row({ title, line, children }: { title: string; line: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3 px-4 py-4 sm:px-5">
      <div className="min-w-0 flex-[1_1_18rem]">
        <h3 className="text-[0.875rem] font-[550] text-ink">{title}</h3>
        <p className="mt-0.5 max-w-[36rem] text-[0.8125rem] text-ink-3">{line}</p>
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}
