import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ChevronRight, Plus, RotateCcw, Trash2 } from "lucide-react";
import { useEffect, useEffectEvent, useMemo, useState } from "react";
import { ApiError } from "@/api/client";
import { mq } from "@/api/modules";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Checkbox, Select } from "@/components/ui/choice";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { useDebounced } from "@/lib/debounced";
import { int } from "@/lib/format";
import { db, problemToast, slugOf, type ColumnChange, type NewColumn, type TableAlter, type TableCreate, type TableDetail } from "./api";
import { shortType } from "./format";
import { Sheet } from "./sheet";

/** The column types a form offers, in plain words; the SQL name is the subtitle. */
const TYPES: Array<[NonNullable<NewColumn["type"]> | "link", string]> = [
  ["text", "Text"],
  ["integer", "Whole number"],
  ["bigint", "Big whole number"],
  ["numeric", "Decimal number"],
  ["boolean", "Yes or no"],
  ["timestamptz", "Date and time"],
  ["date", "Date"],
  ["uuid", "Random ID (UUID)"],
  ["jsonb", "JSON"],
  ["text[]", "List of text"],
  ["link", "Link to another table"],
];

type DefaultKind = "" | NonNullable<NewColumn["default"]>["kind"];
const DEFAULTS: Record<string, Array<[DefaultKind, string]>> = {
  timestamptz: [["now", "the time it's added"]],
  date: [["today", "the day it's added"]],
  uuid: [["random-uuid", "a new random ID"]],
  "text[]": [["empty-list", "an empty list"]],
  jsonb: [["empty-object", "{} (empty)"]],
};

type Draft = {
  id: number;
  name: string;
  type: NonNullable<NewColumn["type"]> | "link";
  nullable: boolean;
  unique: boolean;
  def: DefaultKind;
  value: string;
  ref: string; // "schema.table" for links
  onDelete: NonNullable<NonNullable<NewColumn["references"]>["onDelete"]>;
};
type Existing = { name: string; to: string; type: string; nullable: boolean; unique: boolean; drop: boolean; def: string; defKind: "keep" | "none" | "value"; defValue: string; primary: boolean; fixed: boolean };

let seq = 0;
const blank = (): Draft => ({ id: ++seq, name: "", type: "text", nullable: true, unique: false, def: "", value: "", ref: "", onDelete: "no-action" });

function toNew(d: Draft): NewColumn {
  const c: NewColumn = { name: d.name.trim(), nullable: d.nullable, unique: d.unique || undefined };
  if (d.type === "link") {
    const [schema, ...rest] = d.ref.split(".");
    c.references = { schema, table: rest.join("."), onDelete: d.onDelete };
  } else c.type = d.type;
  if (d.def === "value") c.default = { kind: "value", value: d.value };
  else if (d.def) c.default = { kind: d.def };
  return c;
}

/**
 * Make a table, or change one's columns: a form, with the SQL it will run
 * under Details. Dropping columns asks first, naming the rows that lose a value.
 */
export function TableForm({
  project,
  branch,
  table,
  onClose,
  onDone,
}: {
  project: string;
  branch: string;
  table?: TableDetail;
  onClose: () => void;
  onDone?: () => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const tables = useQuery(mq.tables(project, branch || undefined));
  const [name, setName] = useState(table?.name ?? "");
  const [pk, setPk] = useState<NonNullable<TableCreate["primaryKey"]>>("bigint-identity");
  const [cols, setCols] = useState<Draft[]>(() => (table ? [] : [{ ...blank(), name: "name", nullable: false }, { ...blank(), name: "created_at", type: "timestamptz", nullable: false, def: "now" }]));
  const [existing, setExisting] = useState<Existing[]>(() =>
    (table?.columns ?? []).map((c) => ({
      name: c.name,
      to: c.name,
      type: shortType(c.type),
      nullable: c.nullable,
      unique: !!c.unique,
      drop: false,
      def: c.default ?? "",
      defKind: "keep",
      defValue: "",
      primary: !!c.primary,
      fixed: !!c.generated || !!c.identity,
    })),
  );
  const [confirm, setConfirm] = useState<{ value: string; loses: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const linkTargets = (tables.data ?? []).filter((t) => t.kind === "table" && t.columns?.some((c) => c.primary) && !(table && t.schema === table.schema && t.name === table.name));

  const body: TableCreate | TableAlter | null = useMemo(() => {
    const add = cols.filter((c) => c.name.trim()).map(toNew);
    if (!table) return { name: name.trim(), primaryKey: pk, columns: add, ...(branch ? { branch } : {}) } as TableCreate;
    const alter: TableAlter = { ...(branch ? { branch } : {}) };
    if (name.trim() && name.trim() !== table.name) alter.rename = name.trim();
    if (add.length) alter.add = add;
    const renames = existing.filter((e) => !e.drop && e.to.trim() && e.to.trim() !== e.name).map((e) => ({ from: e.name, to: e.to.trim() }));
    if (renames.length) alter.renameColumns = renames;
    const drops = existing.filter((e) => e.drop).map((e) => e.name);
    if (drops.length) alter.dropColumns = drops;
    const change: ColumnChange[] = [];
    for (const e of existing) {
      const was = table.columns.find((c) => c.name === e.name)!;
      if (e.drop) continue;
      const ch: ColumnChange = { column: e.name };
      if (e.nullable !== was.nullable) ch.nullable = e.nullable;
      if (e.unique !== !!was.unique) ch.unique = e.unique;
      if (e.defKind === "none" && was.default) ch.dropDefault = true;
      if (e.defKind === "value") ch.default = { kind: "value", value: e.defValue };
      if (Object.keys(ch).length > 1) change.push(ch);
    }
    if (change.length) alter.change = change;
    return Object.keys(alter).filter((k) => k !== "branch").length ? alter : null;
  }, [cols, existing, name, pk, table, branch]);

  // The SQL it will run, for Details.
  const shown = useDebounced(body, 350);
  const ready = !!shown && (table ? true : !!(shown as TableCreate).name);
  const sql = useQuery({
    queryKey: ["pg-ddl", project, JSON.stringify(shown)],
    queryFn: () => (table ? db.alterTable(project, table.schema, table.name, { ...(shown as TableAlter), dryRun: true }) : db.createTable(project, { ...(shown as TableCreate), dryRun: true })),
    enabled: ready,
    retry: false,
    placeholderData: (p) => p,
  });

  const submit = async (confirmValue?: string) => {
    if (!body) return;
    setBusy(true);
    setErr(null);
    try {
      if (!table) {
        const res = await db.createTable(project, body as TableCreate);
        void qc.invalidateQueries({ queryKey: ["tables", project] });
        const slug = slugOf({ schema: res.schema, name: res.table });
        toast({
          title: `Made the table ${slug}`,
          action: {
            label: "Undo",
            run: async () => {
              try {
                await db.dropTable(project, res.schema, res.table, branch || undefined);
              } catch (e) {
                if (!(e instanceof ApiError && e.status === 428 && e.problem.confirm)) return problemToast(e);
                await db.dropTable(project, res.schema, res.table, branch || undefined, e.problem.confirm);
              }
              void qc.invalidateQueries({ queryKey: ["tables", project] });
              void navigate({ to: "/projects/$project/data", params: { project }, search: (branch ? { branch } : {}) as never });
            },
          },
        });
        onClose();
        void navigate({ to: "/projects/$project/data/tables/$table", params: { project, table: slug }, search: (branch ? { branch } : {}) as never });
      } else {
        const res = await db.alterTable(project, table.schema, table.name, { ...(body as TableAlter), ...(confirmValue ? { confirm: confirmValue } : {}) });
        void qc.invalidateQueries({ queryKey: ["tables", project] });
        void qc.invalidateQueries({ queryKey: ["pg-table", project] });
        onDone?.();
        toast({ title: `Changed ${slugOf(table)}`, detail: res.snapshot ? "A restore point was taken first." : undefined });
        onClose();
        if (res.table !== table.name)
          void navigate({ to: "/projects/$project/data/tables/$table", params: { project, table: slugOf({ schema: res.schema, name: res.table }) }, search: (branch ? { branch } : {}) as never });
      }
    } catch (e) {
      if (e instanceof ApiError && e.status === 428 && e.problem.confirm) {
        const p = e.problem.preview as { loses?: string } | undefined;
        setConfirm({ value: e.problem.confirm, loses: p?.loses ?? "Some values are gone for good." });
      } else setErr(e);
    } finally {
      setBusy(false);
    }
  };

  const drops = existing.filter((e) => e.drop);
  const nameOk = name.trim().length > 0;
  const colsOk = cols.every((c) => !c.name.trim() || c.type !== "link" || c.ref);

  return (
    <Sheet
      open
      wide
      onOpenChange={(o) => !o && onClose()}
      title={table ? `Columns of ${slugOf(table)}` : "New table"}
      sub={table ? `${table.rowsExact ? int(table.rows) : `about ${int(table.rows)}`} rows` : branch ? `In the copy ${branch}` : "In your database"}
      footer={
        confirm ? (
          <div className="flex w-full flex-col gap-2 sm:flex-row sm:items-center">
            <p className="min-w-0 flex-1 text-sm text-danger">
              {confirm.loses.charAt(0).toUpperCase() + confirm.loses.slice(1)}.
            </p>
            <Button variant="ghost" onClick={() => setConfirm(null)}>
              Keep them
            </Button>
            <Button variant="danger" disabled={busy} onClick={() => void submit(confirm.value)}>
              <Trash2 />
              Drop {drops.length === 1 ? drops[0].name : `${drops.length} columns`} and save
            </Button>
          </div>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose} className="ml-auto">
              Cancel
            </Button>
            <Button variant="primary" disabled={!body || !nameOk || !colsOk || busy} onClick={() => void submit()}>
              {busy ? "Saving…" : table ? "Save changes" : `Create ${name.trim() || "table"}`}
            </Button>
          </>
        )
      }
    >
      <div className="flex flex-col gap-5">
        <div className="flex flex-col gap-1.5">
          <label htmlFor="tf-name" className="text-sm font-medium text-ink">
            Name
          </label>
          <Input
            id="tf-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="reviews"
            className="font-mono"
            autoFocus={!table}
            spellCheck={false}
            autoComplete="off"
            aria-describedby="tf-name-hint"
          />
          <span id="tf-name-hint" className="text-xs text-ink-3">
            Lowercase with underscores reads best in SQL: order_items.
          </span>
        </div>

        {!table && (
          <fieldset className="flex flex-col gap-1.5">
            <legend className="mb-1.5 text-sm font-medium text-ink">Each row's id</legend>
            {(
              [
                ["bigint-identity", "A number Postgres counts up", "1, 2, 3… Short and easy to read."],
                ["uuid", "A random ID", "Can't be guessed; good for things in URLs."],
              ] as const
            ).map(([v, label, sub]) => (
              <label key={v} className={cn("flex cursor-pointer items-start gap-3 rounded-md border px-3 py-2.5", pk === v ? "border-ink bg-paper" : "border-rule-2 hover:border-rule-3")}>
                <input type="radio" name="pk" value={v} checked={pk === v} onChange={() => setPk(v)} className="mt-1 accent-[var(--ink)]" />
                <span>
                  <span className="block text-base text-ink">{label}</span>
                  <span className="block text-sm text-ink-3">
                    {sub} <span className="font-mono text-ink-3">{v === "uuid" ? "uuid" : "bigint identity"}</span>
                  </span>
                </span>
              </label>
            ))}
          </fieldset>
        )}

        {table && (
          <section aria-labelledby="tf-existing">
            <h3 id="tf-existing" className="label mb-2">
              Columns now
            </h3>
            <ul className="divide-y divide-rule border-y border-rule">
              {existing.map((e, i) => {
                const upd = (p: Partial<Existing>) => setExisting((xs) => xs.map((x, j) => (j === i ? { ...x, ...p } : x)));
                return (
                  <li key={e.name} className={cn("flex flex-col gap-2 py-2.5", e.drop && "opacity-60")}>
                    <div className="flex items-center gap-2">
                      <Input
                        aria-label={`Name of ${e.name}`}
                        value={e.to}
                        disabled={e.drop}
                        onChange={(ev) => upd({ to: ev.target.value })}
                        className={cn("h-8 max-w-[14rem] font-mono text-sm", e.drop && "line-through")}
                        spellCheck={false}
                      />
                      <span className="min-w-0 flex-1 truncate font-mono text-xs text-ink-3">{e.type}</span>
                      {e.primary ? (
                        <span className="text-xs text-ink-3">the key</span>
                      ) : (
                        <Button size="sm" variant={e.drop ? "secondary" : "ghost"} onClick={() => upd({ drop: !e.drop })} aria-label={e.drop ? `Keep ${e.name}` : `Drop ${e.name}`}>
                          {e.drop ? <RotateCcw /> : <Trash2 />}
                          {e.drop ? "Keep" : "Drop"}
                        </Button>
                      )}
                    </div>
                    {!e.drop && !e.primary && (
                      <div className="flex flex-wrap items-center gap-x-5 gap-y-2 pl-0.5 text-sm text-ink-2">
                        <label className="flex items-center gap-2">
                          <Checkbox checked={e.nullable} onCheckedChange={(v) => upd({ nullable: v === true })} disabled={e.fixed} />
                          Can be empty
                        </label>
                        <label className="flex items-center gap-2">
                          <Checkbox checked={e.unique} onCheckedChange={(v) => upd({ unique: v === true })} />
                          Unique
                        </label>
                        <div className="flex items-center gap-2">
                          Default
                          <Select
                            size="sm"
                            aria-label={`Default for ${e.name}`}
                            value={e.defKind}
                            onValueChange={(v) => upd({ defKind: v as Existing["defKind"] })}
                            disabled={e.fixed}
                            className="w-auto min-w-[8rem]"
                            options={[
                              { value: "keep", label: e.def ? clipDef(e.def) : "none" },
                              ...(e.def ? [{ value: "none", label: "none" }] : []),
                              { value: "value", label: "a value…" },
                            ]}
                          />
                        </div>
                        {e.defKind === "value" && <Input aria-label={`Default for ${e.name}`} value={e.defValue} onChange={(ev) => upd({ defValue: ev.target.value })} className="h-7 w-36 font-mono text-sm" />}
                      </div>
                    )}
                  </li>
                );
              })}
            </ul>
          </section>
        )}

        <section aria-labelledby="tf-new">
          <h3 id="tf-new" className="label mb-2">
            {table ? "New columns" : "Columns"}
          </h3>
          {!table && <p className="mb-2 text-sm text-ink-3">The id column comes first on its own.</p>}
          <ul className="divide-y divide-rule border-y border-rule">
            {cols.map((c, i) => (
              <NewColumnRow
                key={c.id}
                c={c}
                targets={linkTargets.map((t) => `${t.schema}.${t.name}`)}
                onChange={(p) => setCols((xs) => xs.map((x, j) => (j === i ? { ...x, ...p } : x)))}
                onRemove={() => setCols((xs) => xs.filter((_, j) => j !== i))}
              />
            ))}
          </ul>
          <Button size="sm" variant="ghost" className="mt-2" onClick={() => setCols((xs) => [...xs, blank()])}>
            <Plus />
            Add a column
          </Button>
        </section>

        {err ? <ProblemNote error={err} /> : null}
        {sql.isError && ready && !err && <ProblemNote error={sql.error} />}

        <details className="group/sql">
          <summary className="flex cursor-pointer list-none items-center gap-1.5 text-sm text-ink-3 hover:text-ink [&::-webkit-details-marker]:hidden">
            <ChevronRight className="size-3.5 transition-transform group-open/sql:rotate-90" />
            Details: the SQL this runs
          </summary>
          <pre className="mt-2 overflow-x-auto rounded-md border border-rule bg-paper-sunk px-3 py-2.5 font-mono text-[0.75rem] leading-5 whitespace-pre text-ink-2">
            {sql.data?.sql ?? (ready ? "…" : "Fill in the name to see it.")}
          </pre>
        </details>
      </div>
    </Sheet>
  );
}

const clipDef = (d: string) => {
  const s = d.replace(/::[\w ]+(\[\])?$/, "");
  return s.length > 24 ? `${s.slice(0, 22)}…` : s;
};

function NewColumnRow({ c, targets, onChange, onRemove }: { c: Draft; targets: string[]; onChange: (p: Partial<Draft>) => void; onRemove: () => void }) {
  const defaults = c.type === "link" ? [] : (DEFAULTS[c.type] ?? []);
  // A new link column points at the first table until one is picked; only a type change triggers it.
  const linkFirst = useEffectEvent((type: string) => {
    if (type === "link" && !c.ref && targets[0]) onChange({ ref: targets[0] });
  });
  useEffect(() => linkFirst(c.type), [c.type]);
  return (
    <li className="py-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input aria-label="Column name" value={c.name} onChange={(e) => onChange({ name: e.target.value })} placeholder="column_name" className="h-8 max-w-[15rem] min-w-0 flex-1 font-mono text-sm" spellCheck={false} autoComplete="off" />
        <Select
          size="sm"
          aria-label="Type"
          value={c.type}
          onValueChange={(v) => onChange({ type: v as Draft["type"], def: "" })}
          className="w-auto min-w-[11rem]"
          options={TYPES.map(([v, label]) => ({ value: v, label, disabled: v === "link" && targets.length === 0 }))}
        />
        <Button size="icon-sm" variant="ghost" className="ml-auto" aria-label={`Remove ${c.name || "this column"}`} onClick={onRemove}>
          <Trash2 />
        </Button>
      </div>
      {c.type === "link" && (
        <div className="mt-2 flex flex-wrap items-center gap-2 text-sm text-ink-2">
          to
          <Select
            size="sm"
            aria-label="Linked table"
            value={c.ref}
            onValueChange={(v) => onChange({ ref: v })}
            className="w-auto min-w-[10rem] font-mono"
            options={targets.map((t) => ({ value: t, label: t.replace(/^public\./, "") }))}
          />
          when that row is deleted
          <Select
            size="sm"
            aria-label="When the linked row is deleted"
            value={c.onDelete}
            onValueChange={(v) => onChange({ onDelete: v as Draft["onDelete"] })}
            className="w-44"
            options={[
              { value: "no-action", label: "refuse" },
              { value: "cascade", label: "delete this row too" },
              { value: "set-null", label: "empty the link" },
            ]}
          />
        </div>
      )}
      <div className="mt-2 flex flex-wrap items-center gap-x-5 gap-y-2 text-sm text-ink-2">
        <label className="flex items-center gap-2">
          <Checkbox checked={c.nullable} onCheckedChange={(v) => onChange({ nullable: v === true })} />
          Can be empty
        </label>
        <label className="flex items-center gap-2">
          <Checkbox checked={c.unique} onCheckedChange={(v) => onChange({ unique: v === true })} />
          Unique
        </label>
        {c.type !== "link" && (
          <div className="flex items-center gap-2">
            Default
            <Select
              size="sm"
              aria-label="Default"
              value={c.def || "__none"}
              onValueChange={(v) => onChange({ def: (v === "__none" ? "" : v) as DefaultKind })}
              className="w-auto min-w-[8rem]"
              options={[{ value: "__none", label: "none" }, ...defaults.map(([v, label]) => ({ value: v, label })), { value: "value", label: "a value…" }]}
            />
          </div>
        )}
        {c.def === "value" && (
          <Input aria-label={`Default for ${c.name || "the column"}`} value={c.value} onChange={(e) => onChange({ value: e.target.value })} placeholder={c.type === "boolean" ? "false" : "0"} className="h-7 w-36 font-mono text-sm" />
        )}
      </div>
    </li>
  );
}
