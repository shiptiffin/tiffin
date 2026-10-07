import { useVirtualizer } from "@tanstack/react-virtual";
import { Trash2 } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { cn } from "@/lib/cn";

export type Col = {
  label: string;
  /** CSS grid track, e.g. "minmax(8rem,1fr)". */
  width: string;
  align?: "right";
  /** Muted (an index, an id). */
  quiet?: boolean;
};

export type GridRow = {
  id: string;
  cells: ReactNode[];
  /** The text to edit, per column; undefined where a cell can't be edited. */
  edit?: Array<string | undefined>;
};

/**
 * The values of a hash, list, set, sorted set or stream as a grid (the ARIA
 * grid pattern): arrow keys move between cells, Enter edits a cell and
 * Enter saves it (Shift+Enter for a new line), Escape cancels, Delete
 * removes the row. Saving shows the new value at once and puts the old one
 * back if the box refuses. Rows are virtualized; reaching the end loads
 * the next page.
 */
export function ValueGrid({
  label,
  cols,
  rows,
  onSave,
  onDelete,
  more,
  loadMore,
  deleteLabel,
}: {
  label: string;
  cols: Col[];
  rows: GridRow[];
  onSave?: (row: number, col: number, text: string) => Promise<unknown>;
  onDelete?: (row: number) => void;
  more?: boolean;
  loadMore?: () => void;
  deleteLabel?: (row: number) => string;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const [cell, setCell] = useState<[number, number]>([0, 0]);
  const [editing, setEditing] = useState<{ r: number; c: number; text: string } | null>(null);
  const [pending, setPending] = useState<Record<string, string>>({});
  // Enter and then the blur that follows must save once; Escape not at all.
  const settled = useRef(false);
  const startEdit = (r: number, c: number, text: string) => {
    settled.current = false;
    setEditing({ r, c, text });
  };
  useEffect(() => setPending({}), [rows]);
  // eslint-disable-next-line react-hooks/incompatible-library
  const v = useVirtualizer({ count: rows.length, getScrollElement: () => scroller.current, estimateSize: () => 34, overscan: 10 });
  const items = v.getVirtualItems();
  const lastShown = items[items.length - 1]?.index ?? 0;
  useEffect(() => {
    if (more && loadMore && lastShown >= rows.length - 10) loadMore();
  }, [more, loadMore, lastShown, rows.length]);

  const tracks = [...cols.map((c) => c.width), onDelete ? "2rem" : ""].join(" ");
  const [r0, c0] = [Math.min(cell[0], rows.length - 1), Math.min(cell[1], cols.length - 1)];
  const focusCell = (r: number, c: number) => {
    const rr = Math.max(0, Math.min(rows.length - 1, r));
    const cc = Math.max(0, Math.min(cols.length - 1, c));
    setCell([rr, cc]);
    v.scrollToIndex(rr);
    requestAnimationFrame(() => scroller.current?.querySelector<HTMLElement>(`[data-cell="${rr}:${cc}"]`)?.focus());
  };
  const save = async () => {
    if (!editing || !onSave || settled.current) return;
    settled.current = true;
    const { r, c, text } = editing;
    setEditing(null);
    focusCell(r, c);
    if (text === rows[r].edit?.[c]) return;
    const k = `${rows[r].id}:${c}`;
    setPending((p) => ({ ...p, [k]: text }));
    try {
      await onSave(r, c, text);
    } catch {
      setPending((p) => {
        const n = { ...p };
        delete n[k];
        return n;
      });
    }
  };
  const onKey = (e: React.KeyboardEvent, r: number, c: number) => {
    if (editing) return;
    switch (e.key) {
      case "ArrowDown":
        focusCell(r + 1, c);
        break;
      case "ArrowUp":
        focusCell(r - 1, c);
        break;
      case "ArrowRight":
        focusCell(r, c + 1);
        break;
      case "ArrowLeft":
        focusCell(r, c - 1);
        break;
      case "Home":
        focusCell(e.ctrlKey || e.metaKey ? 0 : r, 0);
        break;
      case "End":
        focusCell(e.ctrlKey || e.metaKey ? rows.length - 1 : r, cols.length - 1);
        break;
      case "PageDown":
        focusCell(r + 10, c);
        break;
      case "PageUp":
        focusCell(r - 10, c);
        break;
      case "Enter":
      case "F2": {
        const t = rows[r].edit?.[c];
        if (t === undefined || !onSave) return;
        startEdit(r, c, pending[`${rows[r].id}:${c}`] ?? t);
        break;
      }
      case "Delete":
      case "Backspace":
        if (!onDelete) return;
        onDelete(r);
        break;
      default:
        return;
    }
    e.preventDefault();
  };

  return (
    <div
      role="grid"
      aria-label={label}
      aria-rowcount={rows.length + 1}
      aria-colcount={cols.length + (onDelete ? 1 : 0)}
      className="overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised"
    >
      <div role="row" aria-rowindex={1} className="grid border-b border-rule-2 bg-paper-sunk" style={{ gridTemplateColumns: tracks }}>
        {cols.map((c) => (
          <div key={c.label} role="columnheader" className={cn("px-3 py-1.5 text-xs text-ink-3", c.align === "right" && "text-right")}>
            {c.label}
          </div>
        ))}
        {onDelete && (
          <div role="columnheader">
            <span className="sr-only">Delete</span>
          </div>
        )}
      </div>
      <div ref={scroller} className="max-h-[min(60vh,34rem)] overflow-auto overscroll-contain">
        <div style={{ height: v.getTotalSize() }} className="relative">
          {items.map((it) => {
            const r = it.index;
            const row = rows[r];
            return (
              <div
                key={row.id}
                role="row"
                aria-rowindex={r + 2}
                data-index={r}
                ref={v.measureElement}
                style={{ transform: `translateY(${it.start}px)`, gridTemplateColumns: tracks }}
                className="group absolute inset-x-0 top-0 grid min-h-[34px] border-b border-rule"
              >
                {cols.map((c, ci) => {
                  const isEditing = editing?.r === r && editing.c === ci;
                  const pend = pending[`${row.id}:${ci}`];
                  const editable = !!onSave && row.edit?.[ci] !== undefined;
                  return (
                    <div
                      key={ci}
                      role="gridcell"
                      data-cell={`${r}:${ci}`}
                      tabIndex={r === r0 && ci === c0 && !editing ? 0 : -1}
                      aria-readonly={!editable || undefined}
                      onKeyDown={(e) => onKey(e, r, ci)}
                      onFocus={() => setCell([r, ci])}
                      onDoubleClick={() => editable && startEdit(r, ci, pend ?? row.edit![ci]!)}
                      title={editable && !isEditing ? "Enter or double-click to edit" : undefined}
                      className={cn(
                        "min-w-0 px-3 py-[7px] font-mono text-[0.78125rem] leading-5 text-ink outline-hidden focus-visible:shadow-[inset_0_0_0_2px_var(--focus)] focus-visible:[border-radius:0]",
                        c.quiet && "text-ink-3",
                        c.align === "right" && "text-right",
                        editable && "cursor-text hover:bg-paper-hover/60",
                        isEditing && "p-1",
                      )}
                    >
                      {isEditing ? (
                        <textarea
                          autoFocus
                          aria-label={`Edit ${c.label}`}
                          value={editing.text}
                          rows={Math.min(8, Math.max(1, editing.text.split("\n").length))}
                          onChange={(e) => setEditing({ ...editing, text: e.target.value })}
                          onFocus={(e) => e.currentTarget.select()}
                          onKeyDown={(e) => {
                            if (e.key === "Enter" && !e.shiftKey) {
                              e.preventDefault();
                              void save();
                            } else if (e.key === "Escape") {
                              e.preventDefault();
                              e.stopPropagation();
                              settled.current = true;
                              setEditing(null);
                              focusCell(r, ci);
                            }
                          }}
                          onBlur={() => void save()}
                          className={cn(
                            "block w-full resize-none rounded-[5px] border border-brass bg-paper px-2 py-[5px] font-mono text-[0.78125rem] leading-5 text-ink shadow-[0_0_0_3px_var(--brass-wash)] outline-hidden",
                            c.align === "right" && "text-right",
                          )}
                        />
                      ) : pend !== undefined ? (
                        <span className="block truncate text-ink-2" aria-busy>
                          {pend}
                        </span>
                      ) : (
                        <span className="block truncate">{row.cells[ci]}</span>
                      )}
                    </div>
                  );
                })}
                {onDelete && (
                  <div role="gridcell" className="grid place-items-center">
                    <button
                      type="button"
                      tabIndex={-1}
                      aria-label={deleteLabel?.(r) ?? "Delete"}
                      title={`${deleteLabel?.(r) ?? "Delete"} (Delete)`}
                      onClick={() => onDelete(r)}
                      className="grid size-6 place-items-center rounded-[5px] text-ink-3 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-danger-wash hover:text-danger max-lg:opacity-60"
                    >
                      <Trash2 className="size-3.5" />
                    </button>
                  </div>
                )}
              </div>
            );
          })}
        </div>
        {rows.length === 0 && <p className="px-3 py-6 text-center text-base text-ink-3">Empty.</p>}
      </div>
      {more && <p className="border-t border-rule px-3 py-1.5 text-xs text-ink-3">Loading more as you scroll…</p>}
    </div>
  );
}
