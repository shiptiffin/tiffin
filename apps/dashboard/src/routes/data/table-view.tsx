import { useInfiniteQuery, useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { Download, Lock, MoreHorizontal, Pencil, Play, Plus, Trash2, Unlock } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { Confirm } from "@/components/confirm";
import { HazardDialog } from "@/components/hazard";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from "@/components/ui/dropdown";
import { cn } from "@/lib/cn";
import { int, num } from "@/lib/format";
import { useMe } from "@/lib/me";
import { db, dq, problemToast, slugOf, type ColumnDetail, type EditResult, type Rows, type TableDetail } from "./api";
import { FilterBar } from "./filters";
import { fromDraft, NUMERIC, rawText, shortType, toCSV, DraftError } from "./format";
import { DataGrid, widthFor, type Cell, type GridCol } from "./grid";
import { LinkPicker } from "./link-picker";
import { fixedWhy, linkOf, RowPanel } from "./row-panel";
import { TableForm } from "./table-form";
import { parseView, useDataSearch, useSetSearch, viewSearch, type View } from "./view";

const PAGE = 200;

type Panel = { kind: "row"; key: string; focus?: string } | { kind: "add" } | null;

/** The rows of one table: filter, sort, edit in place, add and delete, with Undo. */
export function TableView({ project, schema, name, branch }: { project: string; schema: string; name: string; branch: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const search = useDataSearch();
  const setSearch = useSetSearch();
  const { can } = useMe();
  const view = useMemo(() => parseView(search), [search]);
  const detail = useQuery(dq.table(project, schema, name, branch || undefined));
  const t = detail.data;
  const [unlocked, setUnlocked] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [pending, setPending] = useState<Set<string>>(new Set());
  const [panel, setPanel] = useState<Panel>(null);
  const [picking, setPicking] = useState<{ key: string; col: GridCol } | null>(null);
  const [paste, setPaste] = useState<PastePlan | null>(null);
  const [deleting, setDeleting] = useState<string[] | null>(null);
  const [editing, setEditing] = useState(false);
  const [dropping, setDropping] = useState(false);
  const [exporting, setExporting] = useState(false);

  const key = ["pg-rows", project, branch, schema, name, search.f ?? "", search.s ?? ""] as const;
  const rows = useInfiniteQuery({
    queryKey: key,
    queryFn: ({ pageParam }) =>
      db.rows(project, schema, name, {
        filters: view.filters,
        sort: view.sort,
        after: pageParam || undefined,
        limit: PAGE,
        count: !pageParam,
        ...(branch ? { branch } : {}),
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
    enabled: !!t,
    placeholderData: (p) => p,
    // Refetching every loaded page of a big table on focus would be heavy; edits patch the pages instead.
    refetchOnWindowFocus: false,
  });
  const all = useMemo(() => (rows.data?.pages ?? []).flatMap((p) => p.rows ?? []), [rows.data]);
  // Labels of linked rows: from each page, and from rows picked in the link picker since.
  const [picked, setPicked] = useState<Record<string, Record<string, string>>>({});
  const labels = useMemo(() => {
    const out: Record<string, Record<string, string>> = {};
    for (const p of rows.data?.pages ?? []) for (const [c, m] of Object.entries(p.labels ?? {})) out[c] = { ...out[c], ...m };
    for (const [c, m] of Object.entries(picked)) out[c] = { ...out[c], ...m };
    return out;
  }, [rows.data, picked]);
  const learn = (col: string, v: unknown, label?: string) => label && setPicked((p) => ({ ...p, [col]: { ...p[col], [rawText(v)]: label } }));
  const count = rows.data?.pages[0]?.count;
  const capped = rows.data?.pages[0]?.countCapped;

  const writer = can("apply:irreversible");
  const managedLock = !!t?.managed && !unlocked;
  const readOnly = !t ? "" : !writer ? "Your key can read this table but not change it." : !t.writable ? "" : managedLock ? "managed" : "";
  const canEdit = !!t && t.writable && !readOnly;

  const idx = useCallback((n: string) => (t?.columns ?? []).findIndex((c) => c.name === n), [t]);
  const pkIdx = useMemo(() => (t?.primaryKey ?? []).map(idx), [t, idx]);
  const rowKey = useCallback((r: unknown[], i: number) => (pkIdx.length ? pkIdx.map((j) => rawText(r[j])).join("\u0000") : `#${i}`), [pkIdx]);
  const keyObj = (r: unknown[]) => Object.fromEntries((t?.primaryKey ?? []).map((k) => [k, r[idx(k)]]));
  const rowName = (r: unknown[]) => {
    const lab = t?.label ? r[idx(t.label)] : null;
    return lab !== null && lab !== undefined && rawText(lab) ? `“${clip(rawText(lab))}”` : `row ${(t?.primaryKey ?? []).map((k) => rawText(r[idx(k)])).join(", ")}`;
  };

  const cols: GridCol[] = useMemo(
    () =>
      (t?.columns ?? [])
        .map((c, i) => ({ c, i }))
        .filter(({ c }) => !view.hidden.includes(c.name))
        .map(({ c, i }) => ({
          name: c.name,
          index: i,
          type: shortType(c.type),
          category: c.category,
          numeric: !(t && linkOf(t, c.name)) && (c.category === "number" || NUMERIC.test(c.baseType)),
          primary: c.primary,
          nullable: c.nullable,
          link: t ? linkOf(t, c.name) : undefined,
          enum: c.enum,
          editable: canEdit && !fixedWhy(c),
          width: t && linkOf(t, c.name) ? Math.max(200, widthFor(c.category, c.name, c.type)) : widthFor(c.category, c.name, c.type),
        })),
    [t, view.hidden, canEdit],
  );

  // ---- writing rows ----

  /** Replaces rows (by key) in every loaded page; drop removes them. */
  const patch = (fresh: unknown[][], drop?: Set<string>) => {
    const byKey = new Map(fresh.map((r) => [rowKey(r, 0), r]));
    qc.setQueryData<InfiniteData<Rows, string>>(key, (d) =>
      d
        ? {
            ...d,
            pages: d.pages.map((p) => ({ ...p, rows: (p.rows ?? []).filter((r) => !drop?.has(rowKey(r, 0))).map((r) => byKey.get(rowKey(r, 0)) ?? r) })),
          }
        : d,
    );
  };
  const prepend = (fresh: unknown[][]) =>
    qc.setQueryData<InfiniteData<Rows, string>>(key, (d) =>
      d ? { ...d, pages: d.pages.map((p, i) => (i === 0 ? { ...p, rows: [...fresh, ...(p.rows ?? [])], count: p.count === undefined ? p.count : p.count + fresh.length } : p)) } : d,
    );
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["pg-rows", project, branch, schema, name] });
    void qc.invalidateQueries({ queryKey: ["pg-table", project, branch, schema, name] });
    void qc.invalidateQueries({ queryKey: ["tables", project] });
    void qc.invalidateQueries({ queryKey: ["pg-edits", project] });
  };

  const undo = async (res: EditResult, what: string) => {
    try {
      const back = await db.undo(project, res.edit.id);
      if (res.edit.kind === "update") patch(back.rows ?? []);
      else if (res.edit.kind === "insert") patch([], new Set((back.rows ?? []).map((r) => rowKey(r, 0))));
      else refresh();
      void qc.invalidateQueries({ queryKey: ["pg-edits", project] });
      toast({ title: `Undone: ${what}` });
    } catch (e) {
      problemToast(e);
    }
  };

  /** Saves changes to rows, optimistically: the grid shows the new values while they save. */
  const update = async (changes: Array<{ row: unknown[]; values: Record<string, unknown> }>, what: string) => {
    if (!t) return false;
    const before = changes.map((c) => c.row);
    const cells = new Set(changes.flatMap((c) => Object.keys(c.values).map((n) => `${rowKey(c.row, 0)}:${n}`)));
    patch(changes.map((c) => c.row.map((v, j) => (t.columns[j].name in c.values ? c.values[t.columns[j].name] : v))));
    setPending((p) => new Set([...p, ...cells]));
    try {
      const res = await db.update(project, schema, name, changes.map((c) => ({ key: keyObj(c.row), values: c.values })), branch || undefined);
      patch(res.rows ?? []);
      void qc.invalidateQueries({ queryKey: ["pg-edits", project] });
      toast({ title: what, action: { label: "Undo", run: () => undo(res, what) } });
      return true;
    } catch (e) {
      patch(before);
      problemToast(e, "That didn't save.");
      return false;
    } finally {
      setPending((p) => new Set([...p].filter((x) => !cells.has(x))));
    }
  };

  const commit = (r: number, col: GridCol, value: unknown) => {
    const row = all[r];
    if (!row) return;
    void update([{ row, values: { [col.name]: value } }], `Saved ${col.name} on ${rowName(row)}`);
  };

  const insert = async (values: Record<string, unknown>) => {
    try {
      const res = await db.insert(project, schema, name, values, branch || undefined);
      prepend(res.rows ?? []);
      void qc.invalidateQueries({ queryKey: ["pg-edits", project] });
      const what = `Added ${res.rows?.[0] ? rowName(res.rows[0]) : "a row"} to ${name}`;
      toast({ title: what, action: { label: "Undo", run: () => undo(res, `added ${name} row`) } });
      return true;
    } catch (e) {
      problemToast(e, "The row wasn't added.");
      return false;
    }
  };

  const remove = async (keys: string[]) => {
    const rs = all.filter((r, i) => keys.includes(rowKey(r, i)));
    if (!rs.length) return;
    try {
      const res = await db.remove(project, schema, name, rs.map(keyObj), branch || undefined);
      patch([], new Set(rs.map((r) => rowKey(r, 0))));
      setSelected(new Set());
      qc.setQueryData<InfiniteData<Rows, string>>(key, (d) => (d ? { ...d, pages: d.pages.map((p, i) => (i === 0 && p.count !== undefined ? { ...p, count: p.count - rs.length } : p)) } : d));
      void qc.invalidateQueries({ queryKey: ["pg-edits", project] });
      const what = rs.length === 1 ? `Deleted ${rowName(rs[0])} from ${name}` : `Deleted ${int(rs.length)} rows from ${name}`;
      toast({ title: what, detail: res.edit.snapshot ? "A restore point was taken first." : undefined, action: res.edit.undoable ? { label: "Undo", run: () => undo(res, what.toLowerCase()) } : undefined });
    } catch (e) {
      problemToast(e, "Nothing was deleted.");
    }
  };

  const askDelete = (keys: string[]) => (keys.length === 1 ? void remove(keys) : setDeleting(keys));

  const onPaste = (at: Cell, cells: string[][]) => {
    if (!canEdit) return;
    const plan = planPaste(all, cols, at, cells);
    if (plan.changes.length === 0) {
      toast({ title: "Nothing to paste there.", detail: plan.skipped ? `${plan.skipped} values went to columns that can't change.` : undefined, tone: "danger" });
      return;
    }
    if (plan.problem) {
      toast({ title: plan.problem, tone: "danger" });
      return;
    }
    if (plan.cells === 1) {
      const c = plan.changes[0];
      void update([c], `Saved ${Object.keys(c.values)[0]} on ${rowName(c.row)}`);
      return;
    }
    setPaste(plan);
  };

  const exportCSV = async () => {
    if (!t) return;
    setExporting(true);
    try {
      const out: unknown[][] = [];
      let after: string | undefined;
      do {
        const page = await db.rows(project, schema, name, { filters: view.filters, sort: view.sort, after, limit: 1000, ...(branch ? { branch } : {}) });
        out.push(...(page.rows ?? []));
        after = page.next || undefined;
      } while (after && out.length < 100_000);
      const csv = toCSV(
        cols.map((c) => c.name),
        out.map((r) => cols.map((c) => r[c.index])),
      );
      const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = `${name}${branch ? `-${branch}` : ""}.csv`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 10_000);
      toast({ title: `Exported ${int(out.length)} ${out.length === 1 ? "row" : "rows"} as ${a.download}`, detail: after ? "Stopped at 100,000 rows; narrow the filter for the rest." : undefined });
    } catch (e) {
      problemToast(e, "The export didn't finish.");
    } finally {
      setExporting(false);
    }
  };

  const openLink = (link: { schema: string; table: string; column: string }, v: unknown) =>
    navigate({
      to: "/projects/$project/data/tables/$table",
      params: { project, table: slugOf({ schema: link.schema, name: link.table }) },
      search: { ...(branch ? { branch } : {}), ...viewSearch({ filters: [{ column: link.column, op: "eq", value: rawText(v) }], sort: [], hidden: [] }) } as never,
    });
  const setView = (v: View) => {
    setSelected(new Set());
    setSearch(viewSearch(v));
  };
  const sortBy = (c: GridCol) => {
    const s = view.sort.find((x) => x.column === c.name);
    const next = !s ? [{ column: c.name, desc: false }] : !s.desc ? [{ column: c.name, desc: true }] : [];
    setView({ ...view, sort: next });
  };

  if (detail.isError) return <ProblemNote error={detail.error} />;
  if (!t)
    return (
      <div className="flex flex-col gap-3">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-[min(32rem,calc(100dvh-22rem))]" />
      </div>
    );

  const panelRow = panel?.kind === "row" ? all.find((r, i) => rowKey(r, i) === panel.key) : undefined;
  const pickRow = picking ? all.find((r, i) => rowKey(r, i) === picking.key) : undefined;
  const filtered = view.filters.length > 0;
  const kindWord = t.kind === "table" ? "" : t.kind === "materialized-view" ? "materialized view" : t.kind;

  return (
    <div className="flex min-w-0 flex-col">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="min-w-0">
          <h2 className="flex items-baseline gap-2.5">
            <span className="truncate font-mono text-[1.0625rem] font-[550] text-ink">{slugOf(t)}</span>
            {kindWord && <span className="text-sm text-ink-3">{kindWord}</span>}
          </h2>
          <p className="mt-0.5 text-sm text-ink-3 tnum" aria-live="polite">
            {filtered && count !== undefined ? (
              <>
                <span className="text-ink-2">
                  {int(count)}
                  {capped ? "+" : ""}
                </span>{" "}
                of {t.rowsExact ? int(t.rows) : `about ${num(t.rows)}`} rows match
              </>
            ) : (
              <>{t.kind === "view" ? `${int(all.length)}${rows.hasNextPage ? "+" : ""} rows` : t.rowsExact ? `${int(t.rows)} ${t.rows === 1 ? "row" : "rows"}` : `about ${num(t.rows)} rows`}</>
            )}
            {selected.size > 0 && <span className="text-ink-2"> · {int(selected.size)} selected</span>}
          </p>
        </div>
        <div className="flex items-center gap-1.5">
          {selected.size > 0 && canEdit && (
            <Button size="sm" variant="danger-quiet" onClick={() => askDelete([...selected])}>
              <Trash2 />
              Delete {selected.size === 1 ? "row" : `${int(selected.size)} rows`}
            </Button>
          )}
          {canEdit && (
            <Button size="sm" variant="secondary" onClick={() => setPanel({ kind: "add" })}>
              <Plus />
              Add row
            </Button>
          )}
          <Menu>
            <MenuTrigger asChild>
              <Button size="icon-sm" variant="ghost" aria-label={`More for ${slugOf(t)}`}>
                <MoreHorizontal />
              </Button>
            </MenuTrigger>
            <MenuContent align="end" className="w-60">
              {t.kind === "table" && writer && !t.managed && (
                <MenuItem onSelect={() => setEditing(true)}>
                  <Pencil />
                  Edit columns…
                </MenuItem>
              )}
              <MenuItem disabled={exporting} onSelect={() => void exportCSV()}>
                <Download />
                {exporting ? "Exporting…" : `Export ${filtered ? "these rows" : "rows"} as CSV`}
              </MenuItem>
              <MenuItem asChild>
                <Link
                  to="/projects/$project/data/sql"
                  params={{ project }}
                  search={{ ...(branch ? { branch } : {}), sql: `SELECT * FROM ${qualified(t)} LIMIT 100;` } as never}
                >
                  <Play />
                  Query it in SQL
                </Link>
              </MenuItem>
              {t.managed && writer && t.writable && (
                <MenuItem onSelect={() => setUnlocked((u) => !u)}>
                  {unlocked ? <Lock /> : <Unlock />}
                  {unlocked ? "Make read-only again" : "Allow editing here"}
                </MenuItem>
              )}
              {writer && !t.managed && (
                <>
                  <MenuSeparator />
                  <MenuItem onSelect={() => setDropping(true)} className="text-danger data-[highlighted]:text-danger [&_svg]:text-danger">
                    <Trash2 />
                    Delete {t.kind === "table" ? "table" : kindWord}…
                  </MenuItem>
                </>
              )}
            </MenuContent>
          </Menu>
        </div>
      </div>

      {t.managed && (
        <p className="mt-3 flex items-start gap-2 rounded-md border border-rule-2 bg-paper-sunk px-3 py-2 text-sm text-ink-2">
          <Lock className="mt-0.5 size-3.5 shrink-0 text-ink-3" aria-hidden />
          <span>
            {t.schema === "auth"
              ? "Sign-in data, kept by Tiffin. Read it freely; change people on the Users page so their sessions stay right."
              : "Kept by Tiffin or a framework your app uses."}{" "}
            {unlocked ? <span className="text-warn-ink">Editing is on for this visit.</span> : "Read-only here."}
          </span>
        </p>
      )}
      {!t.writable && (t.kind === "table" || t.kind === "partitioned") && (
        <p className="mt-3 rounded-md border border-rule-2 bg-paper-sunk px-3 py-2 text-sm text-ink-2">
          This table has no primary key, so its rows can't be told apart to edit one. Add an <code className="font-mono">id</code> column in{" "}
          <Link
            to="/projects/$project/data/sql"
            params={{ project }}
            search={{ ...(branch ? { branch } : {}), sql: `ALTER TABLE ${qualified(t)} ADD COLUMN id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY;` } as never}
            className="text-ink underline decoration-rule-3 underline-offset-4"
          >
            SQL
          </Link>{" "}
          to edit here.
        </p>
      )}

      <div className="mt-3">
        <FilterBar columns={t.columns} view={view} onView={setView} />
      </div>

      {rows.isError && <ProblemNote className="mt-3" error={rows.error} />}
      <div className={cn("mt-3 overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised", rows.isPlaceholderData && "opacity-70")}>
        {rows.isPending ? (
          <Skeleton className="h-[max(20rem,calc(100dvh-22rem))] rounded-none" />
        ) : (
          <DataGrid
            cols={cols}
            rows={all}
            total={count ?? (t.rowsExact ? t.rows : undefined)}
            rowKey={rowKey}
            labels={labels}
            label={`Rows of ${slugOf(t)}`}
            selected={canEdit ? selected : undefined}
            onSelect={canEdit ? setSelected : undefined}
            pending={pending}
            sort={view.sort}
            onSort={sortBy}
            onCommit={canEdit ? commit : undefined}
            onEditor={(r, col) => {
              const k = rowKey(all[r], r);
              if (col.link) setPicking({ key: k, col });
              else setPanel({ kind: "row", key: k, focus: col.name });
            }}
            onOpenRow={(r) => setPanel({ kind: "row", key: rowKey(all[r], r) })}
            onOpenLink={(col, v) => col.link && openLink(col.link, v)}
            onPaste={canEdit ? onPaste : undefined}
            onDelete={canEdit ? () => askDelete([...selected]) : undefined}
            onEndReached={rows.hasNextPage && !rows.isFetchingNextPage ? () => void rows.fetchNextPage() : undefined}
            height="max(20rem, calc(100dvh - 22rem))"
            footer={
              <p className="px-4 py-10 text-center font-sans text-base text-ink-3">
                {filtered ? "No rows match these filters." : "No rows yet."}
                {canEdit && !filtered && (
                  <>
                    {" "}
                    <button type="button" className="text-ink underline decoration-rule-3 underline-offset-4" onClick={() => setPanel({ kind: "add" })}>
                      Add the first one
                    </button>
                    .
                  </>
                )}
              </p>
            }
          />
        )}
      </div>
      <p className="mt-2 hidden text-xs text-ink-3 lg:block">
        {canEdit ? (
          <>
            <kbd className="kbd">↵</kbd> edits and saves · <kbd className="kbd">esc</kbd> cancels · <kbd className="kbd">⌥</kbd>
            <kbd className="kbd">↵</kbd> opens a link · paste cells from a spreadsheet · <kbd className="kbd">?</kbd> all keys
          </>
        ) : (
          <>
            Arrows move · <kbd className="kbd">⌘</kbd>
            <kbd className="kbd">C</kbd> copies · <kbd className="kbd">?</kbd> all keys
          </>
        )}
        {rows.isFetchingNextPage && <span className="ml-2">Loading more…</span>}
      </p>

      {panel && (panel.kind === "add" || panelRow) && (
        <RowPanel
          key={panel.kind === "row" ? panel.key : "add"}
          project={project}
          branch={branch}
          table={t}
          row={panel.kind === "add" ? null : panelRow!}
          labels={labels}
          readOnly={!canEdit ? readOnly === "managed" ? "Kept by Tiffin: read-only here." : readOnly || "This table can't be edited by row." : undefined}
          focus={panel.kind === "row" ? panel.focus : undefined}
          onSave={(values) =>
            panel.kind === "add"
              ? insert(values)
              : Object.keys(values).length === 0
                ? Promise.resolve(true)
                : update([{ row: panelRow!, values }], Object.keys(values).length === 1 ? `Saved ${Object.keys(values)[0]} on ${rowName(panelRow!)}` : `Saved ${Object.keys(values).length} changes to ${rowName(panelRow!)}`)
          }
          onDelete={
            panel.kind === "row" && canEdit
              ? () => {
                  const k = panel.key;
                  setPanel(null);
                  void remove([k]);
                }
              : undefined
          }
          onOpenLink={(link, v) => {
            setPanel(null);
            void openLink(link, v);
          }}
          onOpenReferrers={(s, tb, col, v) => {
            setPanel(null);
            void openLink({ schema: s, table: tb, column: col }, v);
          }}
          onLabel={learn}
          onClose={() => setPanel(null)}
        />
      )}
      {picking && pickRow && picking.col.link && (
        <LinkPicker
          project={project}
          branch={branch}
          link={picking.col.link}
          column={picking.col.name}
          current={pickRow[picking.col.index]}
          nullable={picking.col.nullable}
          onPick={(v, label) => {
            setPicking(null);
            learn(picking.col.name, v, label);
            if (rawText(v) !== rawText(pickRow[picking.col.index]))
              void update([{ row: pickRow, values: { [picking.col.name]: v } }], `Linked ${rowName(pickRow)} to ${v === null ? "nothing" : label ? `“${clip(label)}”` : `${picking.col.link!.table} ${rawText(v)}`}`);
          }}
          onOpen={(v) => {
            setPicking(null);
            void openLink(picking.col.link!, v);
          }}
          onClose={() => setPicking(null)}
        />
      )}
      <PasteDialog
        plan={paste}
        onClose={() => setPaste(null)}
        onApply={async (p) => {
          const ok = await update(p.changes, `Pasted ${int(p.cells)} values into ${p.changes.length === 1 ? rowName(p.changes[0].row) : `${int(p.changes.length)} rows`}`);
          if (ok) setPaste(null);
        }}
      />
      <Confirm
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title={`Delete ${int(deleting?.length ?? 0)} rows from ${name}?`}
        body={
          (deleting?.length ?? 0) > 100
            ? `A restore point of the database is taken first. You can also put them back with Undo for a week.`
            : "Undo puts them back, with their ids, for a week."
        }
        action={`Delete ${int(deleting?.length ?? 0)} rows`}
        run={() => remove(deleting ?? [])}
        done={() => undefined}
      />
      {editing && <TableForm project={project} branch={branch} table={t} onClose={() => setEditing(false)} onDone={refresh} />}
      <HazardDialog<{ loses: string; rows: number }, unknown>
        open={dropping}
        onOpenChange={setDropping}
        title={`Delete ${slugOf(t)}?`}
        word={t.name}
        action={`Delete ${t.name}`}
        run={(confirm) => db.dropTable(project, schema, name, branch || undefined, confirm)}
        renderPreview={(p) => (
          <div className="rounded-lg border border-danger-rule bg-danger-wash px-4 py-3 text-base text-ink">
            <p>
              <span className="font-mono">{slugOf(t)}</span> and its {int(p.rows)} {p.rows === 1 ? "row" : "rows"} are gone.
            </p>
            <p className="mt-2 text-sm text-ink-2">A restore point of the database is taken first, so you can bring it back under Restore points for 7 days.</p>
          </div>
        )}
        onDone={() => {
          toast({ title: `Deleted ${slugOf(t)}`, detail: "A restore point was taken first." });
          void qc.invalidateQueries({ queryKey: ["tables", project] });
          void navigate({ to: "/projects/$project/data", params: { project }, search: (branch ? { branch } : {}) as never });
        }}
      />
    </div>
  );
}

const qualified = (t: Pick<TableDetail, "schema" | "name">) => {
  const id = (s: string) => (/^[a-z_][a-z0-9_]*$/.test(s) ? s : `"${s.replace(/"/g, '""')}"`);
  return t.schema === "public" ? id(t.name) : `${id(t.schema)}.${id(t.name)}`;
};
const clip = (s: string) => (s.length > 40 ? `${s.slice(0, 38)}…` : s);

// ---- pasting many cells ----

type PastePlan = {
  changes: Array<{ row: unknown[]; values: Record<string, unknown> }>;
  cells: number;
  columns: string[];
  skipped: number;
  beyond: number;
  problem?: string;
};

/** Lays pasted cells onto the grid from the active cell: which rows and columns change, what's skipped. */
function planPaste(rows: unknown[][], cols: GridCol[], at: Cell, cells: string[][]): PastePlan {
  const plan: PastePlan = { changes: [], cells: 0, columns: [], skipped: 0, beyond: 0 };
  const used = new Set<string>();
  cells.forEach((line, dr) => {
    const r = at.r + dr;
    const row = rows[r];
    if (!row) {
      plan.beyond++;
      return;
    }
    const values: Record<string, unknown> = {};
    line.forEach((text, dc) => {
      const col = cols[at.c + dc];
      if (!col) return;
      if (!col.editable || col.link) {
        plan.skipped++;
        return;
      }
      try {
        const v = fromDraft(text, col as unknown as ColumnDetail);
        if (rawText(v) === rawText(row[col.index])) return;
        values[col.name] = v;
        used.add(col.name);
        plan.cells++;
      } catch (e) {
        plan.problem = e instanceof DraftError ? `Row ${r + 1}: ${e.message}` : String(e);
      }
    });
    if (Object.keys(values).length) plan.changes.push({ row, values });
  });
  plan.columns = cols.filter((c) => used.has(c.name)).map((c) => c.name);
  return plan;
}

function PasteDialog({ plan, onClose, onApply }: { plan: PastePlan | null; onClose: () => void; onApply: (p: PastePlan) => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open={!!plan} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-md">
        {plan && (
          <>
            <DialogHeader>
              <DialogTitle>
                Paste {int(plan.cells)} values into {plan.changes.length === 1 ? "1 row" : `${int(plan.changes.length)} rows`}?
              </DialogTitle>
              <DialogDescription>
                {plan.columns.length === 1 ? "Column" : "Columns"} <span className="font-mono text-ink">{plan.columns.join(", ")}</span>. It saves as one change, and Undo puts every value back.
              </DialogDescription>
            </DialogHeader>
            {(plan.skipped > 0 || plan.beyond > 0) && (
              <DialogBody>
                <ul className="list-disc space-y-1 pl-5 text-sm text-ink-2">
                  {plan.skipped > 0 && <li>{int(plan.skipped)} values skip columns that can't change here (keys Postgres numbers, computed columns, links).</li>}
                  {plan.beyond > 0 && <li>{int(plan.beyond)} lines go past the rows shown and are left out.</li>}
                </ul>
              </DialogBody>
            )}
            <DialogFooter>
              <Button variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button
                variant="primary"
                autoFocus
                disabled={busy}
                onClick={async () => {
                  setBusy(true);
                  await onApply(plan);
                  setBusy(false);
                }}
              >
                {busy ? "Saving…" : `Paste ${int(plan.cells)} values`}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
