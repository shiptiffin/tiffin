import { useQuery } from "@tanstack/react-query";
import { Command } from "cmdk";
import { ArrowUpRight, Search } from "lucide-react";
import { useState } from "react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useDebounced } from "@/lib/debounced";
import { db, dq, type Filter } from "./api";
import { rawText } from "./format";

/**
 * Picks a row of another table for a link: search by its label (or its
 * key), arrows and Enter to choose. "Open" goes to that row instead.
 */
export function LinkPicker({
  project,
  branch,
  link,
  column,
  current,
  nullable,
  onPick,
  onOpen,
  onClose,
}: {
  project: string;
  branch: string;
  link: { schema: string; table: string; column: string };
  column: string;
  current: unknown;
  nullable?: boolean;
  /** The chosen key, and the row's label when it has one. */
  onPick: (v: unknown, label?: string) => void;
  onOpen?: (v: unknown) => void;
  onClose: () => void;
}) {
  const [q, setQ] = useState("");
  const dq_ = useDebounced(q, 200);
  const ref = useQuery(dq.table(project, link.schema, link.table, branch || undefined));
  const label = ref.data?.label;
  const keyCol = link.column;
  const keyType = ref.data?.columns.find((c) => c.name === keyCol)?.category;
  const filters: Filter[] = [];
  const t = dq_.trim();
  if (t && label) filters.push({ column: label, op: "contains", value: t });
  else if (t) filters.push({ column: keyCol, op: keyType === "number" ? "eq" : "contains", value: t });
  const rows = useQuery({
    queryKey: ["pg-pick", project, branch, link.schema, link.table, t],
    queryFn: () => db.rows(project, link.schema, link.table, { filters, limit: 30, ...(branch ? { branch } : {}) }),
    enabled: ref.isSuccess,
    placeholderData: (p) => p,
  });
  // Typing a number also finds the row with that key.
  const byKey = useQuery({
    queryKey: ["pg-pick-key", project, branch, link.schema, link.table, t],
    queryFn: () => db.rows(project, link.schema, link.table, { filters: [{ column: keyCol, op: "eq", value: t }], limit: 1, ...(branch ? { branch } : {}) }),
    enabled: ref.isSuccess && !!label && /^\d+$/.test(t) && keyType === "number",
  });
  const cols = rows.data?.columns ?? [];
  const ki = cols.findIndex((c) => c.name === keyCol);
  const li = label ? cols.findIndex((c) => c.name === label) : -1;
  const list = [...(byKey.data?.rows ?? []), ...(rows.data?.rows ?? [])].filter(
    (r, i, all) => all.findIndex((x) => rawText(x[ki]) === rawText(r[ki])) === i,
  );

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader className="pb-3">
          <DialogTitle>
            Link {column} to a row of <span className="font-mono">{link.table}</span>
          </DialogTitle>
          <DialogDescription>
            {label ? `Search by ${label} or by ${keyCol}.` : `Search by ${keyCol}.`} Now: <span className="font-mono text-ink">{current === null || current === undefined ? "nothing" : rawText(current)}</span>
          </DialogDescription>
        </DialogHeader>
        <Command shouldFilter={false} label={`Rows of ${link.table}`} className="flex min-h-0 flex-col">
          <div className="mx-6 flex items-center gap-2 rounded-md border border-rule-2 bg-paper px-3 focus-within:border-brass">
            <Search className="size-4 shrink-0 text-ink-3" aria-hidden />
            <Command.Input value={q} onValueChange={setQ} placeholder={label ? `Search ${link.table} by ${label}…` : "Search…"} className="h-9 w-full bg-transparent text-base text-ink outline-none placeholder:text-ink-4" />
          </div>
          <Command.List className="mt-2 max-h-[min(22rem,50vh)] overflow-y-auto px-3 pb-3">
            <Command.Empty className="px-3 py-6 text-center text-base text-ink-3">{rows.isFetching ? "Looking…" : "No rows match."}</Command.Empty>
            {nullable && (
              <Command.Item value="__none" onSelect={() => onPick(null)} className="flex h-9 cursor-default items-center gap-3 rounded-md px-3 text-base text-ink-3 data-[selected=true]:bg-paper-select data-[selected=true]:text-ink">
                No link (empty)
              </Command.Item>
            )}
            {list.map((r) => {
              const k = r[ki];
              return (
                <Command.Item
                  key={rawText(k)}
                  value={rawText(k)}
                  onSelect={() => onPick(k, li >= 0 ? rawText(r[li]) : undefined)}
                  className="group flex h-9 cursor-default items-center gap-3 rounded-md px-3 text-base text-ink data-[selected=true]:bg-paper-select"
                >
                  <span className="min-w-0 flex-1 truncate">{li >= 0 ? rawText(r[li]) || <span className="text-ink-3">empty</span> : rawText(k)}</span>
                  <span className="shrink-0 font-mono text-xs text-ink-3">{rawText(k).slice(0, 13)}</span>
                  {rawText(k) === rawText(current) && <span className="shrink-0 text-xs text-brass-ink">current</span>}
                </Command.Item>
              );
            })}
          </Command.List>
        </Command>
        {onOpen && current !== null && current !== undefined && (
          <div className="flex justify-end border-t border-rule px-6 py-3">
            <button type="button" onClick={() => onOpen(current)} className="inline-flex items-center gap-1 text-sm text-ink-2 hover:text-ink">
              Open the linked {link.table} row
              <ArrowUpRight className="size-3.5" />
            </button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
