import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDown, ArrowUp, Download, File, FileAudio, FileImage, FileText, FileVideo, Folder, Link2, Trash2 } from "lucide-react";
import { forwardRef, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import type { StorageObject } from "@/api/modules";
import { Checkbox } from "@/components/ui/choice";
import { cn } from "@/lib/cn";
import { bytes } from "@/lib/format";
import { full, relative } from "@/lib/time";
import { kindOf, type Kind } from "./words";

export type Entry = { id: string; kind: "folder"; prefix: string; name: string } | { id: string; kind: "file"; o: StorageObject; name: string };
export type SortBy = "name" | "size" | "modified";
export type Sort = { by: SortBy; dir: "asc" | "desc" };
export type View = "list" | "grid";
export type BrowserHandle = { focus: () => void };

const ICONS: Record<Kind, typeof File> = { image: FileImage, video: FileVideo, audio: FileAudio, pdf: FileText, text: FileText, file: File };

export function FileGlyph({ name, className }: { name: string; className?: string }) {
  const C = ICONS[kindOf(name)];
  return <C aria-hidden className={cn("size-4 shrink-0 text-ink-3", className)} />;
}

const listCols = "grid-cols-[minmax(0,1fr)_4.5rem] @lg:grid-cols-[minmax(0,1fr)_5.5rem_8rem] @2xl:grid-cols-[minmax(0,1fr)_6rem_9rem_7rem]";
const ROW = 40;
const TILE = 176; // tile height, image and two lines

/**
 * The files of one folder, as rows or as tiles with thumbnails, in one
 * virtualized grid so 10,000 files scroll like 10. Keyboard: the ARIA grid
 * pattern with one tab stop (arrows move, Home/End, Page Up/Down), Enter
 * opens, Space selects, Shift with arrows extends the selection, ⌘A selects
 * all, Delete deletes, F2 renames, Alt+↑ goes up a folder.
 */
export const Browser = forwardRef<
  BrowserHandle,
  {
    label: string;
    entries: Entry[];
    view: View;
    sort: Sort;
    onSort: (s: Sort) => void;
    selected: Set<string>;
    onSelect: (s: Set<string>) => void;
    active?: string;
    onOpen: (e: Entry) => void;
    onUp?: () => void;
    onDelete?: (ids: string[]) => void;
    onRename?: (e: Entry) => void;
    onLink: (e: Entry) => void;
    download: (key: string) => string;
    thumb: (o: StorageObject, w: number) => string | undefined;
    className?: string;
    footer?: ReactNode;
  }
>(function Browser(props, ref) {
  const {
    label,
    entries,
    view,
    sort,
    onSort,
    selected,
    onSelect,
    active,
    onOpen,
    onUp,
    onDelete,
    onRename,
    onLink,
    download,
    thumb,
    className,
    footer,
  } = props;
  const scroller = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(800);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);
  const cols = view === "grid" ? Math.max(2, Math.floor((width + 12) / 172)) : 1;
  const rows = Math.ceil(entries.length / cols);
  // eslint-disable-next-line react-hooks/incompatible-library
  const v = useVirtualizer({
    count: rows,
    getScrollElement: () => scroller.current,
    estimateSize: () => (view === "grid" ? TILE + 12 : ROW),
    overscan: 8,
  });
  useEffect(() => v.measure(), [view, cols, v]);

  const [focus, setFocus] = useState(0);
  const [anchor, setAnchor] = useState<number | null>(null);
  const at = Math.min(focus, Math.max(0, entries.length - 1));
  useImperativeHandle(ref, () => ({ focus: () => move(at) }));

  const move = (i: number, extend = false) => {
    const n = Math.max(0, Math.min(entries.length - 1, i));
    if (extend) {
      const from = anchor ?? at;
      setAnchor(from);
      const [a, b] = from < n ? [from, n] : [n, from];
      onSelect(new Set(entries.slice(a, b + 1).map((e) => e.id)));
    } else setAnchor(null);
    setFocus(n);
    v.scrollToIndex(Math.floor(n / cols));
    requestAnimationFrame(() => scroller.current?.querySelector<HTMLElement>(`[data-cell="${n}"]`)?.focus());
  };
  const toggle = (id: string) => {
    const s = new Set(selected);
    if (s.has(id)) s.delete(id);
    else s.add(id);
    onSelect(s);
  };
  const click = (e: React.MouseEvent, i: number) => {
    const x = entries[i];
    setFocus(i);
    if (e.shiftKey) {
      const from = anchor ?? at;
      setAnchor(from);
      const [a, b] = from < i ? [from, i] : [i, from];
      onSelect(new Set(entries.slice(a, b + 1).map((y) => y.id)));
    } else if (e.metaKey || e.ctrlKey) {
      setAnchor(i);
      toggle(x.id);
    } else {
      setAnchor(i);
      onOpen(x);
    }
  };
  const page = Math.max(1, Math.floor((scroller.current?.clientHeight ?? 400) / (view === "grid" ? TILE : ROW)) - 1) * cols;
  const onKey = (e: React.KeyboardEvent, i: number) => {
    const x = entries[i];
    const step = view === "grid" ? cols : 1;
    switch (e.key) {
      case "ArrowDown":
        move(i + step, e.shiftKey);
        break;
      case "ArrowUp":
        if (e.altKey || e.metaKey) {
          if (!onUp) return;
          onUp();
        } else move(i - step, e.shiftKey);
        break;
      case "ArrowRight":
        if (view !== "grid") return;
        move(i + 1, e.shiftKey);
        break;
      case "ArrowLeft":
        if (view !== "grid") return;
        move(i - 1, e.shiftKey);
        break;
      case "Home":
        move(0, e.shiftKey);
        break;
      case "End":
        move(entries.length - 1, e.shiftKey);
        break;
      case "PageDown":
        move(i + page, e.shiftKey);
        break;
      case "PageUp":
        move(i - page, e.shiftKey);
        break;
      case "Enter":
        onOpen(x);
        break;
      case " ":
        toggle(x.id);
        setAnchor(i);
        break;
      case "a":
        if (!(e.metaKey || e.ctrlKey)) return;
        onSelect(new Set(entries.map((y) => y.id)));
        break;
      case "Escape":
        if (selected.size === 0) return;
        onSelect(new Set());
        break;
      case "Delete":
      case "Backspace":
        if (!onDelete) return;
        onDelete(selected.size > 0 && selected.has(x.id) ? [...selected] : [x.id]);
        break;
      case "F2":
        if (!onRename) return;
        onRename(x);
        break;
      default:
        return;
    }
    e.preventDefault();
    e.stopPropagation();
  };

  const allOn = entries.length > 0 && entries.every((x) => selected.has(x.id));
  const someOn = !allOn && entries.some((x) => selected.has(x.id));
  const header = (by: SortBy, text: string, cls?: string) => {
    const on = sort.by === by;
    const Arrow = sort.dir === "asc" ? ArrowUp : ArrowDown;
    return (
      <span role="columnheader" aria-sort={on ? (sort.dir === "asc" ? "ascending" : "descending") : "none"} className={cls}>
        <button
          type="button"
          tabIndex={-1}
          onClick={() => onSort({ by, dir: on ? (sort.dir === "asc" ? "desc" : "asc") : by === "name" ? "asc" : "desc" })}
          className={cn("inline-flex items-center gap-1 label hover:text-ink", on && "text-ink-2")}
        >
          {text}
          {on && <Arrow aria-hidden className="size-3" />}
        </button>
      </span>
    );
  };

  const cell = (x: Entry, i: number, start = 0) => {
    const isOn = selected.has(x.id);
    const isActive = x.kind === "file" && x.o.key === active;
    const common = {
      "data-cell": i,
      role: "gridcell" as const,
      "aria-label": x.name,
      tabIndex: i === at ? 0 : -1,
      "aria-selected": isOn,
      onKeyDown: (e: React.KeyboardEvent) => onKey(e, i),
      onFocus: () => setFocus(i),
    };
    const check = (
      <span
        className={cn(
          "relative z-[1] grid size-6 shrink-0 place-items-center transition-opacity",
          !isOn && selected.size === 0 && "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 max-sm:opacity-100",
        )}
        onClick={(e) => e.stopPropagation()}
      >
        <Checkbox tabIndex={-1} checked={isOn} onCheckedChange={() => toggle(x.id)} aria-label={`Select ${x.name}`} />
      </span>
    );
    const actions = x.kind === "file" && (
      <span className="relative z-[1] hidden shrink-0 items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 @lg:flex">
        <a
          href={download(x.o.key)}
          download={x.name}
          tabIndex={-1}
          aria-label={`Download ${x.name}`}
          title="Download"
          className="grid size-7 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-press hover:text-ink"
          onClick={(e) => e.stopPropagation()}
        >
          <Download className="size-3.5" />
        </a>
        <button
          type="button"
          tabIndex={-1}
          aria-label={`Copy a link to ${x.name}`}
          title="Copy link"
          onClick={(e) => {
            e.stopPropagation();
            onLink(x);
          }}
          className="grid size-7 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-press hover:text-ink"
        >
          <Link2 className="size-3.5" />
        </button>
        {onDelete && (
          <button
            type="button"
            tabIndex={-1}
            aria-label={`Delete ${x.name}`}
            title="Delete (Delete)"
            onClick={(e) => {
              e.stopPropagation();
              onDelete([x.id]);
            }}
            className="grid size-7 place-items-center rounded-[6px] text-ink-3 hover:bg-danger-wash hover:text-danger"
          >
            <Trash2 className="size-3.5" />
          </button>
        )}
      </span>
    );

    if (view === "grid") {
      const src = x.kind === "file" ? thumb(x.o, 256) : undefined;
      return (
        <div
          key={x.id}
          {...common}
          title={x.kind === "file" ? x.o.key : x.prefix}
          onClick={(e) => click(e, i)}
          className={cn(
            "group relative flex h-[176px] cursor-default flex-col overflow-hidden rounded-[10px] border bg-paper-raised outline-none transition-colors",
            "focus-visible:shadow-[0_0_0_2px_var(--focus)]",
            isActive ? "border-brass shadow-[0_0_0_1px_var(--brass)]" : isOn ? "border-ink-3" : "border-rule-2 hover:border-rule-3",
          )}
        >
          <div className="relative grid h-[120px] place-items-center overflow-hidden bg-paper-sunk">
            {x.kind === "folder" ? (
              <Folder aria-hidden className="size-9 text-ink-3" strokeWidth={1.25} />
            ) : src ? (
              <img src={src} alt="" loading="lazy" decoding="async" draggable={false} className="size-full object-cover" />
            ) : (
              <FileGlyph name={x.name} className="size-8 text-ink-4" />
            )}
            <span className="absolute top-1.5 left-1.5">{check}</span>
          </div>
          <div className="flex min-w-0 flex-1 flex-col justify-center px-2.5">
            <span className="truncate font-mono text-[0.78125rem] text-ink">{x.name}</span>
            <span className="truncate text-xs text-ink-3 tnum">{x.kind === "file" ? bytes(x.o.size) : "Folder"}</span>
          </div>
        </div>
      );
    }
    const src = x.kind === "file" ? thumb(x.o, 64) : undefined;
    return (
      <div
        key={x.id}
        role="row"
        aria-rowindex={i + 2}
        aria-selected={isOn}
        onClick={(e) => click(e, i)}
        className={cn(
          "group absolute inset-x-0 top-0 grid h-10 cursor-default items-center gap-x-4 rounded-[6px] pr-1 pl-1 transition-colors duration-[var(--dur-state)] hover:bg-paper-sunk",
          listCols,
          (isOn || isActive) && "bg-paper-sunk",
        )}
        style={{ transform: `translateY(${start}px)` }}
      >
        {isActive && <span aria-hidden className="absolute inset-y-2 left-0 w-[2px] rounded-full bg-brass" />}
        <span
          {...common}
          title={x.kind === "file" ? x.o.key : x.prefix}
          className="flex min-w-0 items-center gap-2 self-stretch rounded-[5px] outline-none focus-visible:shadow-[inset_0_0_0_2px_var(--focus)]"
        >
          {check}
          {x.kind === "folder" ? (
            <Folder aria-hidden className="size-4 shrink-0 text-ink-3" />
          ) : src ? (
            <img src={src} alt="" loading="lazy" decoding="async" className="size-6 shrink-0 rounded-[4px] bg-paper-sunk object-cover" />
          ) : (
            <FileGlyph name={x.name} />
          )}
          <span className="min-w-0 flex-1 truncate font-mono text-[0.8125rem] text-ink">{x.name}</span>
          {actions}
        </span>
        <span role="gridcell" className="text-right text-sm text-ink-2 tnum">
          {x.kind === "file" ? bytes(x.o.size) : ""}
        </span>
        <span role="gridcell" className="hidden truncate text-sm text-ink-3 @lg:block" title={x.kind === "file" ? full(x.o.lastModified) : undefined}>
          {x.kind === "file" ? relative(x.o.lastModified) : ""}
        </span>
        <span role="gridcell" className="hidden truncate text-sm text-ink-3 @2xl:block">
          {x.kind === "folder" ? "Folder" : kindWord(x.name)}
        </span>
      </div>
    );
  };

  return (
    <div className={cn("@container flex min-h-0 flex-col", className)}>
      <div
        ref={scroller}
        role="grid"
        aria-label={label}
        aria-multiselectable
        aria-rowcount={view === "list" ? entries.length + 1 : rows}
        aria-colcount={view === "list" ? 4 : cols}
        className="relative min-h-0 flex-1 overflow-y-auto overscroll-contain"
      >
        {view === "list" && (
          <div
            role="row"
            aria-rowindex={1}
            className={cn("sticky top-0 z-[2] grid h-9 items-center gap-x-4 border-b border-rule bg-paper pr-1 pl-1", listCols)}
          >
            <span
              role="columnheader"
              className="flex items-center gap-2"
              aria-sort={sort.by === "name" ? (sort.dir === "asc" ? "ascending" : "descending") : "none"}
            >
              <span className="grid size-6 place-items-center">
                <Checkbox
                  tabIndex={-1}
                  checked={allOn ? true : someOn ? "indeterminate" : false}
                  onCheckedChange={() => onSelect(allOn ? new Set() : new Set(entries.map((x) => x.id)))}
                  aria-label="Select everything in this folder"
                />
              </span>
              <button
                type="button"
                tabIndex={-1}
                onClick={() => onSort({ by: "name", dir: sort.by === "name" && sort.dir === "asc" ? "desc" : "asc" })}
                className={cn("ml-6 inline-flex items-center gap-1 label hover:text-ink", sort.by === "name" && "text-ink-2")}
              >
                Name
                {sort.by === "name" &&
                  (sort.dir === "asc" ? <ArrowUp aria-hidden className="size-3" /> : <ArrowDown aria-hidden className="size-3" />)}
              </button>
            </span>
            {header("size", "Size", "text-right [&>button]:flex-row-reverse")}
            {header("modified", "Modified", "hidden @lg:block")}
            <span role="columnheader" className="label hidden @2xl:block">
              Kind
            </span>
          </div>
        )}
        <div style={{ height: v.getTotalSize() }} className={cn("relative w-full", view === "grid" && "mt-1")}>
          {view === "list"
            ? v.getVirtualItems().map((it) => cell(entries[it.index], it.index, it.start))
            : v.getVirtualItems().map((it) => (
                <div
                  key={it.key}
                  role="row"
                  aria-rowindex={it.index + 1}
                  className="absolute inset-x-0 top-0 grid gap-3"
                  style={{ transform: `translateY(${it.start}px)`, gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }}
                >
                  {entries.slice(it.index * cols, it.index * cols + cols).map((x, j) => cell(x, it.index * cols + j))}
                </div>
              ))}
        </div>
        {footer}
      </div>
    </div>
  );
});

function kindWord(name: string): string {
  const k = kindOf(name);
  const ext = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1).toUpperCase() : "";
  if (k === "file") return ext ? `${ext} file` : "File";
  if (k === "pdf") return "PDF";
  return `${k.charAt(0).toUpperCase()}${k.slice(1)}${ext && k !== "text" ? ` · ${ext}` : ""}`;
}
