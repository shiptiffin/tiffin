import { ArrowDown, ArrowUp, Columns3, Plus, X } from "lucide-react";
import { DropdownMenu as M, Popover as P } from "radix-ui";
import { useState } from "react";
import { Select } from "@/components/ui/choice";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { ColumnDetail, Filter } from "./api";
import { OPS, opsFor, type View } from "./view";

const chip =
  "inline-flex h-7 max-w-full items-center gap-1.5 rounded-[6px] border border-rule-2 bg-paper-raised pl-2.5 text-[0.8125rem] text-ink shadow-[var(--top-light)] transition-colors hover:border-rule-3";

/** Words for a filter: "price < 30", "in_stock is true", "isbn is empty". */
export function filterWords(f: Filter) {
  const v = f.op === "in" ? (f.values ?? []).join(", ") : f.op === "isNull" || f.op === "notNull" ? "" : (f.value ?? "");
  return { col: f.column, op: OPS[f.op], v };
}

/**
 * Filters as chips (each opens its editor; × removes it), "+ Filter", the
 * sort and which columns show. Everything here lives in the URL.
 */
export function FilterBar({ columns, view, onView }: { columns: ColumnDetail[]; view: View; onView: (v: View) => void }) {
  const [adding, setAdding] = useState(false);
  const set = (filters: Filter[]) => onView({ ...view, filters });
  return (
    <div className="flex flex-wrap items-center gap-1.5" role="toolbar" aria-label="Filters and sort">
      {view.filters.map((f, i) => {
        const w = filterWords(f);
        return (
          <span key={i} className={chip}>
            <FilterEditor
              columns={columns}
              initial={f}
              onDone={(nf) => set(view.filters.map((x, j) => (j === i ? nf : x)))}
              trigger={
                <button type="button" className="min-w-0 truncate text-left" aria-label={`Filter: ${w.col} ${w.op} ${w.v}. Change it`}>
                  <span className="font-mono">{w.col}</span> <span className="text-ink-3">{w.op}</span> {w.v && <span className="font-mono">{w.v}</span>}
                </button>
              }
            />
            <button
              type="button"
              aria-label={`Remove the filter on ${f.column}`}
              onClick={() => set(view.filters.filter((_, j) => j !== i))}
              className="grid h-full w-6 shrink-0 place-items-center rounded-r-[6px] text-ink-3 hover:bg-paper-hover hover:text-ink"
            >
              <X className="size-3.5" />
            </button>
          </span>
        );
      })}
      <FilterEditor
        columns={columns}
        open={adding}
        onOpenChange={setAdding}
        onDone={(nf) => set([...view.filters, nf])}
        trigger={
          <Button variant="ghost" size="sm" className="text-ink-2">
            <Plus />
            Filter
          </Button>
        }
      />
      {view.sort.map((s, i) => (
        <span key={s.column} className={chip}>
          <button
            type="button"
            className="inline-flex items-center gap-1"
            aria-label={`Sorted by ${s.column}, ${s.desc ? "highest" : "lowest"} first. Flip it`}
            onClick={() => onView({ ...view, sort: view.sort.map((x, j) => (j === i ? { ...x, desc: !x.desc } : x)) })}
          >
            <span className="text-ink-3">Sort:</span> <span className="font-mono">{s.column}</span>
            {s.desc ? <ArrowDown className="size-3.5 text-ink-2" /> : <ArrowUp className="size-3.5 text-ink-2" />}
          </button>
          <button
            type="button"
            aria-label={`Stop sorting by ${s.column}`}
            onClick={() => onView({ ...view, sort: view.sort.filter((_, j) => j !== i) })}
            className="grid h-full w-6 place-items-center rounded-r-[6px] text-ink-3 hover:bg-paper-hover hover:text-ink"
          >
            <X className="size-3.5" />
          </button>
        </span>
      ))}
      <ColumnsMenu columns={columns} hidden={view.hidden} onHidden={(hidden) => onView({ ...view, hidden })} />
    </div>
  );
}

function ColumnsMenu({ columns, hidden, onHidden }: { columns: ColumnDetail[]; hidden: string[]; onHidden: (h: string[]) => void }) {
  return (
    <M.Root>
      <M.Trigger asChild>
        <Button variant="ghost" size="sm" className="text-ink-2">
          <Columns3 />
          {hidden.length ? `${hidden.length} hidden` : "Columns"}
        </Button>
      </M.Trigger>
      <M.Portal>
        <M.Content align="start" sideOffset={6} className="z-50 max-h-[60vh] min-w-52 overflow-y-auto rounded-lg border border-rule bg-paper-raised p-1 shadow-raised data-[state=open]:animate-pop">
          <M.Label className="px-2 pt-1.5 pb-1 text-xs text-ink-3">Show columns</M.Label>
          {columns.map((c) => (
            <M.CheckboxItem
              key={c.name}
              checked={!hidden.includes(c.name)}
              onCheckedChange={(on) => onHidden(on ? hidden.filter((h) => h !== c.name) : [...hidden, c.name])}
              onSelect={(e) => e.preventDefault()}
              className="relative flex h-8 cursor-default items-center gap-2 rounded-md pr-2 pl-7 font-mono text-[0.8125rem] text-ink-2 outline-hidden select-none data-[highlighted]:bg-paper-hover data-[highlighted]:text-ink"
            >
              <M.ItemIndicator className="absolute left-2 text-ink">✓</M.ItemIndicator>
              {c.name}
            </M.CheckboxItem>
          ))}
          {hidden.length > 0 && (
            <>
              <M.Separator className="-mx-1 my-1 h-px bg-rule" />
              <M.Item onSelect={() => onHidden([])} className="flex h-8 cursor-default items-center rounded-md px-2 text-base text-ink-2 outline-hidden data-[highlighted]:bg-paper-hover">
                Show all
              </M.Item>
            </>
          )}
        </M.Content>
      </M.Portal>
    </M.Root>
  );
}

/** Column, operator, value: a small form in a popover. */
function FilterEditor({
  columns,
  initial,
  onDone,
  trigger,
  open,
  onOpenChange,
}: {
  columns: ColumnDetail[];
  initial?: Filter;
  onDone: (f: Filter) => void;
  trigger: React.ReactNode;
  open?: boolean;
  onOpenChange?: (o: boolean) => void;
}) {
  const [own, setOwn] = useState(false);
  const isOpen = open ?? own;
  const setOpen = onOpenChange ?? setOwn;
  return (
    <P.Root open={isOpen} onOpenChange={setOpen}>
      <P.Trigger asChild>{trigger}</P.Trigger>
      <P.Portal>
        <P.Content align="start" sideOffset={6} className="z-50 w-[min(22rem,calc(100vw-2rem))] rounded-lg border border-rule bg-paper-raised p-3 shadow-raised data-[state=open]:animate-pop">
          {isOpen && (
            <FilterForm
              columns={columns}
              initial={initial}
              onDone={(f) => {
                onDone(f);
                setOpen(false);
              }}
            />
          )}
        </P.Content>
      </P.Portal>
    </P.Root>
  );
}

function FilterForm({ columns, initial, onDone }: { columns: ColumnDetail[]; initial?: Filter; onDone: (f: Filter) => void }) {
  const [col, setCol] = useState(initial?.column ?? columns.find((c) => c.category === "text")?.name ?? columns[0]?.name ?? "");
  const c = columns.find((x) => x.name === col);
  const ops = c ? opsFor(c) : [];
  const [op, setOp] = useState<Filter["op"]>(initial?.op ?? ops[0] ?? "eq");
  const [value, setValue] = useState(initial?.op === "in" ? (initial.values ?? []).join(", ") : (initial?.value ?? ""));
  const opOk = ops.includes(op) ? op : ops[0];
  const needsValue = opOk !== "isNull" && opOk !== "notNull";
  const submit = () => {
    if (!c || !opOk) return;
    const v = value || (c.category === "bool" ? "true" : c.category === "enum" ? (c.enum?.[0] ?? "") : "");
    if (opOk === "in") onDone({ column: col, op: opOk, values: v.split(",").map((s) => s.trim()).filter(Boolean) });
    else if (needsValue) onDone({ column: col, op: opOk, value: v.trim() });
    else onDone({ column: col, op: opOk });
  };
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      className="flex flex-col gap-2.5"
      aria-label="Filter"
    >
      <div className="grid grid-cols-2 gap-2">
        <div className="flex flex-col gap-1 text-xs text-ink-3">
          Column
          <Select size="sm" aria-label="Column" value={col} onValueChange={setCol} className="font-mono" options={columns.map((x) => ({ value: x.name, label: x.name }))} />
        </div>
        <div className="flex flex-col gap-1 text-xs text-ink-3">
          Matches
          <Select size="sm" aria-label="Matches" value={opOk} onValueChange={(v) => setOp(v as Filter["op"])} options={ops.map((o) => ({ value: o, label: OPS[o] }))} />
        </div>
      </div>
      {needsValue && (
        <label className="flex flex-col gap-1 text-xs text-ink-3">
          {opOk === "in" ? "Values, separated by commas" : "Value"}
          {c?.category === "bool" && opOk !== "in" ? (
            <Select
              size="sm"
              aria-label="Value"
              value={value || "true"}
              onValueChange={setValue}
              className="font-mono"
              options={[
                { value: "true", label: "true" },
                { value: "false", label: "false" },
              ]}
            />
          ) : c?.category === "enum" && opOk !== "in" ? (
            <Select size="sm" aria-label="Value" value={value || c.enum?.[0] || ""} onValueChange={setValue} className="font-mono" options={(c.enum ?? []).map((x) => ({ value: x, label: x }))} />
          ) : (
            <Input
              autoFocus
              value={value}
              onChange={(e) => setValue(e.target.value)}
              type={c?.category === "date" && opOk !== "in" ? "date" : "text"}
              inputMode={c?.category === "number" ? "decimal" : undefined}
              className="h-8 font-mono text-sm"
              placeholder={c?.category === "timestamp" ? "2026-10-05 14:00" : ""}
            />
          )}
        </label>
      )}
      <Button type="submit" variant="primary" size="sm" className="self-end">
        {initial ? "Update filter" : "Add filter"}
      </Button>
    </form>
  );
}
