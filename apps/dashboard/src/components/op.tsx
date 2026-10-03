import type { Op } from "@/api/client";
import { cn } from "@/lib/cn";
import { asTier, diffOp, formatValue, opCounts, splitAddress, type FieldDiff } from "@/lib/changes";
import { RiskBadge } from "./risk";

const actionGlyph: Record<string, { g: string; label: string; cls: string }> = {
  create: { g: "+", label: "Create", cls: "text-rev" },
  update: { g: "~", label: "Update", cls: "text-ink-2" },
  delete: { g: "−", label: "Delete", cls: "text-irr" },
};

export function Address({ address, className }: { address: string; className?: string }) {
  const { kind, name } = splitAddress(address);
  return (
    <code className={cn("font-mono text-[0.8125rem]", className)}>
      {name ? (
        <>
          <span className="text-ink-3">{kind}/</span>
          <span className="text-ink">{name}</span>
        </>
      ) : (
        <span className="text-ink">{kind}</span>
      )}
    </code>
  );
}

/** "+3 ~1 −0" in mono; zero counts fade back. */
export function OpCounts({ ops, className }: { ops: Op[] | null | undefined; className?: string }) {
  const c = opCounts(ops);
  const cell = (n: number, g: string, label: string) => (
    <span className={cn(n === 0 ? "text-ink-4" : "text-ink-2")} title={`${n} to ${label}`}>
      {g}
      {n}
    </span>
  );
  return (
    <span
      className={cn("inline-flex gap-1.5 font-mono text-xs tnum", className)}
      aria-label={`${c.create} to create, ${c.update} to update, ${c.delete} to delete`}
    >
      {cell(c.create, "+", "create")}
      {cell(c.update, "~", "update")}
      {cell(c.delete, "−", "delete")}
    </span>
  );
}

export function OpView({ op, compact }: { op: Op; compact?: boolean }) {
  const a = actionGlyph[op.action] ?? actionGlyph.update;
  const tier = asTier(op.risk);
  const rows = diffOp(op);
  return (
    <article
      className={cn(
        "relative rounded-lg border bg-raised",
        tier === "irreversible" ? "border-irr-rule" : tier === "outbound" ? "border-out/35" : "border-rule",
      )}
    >
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 pt-3">
        <span className={cn("grid size-5 place-items-center rounded-[5px] bg-paper-sunk font-mono text-sm leading-none", a.cls)} aria-label={a.label}>
          {a.g}
        </span>
        <Address address={op.address} className="text-md" />
        <span className="sr-only">{a.label}</span>
        <RiskBadge tier={tier} size="sm" className="ml-auto" />
      </header>
      <p className={cn("px-4 pt-1 pl-12 text-base", tier === "irreversible" ? "text-irr" : "text-ink-2")}>{capital(op.reason)}</p>
      {rows.length > 0 && <DiffTable rows={rows} action={op.action} compact={compact} />}
      {rows.length === 0 && <p className="px-4 pt-1 pb-3 pl-12 text-sm text-ink-4">Default settings, nothing else to show.</p>}
    </article>
  );
}

function capital(s: string) {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

function DiffTable({ rows, action, compact }: { rows: FieldDiff[]; action: string; compact?: boolean }) {
  const shown = compact ? rows.filter((r) => r.kind !== "same") : rows;
  const two = action === "update";
  return (
    <div className="mx-3 mt-3 mb-3 overflow-hidden rounded-md border border-rule bg-paper-sunk/70">
      {two && (
        <div className="hidden grid-cols-[minmax(7rem,0.8fr)_1fr_1fr] border-b border-rule px-3 py-1.5 text-2xs font-medium tracking-wider text-ink-3 uppercase sm:grid">
          <span>Field</span>
          <span>Before</span>
          <span>After</span>
        </div>
      )}
      <dl className="divide-y divide-rule/70 font-mono text-[0.78rem] leading-5">
        {shown.map((r) => (
          <div
            key={r.path}
            className={cn(
              "grid gap-x-4 px-3 py-1.5",
              two ? "grid-cols-1 sm:grid-cols-[minmax(7rem,0.8fr)_1fr_1fr]" : "grid-cols-[minmax(7rem,0.8fr)_2fr]",
              r.kind === "same" && "opacity-55",
            )}
          >
            <dt className="truncate text-ink-3" title={r.path}>
              {r.path}
            </dt>
            {two ? (
              <>
                <dd
                  className={cn(
                    "break-all",
                    r.kind === "changed" || r.kind === "removed" ? "text-ink-3 line-through decoration-irr/60" : "text-ink-3",
                  )}
                >
                  <span className="mr-1 text-ink-4 sm:hidden">before</span>
                  {r.kind === "added" ? <span className="text-ink-4 no-underline">unset</span> : formatValue(r.before)}
                </dd>
                <dd className={cn("break-all", r.kind === "same" ? "text-ink-3" : "text-ink")}>
                  <span className="mr-1 text-ink-4 sm:hidden">after</span>
                  {r.kind === "removed" ? <span className="text-ink-4">unset</span> : formatValue(r.after)}
                </dd>
              </>
            ) : (
              <dd className={cn("break-all", action === "delete" ? "text-ink-3 line-through decoration-irr/60" : "text-ink")}>
                {formatValue(action === "delete" ? r.before : r.after)}
              </dd>
            )}
          </div>
        ))}
      </dl>
    </div>
  );
}
