import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDown, ArrowUp, ArrowUpRight, KeyRound, Link2, Maximize2 } from "lucide-react";
import { memo, useCallback, useEffect, useLayoutEffect, useRef, useState, type ClipboardEvent, type KeyboardEvent, type ReactNode } from "react";
import { Checkbox } from "@/components/ui/choice";
import { cn } from "@/lib/cn";
import { copyText } from "@/lib/clipboard";
import type { Sort } from "./api";
import { cellText, fromDraft, DraftError, isTimestamp, parseTSV, rawText, toDraft } from "./format";

/**
 * The table grid. Rows are virtualized (only what's on screen is in the
 * page), so 50,000 rows scroll as smoothly as 50. It follows the ARIA grid
 * pattern: one cell is focusable at a time; arrows move, Enter edits and
 * saves, Escape cancels, Tab saves and moves right while editing, ⌘C copies,
 * pasting many cells hands them to onPaste. Every value is text: nothing a
 * row holds is rendered as HTML.
 */
export type GridCol = {
  name: string;
  /** Where the value sits in each row. */
  index: number;
  type: string;
  category: string;
  numeric: boolean;
  primary?: boolean;
  nullable?: boolean;
  /** A link to another table's row (a one-column foreign key). */
  link?: { schema: string; table: string; column: string };
  enum?: string[];
  editable: boolean;
  width: number;
};

export type Cell = { r: number; c: number };

const ROW = 32;
const HEAD = 44;
const CHECK = 64;

/** A width that fits the column's kind of value. */
export function widthFor(category: string, name: string, type: string): number {
  const base: Record<string, number> = { number: 112, bool: 88, date: 120, time: 104, timestamp: 184, uuid: 300, json: 260, enum: 128, array: 180, text: 220 };
  const w = base[category] ?? 180;
  return Math.max(w, Math.min(320, 34 + Math.max(name.length, type.length * 0.85) * 7.4));
}

export const DataGrid = memo(function DataGrid({
  cols,
  rows,
  total,
  rowKey,
  labels,
  label,
  selected,
  onSelect,
  pending,
  sort,
  onSort,
  onCommit,
  onEditor,
  onOpenRow,
  onOpenLink,
  onPaste,
  onDelete,
  onEndReached,
  offset = 0,
  className,
  height,
  footer,
}: {
  cols: GridCol[];
  rows: unknown[][];
  total?: number;
  rowKey: (row: unknown[], i: number) => string;
  labels?: Record<string, Record<string, string>>;
  label: string;
  selected?: Set<string>;
  onSelect?: (next: Set<string>) => void;
  pending?: Set<string>;
  sort?: Sort[];
  onSort?: (col: GridCol) => void;
  onCommit?: (r: number, col: GridCol, value: unknown) => void;
  /** Values edited outside the grid: JSON and lists in the row panel, links in the picker. */
  onEditor?: (r: number, col: GridCol) => void;
  onOpenRow?: (r: number) => void;
  onOpenLink?: (col: GridCol, value: unknown) => void;
  onPaste?: (at: Cell, cells: string[][]) => void;
  onDelete?: () => void;
  onEndReached?: () => void;
  offset?: number;
  className?: string;
  /** The grid's height; by default it fills its container. */
  height?: number | string;
  footer?: ReactNode;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const selectable = !!onSelect;
  const [active, setActive] = useState<Cell>({ r: -1, c: 0 });
  const [editing, setEditing] = useState<{ r: number; c: number; draft: string } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const focusNext = useRef(false);
  const anchor = useRef<number | null>(null);
  const minC = selectable ? -1 : 0;
  // The virtualizer re-renders this grid itself on scroll; nothing memoizes its functions.
  // eslint-disable-next-line react-hooks/incompatible-library
  const virt = useVirtualizer({ count: rows.length, getScrollElement: () => scroller.current, estimateSize: () => ROW, overscan: 16, paddingStart: HEAD, scrollPaddingStart: HEAD });
  const items = virt.getVirtualItems();
  const width = (selectable ? CHECK : 56) + cols.reduce((n, c) => n + c.width, 0);
  // An empty last track takes what width is left, so columns keep their size on a wide screen.
  const template = `${selectable ? CHECK : 56}px ${cols.map((c) => `${c.width}px`).join(" ")} minmax(0, 1fr)`;

  const lastIndex = items.length ? items[items.length - 1].index : 0;
  useEffect(() => {
    if (onEndReached && rows.length > 0 && lastIndex >= rows.length - 25) onEndReached();
  }, [lastIndex, rows.length, onEndReached]);

  // Keep the active cell inside the rows when they change.
  const r = Math.min(active.r, rows.length - 1);
  const cur = { r, c: Math.min(active.c, cols.length - 1) };

  const cellId = (c: Cell) => `${label.replace(/\W/g, "")}-${c.r}-${c.c}`;
  const move = useCallback(
    (to: Cell) => {
      const next = { r: Math.max(-1, Math.min(rows.length - 1, to.r)), c: Math.max(minC, Math.min(cols.length - 1, to.c)) };
      setActive(next);
      focusNext.current = true;
      if (next.r >= 0) virt.scrollToIndex(next.r, { align: "auto" });
    },
    [rows.length, cols.length, minC, virt],
  );
  useLayoutEffect(() => {
    if (!focusNext.current) return;
    const el = document.getElementById(cellId(cur));
    if (el) {
      focusNext.current = false;
      el.focus({ preventScroll: true });
      el.scrollIntoView?.({ block: "nearest", inline: "nearest" });
    } else requestAnimationFrame(() => document.getElementById(cellId(cur))?.focus({ preventScroll: true }));
  });

  const toggle = (k: string, range?: boolean, i?: number) => {
    if (!onSelect) return;
    const next = new Set(selected);
    if (range && anchor.current !== null && i !== undefined) {
      const [a, b] = [Math.min(anchor.current, i), Math.max(anchor.current, i)];
      for (let j = a; j <= b; j++) next.add(rowKey(rows[j], j));
    } else if (next.has(k)) next.delete(k);
    else next.add(k);
    if (i !== undefined) anchor.current = i;
    onSelect(next);
  };

  const begin = (at: Cell, draft?: string) => {
    const col = cols[at.c];
    if (!col || at.r < 0 || !onCommit) return;
    const v = rows[at.r]?.[col.index];
    if (!col.editable) return;
    if (col.link || col.category === "json" || col.category === "array") {
      onEditor?.(at.r, col);
      return;
    }
    if (col.category === "bool") {
      onCommit(at.r, col, v === null && !col.nullable ? true : v === true ? false : v === false && col.nullable ? null : true);
      return;
    }
    setErr(null);
    setEditing({ ...at, draft: draft ?? toDraft(v, col.category) });
  };

  const commit = (then?: Cell) => {
    if (!editing) return;
    const col = cols[editing.c];
    const before = rows[editing.r]?.[col.index];
    try {
      const v = fromDraft(editing.draft, col);
      if (editing.draft !== toDraft(before, col.category)) onCommit?.(editing.r, col, v);
      setEditing(null);
      setErr(null);
      if (then) move(then);
      else {
        focusNext.current = true;
      }
    } catch (e) {
      setErr(e instanceof DraftError ? e.message : String(e));
    }
  };

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (editing) return; // the editor handles its own keys
    const meta = e.metaKey || e.ctrlKey;
    const page = Math.max(1, Math.floor((scroller.current?.clientHeight ?? 400) / ROW) - 2);
    const k = e.key;
    const go = (to: Cell) => {
      e.preventDefault();
      move(to);
    };
    if (k === "ArrowDown") return go({ ...cur, r: meta ? rows.length - 1 : cur.r + 1 });
    if (k === "ArrowUp") return go({ ...cur, r: meta ? -1 : cur.r - 1 });
    if (k === "ArrowRight") return go({ ...cur, c: meta ? cols.length - 1 : cur.c + 1 });
    if (k === "ArrowLeft") return go({ ...cur, c: meta ? minC : cur.c - 1 });
    if (k === "Home") return go(meta ? { r: 0, c: minC } : { ...cur, c: minC });
    if (k === "End") return go(meta ? { r: rows.length - 1, c: cols.length - 1 } : { ...cur, c: cols.length - 1 });
    if (k === "PageDown") return go({ ...cur, r: cur.r + page });
    if (k === "PageUp") return go({ ...cur, r: cur.r - page });
    const row = rows[cur.r];
    if (cur.r < 0) {
      if ((k === "Enter" || k === " ") && cur.c === -1 && onSelect) {
        e.preventDefault();
        onSelect(rows.length && rows.every((x, i) => selected?.has(rowKey(x, i))) ? new Set() : new Set(rows.map((x, i) => rowKey(x, i))));
      } else if ((k === "Enter" || k === " ") && cols[cur.c]) {
        e.preventDefault();
        onSort?.(cols[cur.c]);
      }
      return;
    }
    if (!row) return;
    const key = rowKey(row, cur.r);
    if (k === " " && (cur.c === -1 || e.shiftKey)) {
      e.preventDefault();
      toggle(key, e.shiftKey && cur.c === -1, cur.r);
      return;
    }
    if (k === "Enter" && cur.c === -1) {
      e.preventDefault();
      onOpenRow?.(cur.r);
      return;
    }
    if (k === "Enter" && e.altKey) {
      const col = cols[cur.c];
      if (col?.link && row[col.index] !== null) {
        e.preventDefault();
        onOpenLink?.(col, row[col.index]);
      }
      return;
    }
    if (k === "Enter" || k === "F2") {
      e.preventDefault();
      begin(cur);
      return;
    }
    if (k === " " && cols[cur.c]?.category === "bool") {
      e.preventDefault();
      begin(cur);
      return;
    }
    if (k === "Escape" && selected?.size) {
      e.preventDefault();
      onSelect?.(new Set());
      return;
    }
    if ((k === "Backspace" || k === "Delete") && selected?.size && onDelete) {
      e.preventDefault();
      onDelete();
      return;
    }
    if (meta && k.toLowerCase() === "c") {
      e.preventDefault();
      if (selected?.size) {
        const pick = rows.filter((x, i) => selected.has(rowKey(x, i)));
        void copyText([cols.map((c) => c.name).join("\t"), ...pick.map((x) => cols.map((c) => rawText(x[c.index]).replace(/[\t\n]/g, " ")).join("\t"))].join("\n"));
      } else if (cols[cur.c]) void copyText(rawText(row[cols[cur.c].index]));
      return;
    }
    if (meta && k.toLowerCase() === "a" && onSelect) {
      e.preventDefault();
      onSelect(new Set(rows.map((x, i) => rowKey(x, i))));
      return;
    }
    // Typing starts an edit with what was typed, like a spreadsheet.
    const col = cols[cur.c];
    if (!meta && !e.altKey && k.length === 1 && col?.editable && !["bool", "json", "array", "enum"].includes(col.category) && !col.link) {
      e.preventDefault();
      begin(cur, k);
    }
  };

  const onPasteEvent = (e: ClipboardEvent<HTMLDivElement>) => {
    if (editing || !onPaste || cur.r < 0 || cur.c < 0) return;
    const text = e.clipboardData.getData("text/plain");
    if (!text) return;
    e.preventDefault();
    onPaste(cur, parseTSV(text));
  };

  const allSel = selectable && rows.length > 0 && rows.every((x, i) => selected?.has(rowKey(x, i)));
  const someSel = selectable && !allSel && rows.some((x, i) => selected?.has(rowKey(x, i)));
  const sortOf = (c: GridCol) => sort?.find((s) => s.column === c.name);

  return (
    <div className={cn("relative min-h-0", className)} style={{ height: height ?? "100%" }}>
      <div
        ref={scroller}
        role="grid"
        aria-label={label}
        aria-rowcount={(total ?? rows.length) + 1}
        aria-colcount={cols.length + 1}
        aria-multiselectable={selectable || undefined}
        onKeyDown={onKey}
        onPaste={onPasteEvent}
        className="h-full overflow-auto overscroll-contain font-mono text-[0.78125rem] leading-[1.1875rem] focus:outline-none"
      >
        <div style={{ minWidth: width, height: virt.getTotalSize() }} className="relative">
          <div role="rowgroup" className="sticky top-0 z-[2]">
            <div role="row" aria-rowindex={1} className="grid border-b border-rule-2 bg-paper-sunk" style={{ gridTemplateColumns: template, height: HEAD }}>
              {selectable && (
                <div
                  role="columnheader"
                  id={cellId({ r: -1, c: -1 })}
                  tabIndex={cur.r === -1 && cur.c === -1 ? 0 : -1}
                  onFocus={() => setActive({ r: -1, c: -1 })}
                  className="sticky left-0 z-[1] grid place-items-center bg-paper-sunk outline-none focus-visible:shadow-[inset_0_0_0_2px_var(--focus)]"
                >
                  <Checkbox
                    tabIndex={-1}
                    aria-label={allSel ? "Unselect all rows" : "Select all loaded rows"}
                    checked={allSel ? true : someSel ? "indeterminate" : false}
                    onCheckedChange={() => onSelect?.(allSel ? new Set() : new Set(rows.map((x, i) => rowKey(x, i))))}
                  />
                </div>
              )}
              {!selectable && (
                <div role="columnheader" aria-label="Row number" className="flex items-end justify-end px-3 pb-1.5 text-ink-3">
                  #
                </div>
              )}
              {cols.map((c, j) => {
                const s = sortOf(c);
                const on = cur.r === -1 && cur.c === j;
                return (
                  <div
                    key={c.name}
                    role="columnheader"
                    id={cellId({ r: -1, c: j })}
                    aria-colindex={j + 2}
                    aria-sort={s ? (s.desc ? "descending" : "ascending") : undefined}
                    tabIndex={on ? 0 : -1}
                    onFocus={() => setActive({ r: -1, c: j })}
                    onClick={() => {
                      setActive({ r: -1, c: j });
                      onSort?.(c);
                    }}
                    title={onSort ? `Sort by ${c.name}` : undefined}
                    className={cn(
                      "group/h flex min-w-0 flex-col justify-end border-l border-rule px-3 pb-1.5 outline-none select-none focus-visible:shadow-[inset_0_0_0_2px_var(--focus)]",
                      onSort && "cursor-pointer hover:bg-paper-press/60",
                      c.numeric && "items-end text-right",
                    )}
                  >
                    <span className={cn("flex max-w-full items-center gap-1 text-ink", c.numeric && "flex-row-reverse")}>
                      {c.primary && <KeyRound className="size-3 shrink-0 text-brass-ink" aria-label="primary key" />}
                      {c.link && <Link2 className="size-3 shrink-0 text-ink-3" aria-label={`links to ${c.link.table}`} />}
                      <span className="truncate">{c.name}</span>
                      {s && (s.desc ? <ArrowDown className="size-3 shrink-0 text-brass-ink" aria-hidden /> : <ArrowUp className="size-3 shrink-0 text-brass-ink" aria-hidden />)}
                    </span>
                    <span className="block max-w-full truncate text-[0.6875rem] text-ink-3">{c.link ? `→ ${c.link.table}` : c.type}</span>
                  </div>
                );
              })}
            </div>
          </div>
          <div role="rowgroup">
            {items.map((it) => {
              const i = it.index;
              const row = rows[i];
              const k = rowKey(row, i);
              return (
                <GridRow
                  key={k}
                  i={i}
                  top={it.start}
                  row={row}
                  rowKey={k}
                  cols={cols}
                  template={template}
                  width={width}
                  selectable={selectable}
                  selected={!!selected?.has(k)}
                  activeC={cur.r === i ? cur.c : null}
                  editing={editing?.r === i ? editing : null}
                  pending={pending}
                  labels={labels}
                  offset={offset}
                  cellId={cellId}
                  onCell={(c, ev) => {
                    if (ev?.shiftKey && c === -1) toggle(k, true, i);
                    else if (c === -1 && ev) toggle(k, false, i);
                    setActive({ r: i, c });
                  }}
                  onDouble={(c) => (c === -1 ? onOpenRow?.(i) : begin({ r: i, c }))}
                  onDraft={(d) => setEditing((x) => (x ? { ...x, draft: d } : x))}
                  onEditKey={(ev) => {
                    if (ev.key === "Escape") {
                      ev.preventDefault();
                      ev.stopPropagation();
                      setEditing(null);
                      setErr(null);
                      focusNext.current = true;
                    } else if (ev.key === "Enter" && !ev.shiftKey) {
                      ev.preventDefault();
                      commit();
                    } else if (ev.key === "Tab") {
                      ev.preventDefault();
                      commit({ r: i, c: Math.max(0, Math.min(cols.length - 1, (editing?.c ?? 0) + (ev.shiftKey ? -1 : 1))) });
                    }
                  }}
                  onBlurEdit={() => commit()}
                  onOpenLink={onOpenLink}
                  onEditor={(c) => onEditor?.(i, cols[c])}
                  editable={!!onCommit}
                />
              );
            })}
          </div>
        </div>
        {rows.length === 0 && footer}
      </div>
      {err && (
        <p role="alert" className="absolute right-3 bottom-3 z-[3] max-w-sm rounded-md border border-danger-rule bg-danger-wash px-3 py-1.5 font-sans text-sm text-ink">
          {err}
        </p>
      )}
    </div>
  );
});

const GridRow = memo(function GridRow({
  i,
  top,
  row,
  rowKey,
  cols,
  template,
  width,
  selectable,
  selected,
  activeC,
  editing,
  pending,
  labels,
  offset,
  cellId,
  onCell,
  onDouble,
  onDraft,
  onEditKey,
  onBlurEdit,
  onOpenLink,
  onEditor,
  editable,
}: {
  i: number;
  top: number;
  row: unknown[];
  rowKey: string;
  cols: GridCol[];
  template: string;
  width: number;
  selectable: boolean;
  selected: boolean;
  activeC: number | null;
  editing: { r: number; c: number; draft: string } | null;
  pending?: Set<string>;
  labels?: Record<string, Record<string, string>>;
  offset: number;
  cellId: (c: Cell) => string;
  onCell: (c: number, ev?: { shiftKey: boolean }) => void;
  onDouble: (c: number) => void;
  onDraft: (d: string) => void;
  onEditKey: (e: KeyboardEvent<HTMLElement>) => void;
  onBlurEdit: () => void;
  onOpenLink?: (col: GridCol, value: unknown) => void;
  onEditor: (c: number) => void;
  editable: boolean;
}) {
  return (
    <div
      role="row"
      aria-rowindex={i + 2}
      aria-selected={selectable ? selected : undefined}
      className={cn("group/r absolute top-0 left-0 grid border-b border-rule", selected ? "bg-brass-wash/60" : "bg-paper-raised hover:bg-paper-hover/70")}
      style={{ transform: `translateY(${top}px)`, gridTemplateColumns: template, height: ROW, minWidth: width, width: "100%" }}
    >
      {selectable && (
        <div
          role="gridcell"
          id={cellId({ r: i, c: -1 })}
          tabIndex={activeC === -1 ? 0 : -1}
          onMouseDown={(e) => {
            if (e.shiftKey) e.preventDefault();
          }}
          onClick={(e) => onCell(-1, e)}
          onDoubleClick={() => onDouble(-1)}
          className={cn("sticky left-0 z-[1] flex items-center justify-center gap-1.5 outline-none focus-visible:shadow-[inset_0_0_0_2px_var(--focus)]", selected ? "bg-brass-wash" : "bg-inherit")}
        >
          <Checkbox tabIndex={-1} checked={selected} aria-label={`Select row ${offset + i + 1}`} className="pointer-events-none" />
          <button
            type="button"
            tabIndex={-1}
            aria-label={`Open row ${offset + i + 1}`}
            title="Open the row (↵ in this column)"
            onClick={(e) => {
              e.stopPropagation();
              onDouble(-1);
            }}
            className="grid size-5 place-items-center rounded-[4px] text-ink-3 opacity-0 transition-opacity group-hover/r:opacity-100 hover:bg-paper-press hover:text-ink [@media(hover:none)]:opacity-100"
          >
            <Maximize2 className="size-3" />
          </button>
        </div>
      )}
      {!selectable && (
        <div role="rowheader" className="truncate px-3 py-1.5 text-right text-ink-3 tnum">
          {offset + i + 1}
        </div>
      )}
      {cols.map((c, j) => {
        const v = row[c.index];
        const on = activeC === j;
        const isEditing = editing?.c === j;
        const label = c.link && v !== null && v !== undefined ? labels?.[c.name]?.[String(v)] : undefined;
        const busy = pending?.has(`${rowKey}:${c.name}`);
        const text = label ?? cellText(v);
        return (
          <div
            key={c.name}
            role="gridcell"
            id={cellId({ r: i, c: j })}
            aria-colindex={j + 2}
            data-col={c.name}
            tabIndex={on ? 0 : -1}
            aria-readonly={!c.editable || !editable || undefined}
            aria-label={label ? `${label} (${rawText(v)})` : undefined}
            aria-busy={busy || undefined}
            onClick={() => onCell(j)}
            onDoubleClick={() => onDouble(j)}
            title={isTimestamp(v) ? String(v) : text.length > 32 ? text.slice(0, 500) : undefined}
            className={cn(
              "group/c relative flex min-w-0 items-center border-l border-rule px-3 outline-none",
              c.numeric && "justify-end tnum",
              v === null ? "text-ink-3 italic" : typeof v === "object" || c.category === "array" ? "text-ink-2" : "text-ink",
              on && "z-[1] shadow-[inset_0_0_0_2px_var(--focus)]",
              busy && "bg-brass-wash/70",
            )}
          >
            {isEditing ? (
              <Editor col={c} draft={editing!.draft} onDraft={onDraft} onKey={onEditKey} onBlur={onBlurEdit} />
            ) : (
              <>
                <span className="truncate">{text}</span>
                {c.link && v !== null && onOpenLink && (
                  <button
                    type="button"
                    tabIndex={-1}
                    aria-label={`Open ${c.link.table} row ${rawText(v)}`}
                    title={`Open the ${c.link.table} row (⌥↵)`}
                    onClick={(e) => {
                      e.stopPropagation();
                      onOpenLink(c, v);
                    }}
                    className="ml-auto grid size-5 shrink-0 place-items-center rounded-[4px] text-ink-3 opacity-0 transition-opacity group-hover/c:opacity-100 hover:bg-paper-press hover:text-ink"
                  >
                    <ArrowUpRight className="size-3.5" />
                  </button>
                )}
                {(c.category === "json" || c.category === "array") && on && c.editable && editable && (
                  <button
                    type="button"
                    tabIndex={-1}
                    onClick={(e) => {
                      e.stopPropagation();
                      onEditor(j);
                    }}
                    className="ml-auto shrink-0 rounded-[4px] px-1 font-sans text-xs text-ink-3 hover:text-ink"
                  >
                    Edit
                  </button>
                )}
              </>
            )}
          </div>
        );
      })}
    </div>
  );
});

/** The editor inside a cell: a native input of the column's kind, or a select for an enum. */
function Editor({
  col,
  draft,
  onDraft,
  onKey,
  onBlur,
}: {
  col: GridCol;
  draft: string;
  onDraft: (d: string) => void;
  onKey: (e: KeyboardEvent<HTMLElement>) => void;
  onBlur: () => void;
}) {
  const ref = useRef<HTMLInputElement & HTMLSelectElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.focus();
    if (el.tagName === "INPUT" && (el as HTMLInputElement).type === "text" && draft.length > 1) (el as HTMLInputElement).select();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const cls = "absolute inset-0 h-full w-full bg-paper-raised px-2.5 font-mono text-[0.78125rem] text-ink outline-none shadow-[inset_0_0_0_2px_var(--focus)]";
  if (col.category === "enum")
    return (
      <select ref={ref} aria-label={`New ${col.name}`} value={draft} onChange={(e) => onDraft(e.target.value)} onKeyDown={onKey} onBlur={onBlur} className={cls}>
        {col.nullable && <option value="">null</option>}
        {(col.enum ?? []).map((x) => (
          <option key={x} value={x}>
            {x}
          </option>
        ))}
      </select>
    );
  const type = col.category === "date" ? "date" : col.category === "time" ? "time" : col.category === "timestamp" ? "datetime-local" : "text";
  return (
    <input
      ref={ref}
      aria-label={`New ${col.name}`}
      type={type}
      step={type === "time" || type === "datetime-local" ? 1 : undefined}
      inputMode={col.numeric ? "decimal" : undefined}
      value={draft}
      spellCheck={false}
      autoComplete="off"
      onChange={(e) => onDraft(e.target.value)}
      onKeyDown={onKey}
      onBlur={onBlur}
      className={cn(cls, col.numeric && "text-right")}
    />
  );
}
