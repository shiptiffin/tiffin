import { useQuery } from "@tanstack/react-query";
import { useLayoutEffect, useRef } from "react";
import { mod } from "@/api/modules";
import { LevelTag, LineMessage, LineTime } from "@/components/logs-table";
import { merge, sourceLabel, streamOf, toLine, type Line } from "@/components/logs-query";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogFooter, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/cn";

const AROUND = 40;

/**
 * "Show surrounding lines": what the same app (or service) wrote just
 * before and after a line, whatever the search was, with the line marked.
 */
export function LogsContext({
  line,
  scope,
  onClose,
  onShowAll,
}: {
  line: Line | null;
  scope: string;
  onClose: () => void;
  onShowAll: (stream: string, at: number) => void;
}) {
  const stream = line ? streamOf(line.row) : "";
  const res = useQuery({
    queryKey: ["logs-context", scope, stream, line?.iso],
    enabled: !!line,
    staleTime: 60_000,
    queryFn: async () => {
      const iso = line!.iso;
      const [before, after] = await Promise.all([
        mod.logs({ query: stream, project: scope || undefined, end: iso, since: "24h", limit: AROUND + 1 }),
        mod.logs({ query: `${stream} | sort by (_time)`, project: scope || undefined, start: iso, limit: AROUND + 1 }),
      ]);
      return merge((before.rows ?? []).map(toLine), (after.rows ?? []).map(toLine), [line!]);
    },
  });
  const box = useRef<HTMLOListElement>(null);
  useLayoutEffect(() => {
    if (!res.data || !line) return;
    box.current?.querySelector(`[data-anchor]`)?.scrollIntoView({ block: "center" });
  }, [res.data, line]);

  const who = line ? [line.source, line.kind ? sourceLabel(line.kind).toLowerCase() : ""].filter(Boolean).join(", ") : "";
  return (
    <Dialog open={!!line} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-5xl">
        <DialogHeader>
          <DialogTitle>Surrounding lines</DialogTitle>
          <DialogDescription>
            What {who || "the same source"} wrote just before and after this line, ignoring the search.
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="px-0 pb-0">
          {res.isError && <ProblemNote error={res.error} className="mx-6 mb-4" />}
          {res.isPending && <Skeleton className="mx-6 mb-4 h-[50vh]" />}
          {res.data && line && (
            <ol ref={box} className="max-h-[60vh] overflow-y-auto border-y border-rule bg-paper-sunk py-1 font-mono text-[0.75rem] leading-5 [font-variant-ligatures:none]">
              {res.data.map((l) => {
                const anchor = l.key === line.key;
                return (
                  <li
                    key={l.key}
                    data-anchor={anchor || undefined}
                    aria-current={anchor || undefined}
                    className={cn(
                      "grid grid-cols-[auto_3rem_minmax(0,1fr)] items-baseline gap-x-3 px-6 py-[1px]",
                      anchor ? "bg-brass-wash shadow-[inset_2px_0_0_var(--brass)]" : "",
                    )}
                  >
                    <LineTime line={l} />
                    <LevelTag line={l} />
                    <span className={cn("min-w-0 break-all whitespace-pre-wrap", anchor || l.level === "error" ? "text-ink" : "text-ink-2")}>
                      <LineMessage line={l} terms={null} />
                    </span>
                  </li>
                );
              })}
            </ol>
          )}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
          <Button onClick={() => line && onShowAll(stream, line.t)} disabled={!line}>
            Open in the list
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
