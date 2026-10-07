import { useQueries } from "@tanstack/react-query";
import { ArrowUpRight, KeyRound, Trash2 } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Breaker } from "@/components/breaker";
import { Select } from "@/components/ui/choice";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";
import { db, type ColumnDetail, type TableDetail } from "./api";
import { cellText, DraftError, fromDraft, rawText, shortType, toDraft } from "./format";
import { LinkPicker } from "./link-picker";
import { Sheet } from "./sheet";

type Link = { schema: string; table: string; column: string };

/** A one-column link from this column to another table, if it has one. */
export function linkOf(t: TableDetail, col: string): Link | undefined {
  const f = t.foreignKeys.find((x) => x.columns.length === 1 && x.columns[0] === col);
  return f ? { schema: f.refSchema, table: f.refTable, column: f.refColumns[0] } : undefined;
}

/** Why a column can't be written here, or "". */
export function fixedWhy(c: ColumnDetail): string {
  if (c.generated) return "Computed from other columns";
  if (c.identity === "always") return "Numbered by Postgres";
  return "";
}

/**
 * One row in full, every column editable by its kind, or an empty row to
 * add. Saving sends only what changed, as one edit (with Undo). Links to
 * this row from other tables are counted at the bottom.
 */
export function RowPanel({
  project,
  branch,
  table,
  row,
  labels,
  readOnly,
  focus,
  onSave,
  onDelete,
  onOpenLink,
  onOpenReferrers,
  onLabel,
  onClose,
}: {
  project: string;
  branch: string;
  table: TableDetail;
  /** The row's values in column order, or null to add a row. */
  row: unknown[] | null;
  labels?: Record<string, Record<string, string>>;
  readOnly?: string;
  /** The column to put the cursor in. */
  focus?: string;
  onSave: (values: Record<string, unknown>) => Promise<boolean>;
  onDelete?: () => void;
  onOpenLink: (link: Link, value: unknown) => void;
  onOpenReferrers: (schema: string, table: string, column: string, value: unknown) => void;
  /** A linked row's label, learnt from the picker, for the grid to show. */
  onLabel?: (column: string, value: unknown, label: string) => void;
  onClose: () => void;
}) {
  const adding = row === null;
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [values, setValues] = useState<Record<string, unknown>>({});
  const [errs, setErrs] = useState<Record<string, string>>({});
  const [picking, setPicking] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [pickedLabels, setPickedLabels] = useState<Record<string, string>>({});
  const idx = (name: string) => table.columns.findIndex((c) => c.name === name);
  const now = (c: ColumnDetail) => (c.name in values ? values[c.name] : adding ? undefined : row![idx(c.name)]);
  const dirty = Object.keys(values).length + Object.keys(drafts).length > 0;
  const pk = table.primaryKey;
  const keyText = row ? pk.map((k) => rawText(row[idx(k)])).join(", ") : "";
  const title = adding ? `New row in ${table.name}` : table.label && row ? rawText(row[idx(table.label)]) || `Row ${keyText}` : `Row ${keyText}`;

  // Rows elsewhere that link here: "Linked from orders · 212 rows".
  const refs = adding ? [] : table.referencedBy.filter((f) => f.columns.length === 1 && f.refColumns.length === 1);
  const counts = useQueries({
    queries: refs.map((f) => {
      const v = row![idx(f.refColumns[0])];
      return {
        queryKey: ["pg-refcount", project, branch, f.schema, f.table, f.columns[0], rawText(v)],
        queryFn: () => db.rows(project, f.schema, f.table, { filters: [{ column: f.columns[0], op: "eq", value: rawText(v) }], limit: 1, count: true, ...(branch ? { branch } : {}) }),
        enabled: v !== null && v !== undefined,
        staleTime: 30_000,
      };
    }),
  });

  const set = (c: ColumnDetail, v: unknown) => {
    setValues((x) => ({ ...x, [c.name]: v }));
    setDrafts(({ [c.name]: _, ...rest }) => rest);
    setErrs(({ [c.name]: _, ...rest }) => rest);
  };
  const setDraft = (c: ColumnDetail, d: string) => {
    setDrafts((x) => ({ ...x, [c.name]: d }));
    setValues(({ [c.name]: _, ...rest }) => rest);
  };

  const save = async () => {
    const out: Record<string, unknown> = { ...values };
    const bad: Record<string, string> = {};
    for (const [name, d] of Object.entries(drafts)) {
      const c = table.columns[idx(name)];
      try {
        const v = fromDraft(d, c);
        if (adding && v === null && d.trim() === "") continue; // left empty: its default
        out[name] = v;
      } catch (e) {
        bad[name] = e instanceof DraftError ? e.message : String(e);
      }
    }
    setErrs(bad);
    if (Object.keys(bad).length) return;
    setBusy(true);
    const ok = await onSave(out);
    setBusy(false);
    if (ok) onClose();
  };

  const field = (c: ColumnDetail): ReactNode => {
    const fixed = fixedWhy(c);
    const v = now(c);
    const id = `rp-${c.name}`;
    const link = linkOf(table, c.name);
    const ro = !!readOnly || !!fixed;
    if (ro) {
      return (
        <p id={id} className={cn("min-h-9 py-2 font-mono text-[0.8125rem] break-words", v === null || v === undefined ? "text-ink-3" : "text-ink-2")}>
          {adding ? fixed || "set by its default" : cellText(v, true)}
        </p>
      );
    }
    if (link) {
      const lab = v !== null && v !== undefined ? (pickedLabels[`${c.name}:${rawText(v)}`] ?? labels?.[c.name]?.[String(v)]) : undefined;
      return (
        <div className="flex min-h-9 items-center gap-2">
          <span className={cn("min-w-0 flex-1 truncate text-base", v === null || v === undefined ? "text-ink-3" : "text-ink")}>
            {v === undefined ? "its default" : v === null ? "nothing" : (lab ?? rawText(v))}
            {lab && <span className="ml-1.5 font-mono text-xs text-ink-3">{rawText(v)}</span>}
          </span>
          <Button id={id} size="sm" variant="secondary" onClick={() => setPicking(c.name)}>
            Change…
          </Button>
          {v !== null && v !== undefined && (
            <Button size="icon-sm" variant="ghost" aria-label={`Open the linked ${link.table} row`} title={`Open the linked ${link.table} row`} onClick={() => onOpenLink(link, v)}>
              <ArrowUpRight />
            </Button>
          )}
        </div>
      );
    }
    if (c.category === "bool") {
      return (
        <div className="flex min-h-9 items-center gap-3">
          <Breaker id={id} label={c.name} state={v === true ? "on" : "off"} onFlip={(n) => set(c, n === "on")} printed={false} />
          <span className="font-mono text-sm text-ink-2">{v === undefined ? "its default" : v === null ? "null" : String(v)}</span>
        </div>
      );
    }
    if (c.category === "enum") {
      return (
        <Select
          id={id}
          value={v === undefined || v === null ? (c.nullable || adding ? "__null" : "") : String(v)}
          onValueChange={(x) => set(c, x === "__null" ? null : x)}
          className="font-mono"
          options={[...(c.nullable || adding ? [{ value: "__null", label: adding && c.default ? "its default" : "null" }] : []), ...(c.enum ?? []).map((x) => ({ value: x, label: x }))]}
        />
      );
    }
    const d = c.name in drafts ? drafts[c.name] : v === undefined ? "" : toDraft(v, c.category);
    const multi = c.category === "json" || c.category === "array" || (c.category === "text" && (d.length > 60 || d.includes("\n")));
    const placeholder = adding
      ? c.identity
        ? "the next number"
        : c.default
          ? `default: ${c.default.replace(/::[\w ]+$/, "")}`
          : c.nullable
            ? "empty"
            : "required"
      : c.nullable
        ? "null"
        : "";
    if (multi)
      return (
        <textarea
          id={id}
          value={d}
          onChange={(e) => setDraft(c, e.target.value)}
          placeholder={c.category === "array" ? "One item per line" : placeholder}
          spellCheck={c.category === "text"}
          rows={Math.min(12, Math.max(3, d.split("\n").length + 1))}
          aria-invalid={!!errs[c.name] || undefined}
          className="block w-full resize-y rounded-md border border-rule bg-paper px-3 py-2 font-mono text-[0.8125rem] leading-5 text-ink outline-none placeholder:text-ink-4 hover:border-rule-2 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger"
        />
      );
    const type = c.category === "date" ? "date" : c.category === "time" ? "time" : c.category === "timestamp" ? "datetime-local" : "text";
    return (
      <Input
        id={id}
        type={type}
        step={type === "time" || type === "datetime-local" ? 1 : undefined}
        inputMode={c.category === "number" ? "decimal" : undefined}
        value={d}
        onChange={(e) => setDraft(c, e.target.value)}
        placeholder={placeholder}
        spellCheck={false}
        aria-invalid={!!errs[c.name] || undefined}
        className={cn("font-mono text-[0.8125rem]", c.category === "number" && "tnum")}
      />
    );
  };

  return (
    <Sheet
      open
      onOpenChange={(o) => !o && onClose()}
      focusId={focus ? `rp-${focus}` : undefined}
      title={title}
      sub={
        adding ? undefined : <>
          <span className="font-mono">{table.name}</span>
          {!adding && pk.length > 0 && (
            <>
              {" "}
              · {pk.join(", ")} <span className="font-mono">{keyText}</span>
            </>
          )}
          {readOnly && <span className="block text-ink-3">{readOnly}</span>}
        </>
      }
      footer={
        readOnly ? undefined : (
          <>
            {!adding && onDelete && (
              <Button variant="danger-quiet" size="sm" onClick={onDelete}>
                <Trash2 />
                Delete row
              </Button>
            )}
            <span className="ml-auto hidden text-xs text-ink-3 sm:inline">
              <kbd className="kbd">⌘</kbd> <kbd className="kbd">↵</kbd> saves
            </span>
            <Button variant="primary" disabled={(!adding && !dirty) || busy} onClick={() => void save()}>
              {busy ? "Saving…" : adding ? "Add row" : "Save changes"}
            </Button>
          </>
        )
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
            e.preventDefault();
            void save();
          }
        }}
        className="flex flex-col gap-4"
      >
        {table.columns.map((c) => (
          <div key={c.name} className="min-w-0">
            <div className="mb-1 flex items-baseline gap-2">
              <label htmlFor={`rp-${c.name}`} className="flex items-center gap-1 font-mono text-[0.8125rem] text-ink">
                {c.primary && <KeyRound className="size-3 text-brass-ink" aria-label="primary key" />}
                {c.name}
              </label>
              <span className="font-mono text-[0.6875rem] text-ink-3">{shortType(c.type)}</span>
              {adding && !c.nullable && !c.default && !c.identity && !c.generated && <span className="text-xs text-ink-3">required</span>}
              {!readOnly && !fixedWhy(c) && c.nullable && now(c) !== null && (now(c) !== undefined || !adding) && (
                <button type="button" onClick={() => set(c, null)} className="ml-auto text-xs text-ink-3 hover:text-ink">
                  Set empty
                </button>
              )}
            </div>
            {field(c)}
            {errs[c.name] && (
              <p role="alert" className="mt-1 text-sm text-danger">
                {errs[c.name]}
              </p>
            )}
            {c.comment && <p className="mt-1 text-xs text-ink-3">{c.comment}</p>}
          </div>
        ))}
        <button type="submit" hidden />
      </form>
      {refs.length > 0 && (
        <div className="mt-6 border-t border-rule pt-4">
          <p className="label mb-1.5">Linked from</p>
          <ul className="divide-y divide-rule">
            {refs.map((f, i) => {
              const n = counts[i]?.data?.count;
              const v = row![idx(f.refColumns[0])];
              return (
                <li key={f.name}>
                  <button
                    type="button"
                    onClick={() => onOpenReferrers(f.schema, f.table, f.columns[0], v)}
                    className="flex w-full items-center gap-2 py-2 text-left text-base text-ink-2 hover:text-ink"
                  >
                    <span className="font-mono text-[0.8125rem] text-ink">{f.table}</span>
                    <span className="text-ink-3">by {f.columns[0]}</span>
                    <span className="ml-auto tnum">{n === undefined ? "" : n === 0 ? "none" : `${int(n)}${counts[i]?.data?.countCapped ? "+" : ""} ${n === 1 ? "row" : "rows"}`}</span>
                    <ArrowUpRight className="size-3.5 text-ink-3" />
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      )}
      {picking &&
        (() => {
          const c = table.columns[idx(picking)];
          const link = linkOf(table, picking)!;
          return (
            <LinkPicker
              project={project}
              branch={branch}
              link={link}
              column={picking}
              current={now(c)}
              nullable={c.nullable}
              onPick={(v, label) => {
                set(c, v);
                if (label) {
                  setPickedLabels((x) => ({ ...x, [`${c.name}:${rawText(v)}`]: label }));
                  onLabel?.(c.name, v, label);
                }
                setPicking(null);
              }}
              onClose={() => setPicking(null)}
            />
          );
        })()}
    </Sheet>
  );
}
