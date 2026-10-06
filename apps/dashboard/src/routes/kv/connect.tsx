import { useQuery } from "@tanstack/react-query";
import { Eye } from "lucide-react";
import { useState } from "react";
import { mod } from "@/api/modules";
import { Command, CopyButton } from "@/components/copy";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useMe } from "@/lib/me";

/**
 * How apps reach the KV: the env every app in the project already has, by
 * name. Secret values only for people with full access, on request (audited).
 */
export function KvConnect({ project, open, onOpenChange }: { project: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { can } = useMe();
  const [reveal, setReveal] = useState(false);
  const c = useQuery({ queryKey: ["kv-connection", project, reveal], queryFn: () => mod.kvConnection(project, reveal), enabled: open, staleTime: 60_000 });
  const url = c.data?.env?.find((e) => e.name === "REDIS_URL")?.value;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>Connect to the KV</DialogTitle>
          <DialogDescription>Every app in {project} already has these, so there is nothing to set. Keys start with the prefix on REDIS_URL; the REST endpoint adds it for you.</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          {c.isError && <ProblemNote error={c.error} />}
          {c.data && (
            <div className="overflow-hidden rounded-[10px] border border-rule-2">
              {(c.data.env ?? []).map((e) => (
                <div key={e.name} className="grid grid-cols-[minmax(0,13rem)_minmax(0,1fr)_auto] items-center gap-3 border-b border-rule px-3 py-1.5 last:border-b-0">
                  <code className="truncate font-mono text-[0.78125rem] text-ink">{e.name}</code>
                  <code className="truncate font-mono text-[0.78125rem] text-ink-2" title={e.value || undefined}>
                    {e.value || (e.secret ? "secret" : "")}
                  </code>
                  {e.value ? <CopyButton value={e.value} label={`Copy ${e.name}`} className="size-6" /> : <span className="size-6" />}
                </div>
              ))}
            </div>
          )}
          {c.data && !c.data.revealed && (
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-sm text-ink-3">{can("apply:irreversible") ? "Secrets stay hidden until you ask. Showing them is recorded." : "Showing secrets needs full access to this project."}</p>
              {can("apply:irreversible") && (
                <Button size="sm" onClick={() => setReveal(true)}>
                  <Eye />
                  Show secrets
                </Button>
              )}
            </div>
          )}
          {url && <Command cmd={`valkey-cli -u "${url}"`} />}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}
