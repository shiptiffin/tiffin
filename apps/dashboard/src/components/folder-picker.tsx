import { Check, ChevronRight, Container, Folder, FolderOpen, Search } from "lucide-react";
import { useId, useMemo, useState, type ReactNode } from "react";
import { FrameworkLogo } from "@/components/framework-logo";
import { Button } from "@/components/ui/button";
import { RadioGroup, RadioItem } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/cn";
import { presetName, presetOf } from "@/lib/frameworks";
import type { RepoRoot } from "@/lib/github";

/** The top of the repository, as a value ("" can't be a radio value). */
const TOP = ".";

type Node = { path: string; name: string; children: string[] };

/** The folder list as a tree: each folder's children, the top's under "". */
function treeOf(folders: string[]): Map<string, Node> {
  const nodes = new Map<string, Node>([["", { path: "", name: "", children: [] }]]);
  for (const f of [...folders].sort()) {
    const parts = f.split("/");
    for (let i = 1; i <= parts.length; i++) {
      const p = parts.slice(0, i).join("/");
      if (nodes.has(p)) continue;
      nodes.set(p, { path: p, name: parts[i - 1], children: [] });
      nodes.get(parts.slice(0, i - 1).join("/"))!.children.push(p);
    }
  }
  return nodes;
}

const ancestors = (p: string) => p.split("/").map((_, i, all) => all.slice(0, i).join("/"));

/**
 * Picks an app's folder from the repository's folders, as a tree (Vercel's
 * Root Directory picker): the folders the box detected an app in carry its
 * framework, and a search narrows the list to matching paths.
 */
export function FolderPicker({
  open,
  onOpenChange,
  repo,
  folders,
  roots = [],
  value,
  onPick,
  loading,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  repo: string;
  folders: string[];
  roots?: RepoRoot[];
  value: string;
  onPick: (path: string) => void;
  loading?: boolean;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[34rem]" aria-describedby={undefined}>
        {open && <Body repo={repo} folders={folders} roots={roots} value={value} loading={loading} onPick={onPick} close={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  );
}

function Body({ repo, folders, roots, value, onPick, close, loading }: { repo: string; folders: string[]; roots: RepoRoot[]; value: string; onPick: (p: string) => void; close: () => void; loading?: boolean }) {
  const uid = useId();
  const [picked, setPicked] = useState(value);
  const [q, setQ] = useState("");
  const nodes = useMemo(() => treeOf(folders), [folders]);
  const found = useMemo(() => new Map(roots.filter((r) => !r.workspace).map((r) => [r.path, r])), [roots]);
  // Open down to the picked folder and to every folder an app was found in.
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(["", ...ancestors(value), ...roots.flatMap((r) => ancestors(r.path))]));
  const toggle = (p: string) =>
    setExpanded((s) => {
      const n = new Set(s);
      if (n.has(p)) n.delete(p);
      else n.add(p);
      return n;
    });

  const needle = q.trim().toLowerCase();
  const rows: Array<{ path: string; depth: number }> = [];
  if (needle) {
    for (const f of folders) if (f.toLowerCase().includes(needle)) rows.push({ path: f, depth: 0 });
  } else {
    const walk = (p: string, depth: number) => {
      for (const c of nodes.get(p)?.children ?? []) {
        rows.push({ path: c, depth });
        if (expanded.has(c)) walk(c, depth + 1);
      }
    };
    walk("", 0);
  }

  return (
    <>
      <DialogHeader className="pb-3">
        <DialogTitle>Root directory</DialogTitle>
        <DialogDescription className="text-[0.875rem]">
          The folder in <span className="ident text-[0.8125rem] text-ink">{repo}</span> with the app’s package.json or Dockerfile. Builds and installs run there.
        </DialogDescription>
      </DialogHeader>
      <div className="px-6 pb-3">
        <div className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-ink-4" />
          <label htmlFor={`${uid}-q`} className="sr-only">
            Search folders
          </label>
          <input
            id={`${uid}-q`}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={folders.length ? `Search ${folders.length} folders` : "Search folders"}
            spellCheck={false}
            autoComplete="off"
            className="h-9 w-full rounded-[7px] border border-rule-2 bg-paper-raised pr-2.5 pl-8 text-[0.84375rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]"
          />
        </div>
      </div>
      <DialogBody className="px-3 pb-3">
        <RadioGroup aria-label="Folders" value={picked || TOP} onValueChange={(v) => setPicked(v === TOP ? "" : v)} className="flex flex-col">
          {!needle && <Row path={TOP} label={<span className="text-ink-2">The top of the repository</span>} depth={0} root={found.get("")} picked={picked === ""} onConfirm={() => (onPick(""), close())} />}
          {rows.map(({ path, depth }) => {
            const n = nodes.get(path);
            const kids = (n?.children.length ?? 0) > 0 && !needle;
            return (
              <Row
                key={path}
                path={path}
                depth={depth}
                label={needle ? <Highlight text={path} needle={needle} /> : n?.name}
                root={found.get(path)}
                picked={picked === path}
                expand={kids ? { open: expanded.has(path), toggle: () => toggle(path) } : undefined}
                onConfirm={() => (onPick(path), close())}
              />
            );
          })}
        </RadioGroup>
        {loading && <p className="px-3 py-4 text-sm text-ink-3">Reading the repository’s folders…</p>}
        {!loading && needle && rows.length === 0 && <p className="px-3 py-4 text-sm text-ink-3">No folder matches “{q}”.</p>}
        {!loading && !needle && folders.length === 0 && <p className="px-3 py-4 text-sm text-ink-3">This repository has no folders, only files at its top.</p>}
      </DialogBody>
      <DialogFooter>
        <span className="ident mr-auto min-w-0 truncate text-[0.75rem] text-ink-3 max-sm:hidden">{picked || "/"}</span>
        <Button type="button" variant="ghost" size="md" onClick={close}>
          Cancel
        </Button>
        <Button type="button" variant="primary" size="md" onClick={() => (onPick(picked), close())}>
          Use this folder
        </Button>
      </DialogFooter>
    </>
  );
}

function Row({
  path,
  label,
  depth,
  root,
  picked,
  expand,
  onConfirm,
}: {
  path: string;
  label: ReactNode;
  depth: number;
  root?: RepoRoot;
  picked: boolean;
  expand?: { open: boolean; toggle: () => void };
  onConfirm: () => void;
}) {
  const Icon = expand?.open ? FolderOpen : Folder;
  return (
    <div className={cn("group/row flex items-center rounded-[7px] transition-colors", picked ? "bg-brass-wash" : "hover:bg-paper-hover")} style={{ paddingLeft: `${depth * 1.125}rem` }}>
      {expand ? (
        <button
          type="button"
          onClick={expand.toggle}
          aria-label={`${expand.open ? "Close" : "Open"} ${path}`}
          aria-expanded={expand.open}
          className="grid size-7 shrink-0 place-items-center rounded-[6px] text-ink-3 hover:text-ink focus-visible:shadow-[0_0_0_2px_var(--brass-wash)] focus-visible:outline-hidden"
        >
          <ChevronRight className={cn("size-3.5 transition-transform duration-[var(--dur-state)]", expand.open && "rotate-90")} />
        </button>
      ) : (
        <span className="size-7 shrink-0" aria-hidden />
      )}
      <RadioItem
        value={path}
        onDoubleClick={onConfirm}
        className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-[6px] pr-2 text-left text-[0.84375rem] text-ink outline-hidden focus-visible:shadow-[0_0_0_2px_var(--brass-wash)]"
      >
        <Icon className={cn("size-4 shrink-0", picked ? "text-brass-ink" : "text-ink-3")} strokeWidth={1.75} aria-hidden />
        <span className={cn("min-w-0 truncate text-[0.8125rem]", path !== TOP && "ident")}>{label}</span>
        {root && (
          <span className="ml-auto inline-flex shrink-0 items-center gap-1.5 text-xs text-ink-3">
            {root.builder === "dockerfile" ? "Dockerfile" : presetName(presetOf(root))}
            {root.builder === "dockerfile" ? <Container className="size-3.5 text-ink-2" strokeWidth={1.75} aria-hidden /> : <FrameworkLogo preset={presetOf(root)} className="size-3.5 text-ink-2" />}
          </span>
        )}
        {picked && <Check className={cn("size-3.5 shrink-0 text-brass-ink", !root && "ml-auto")} strokeWidth={2.5} aria-hidden />}
      </RadioItem>
    </div>
  );
}

function Highlight({ text, needle }: { text: string; needle: string }) {
  const i = text.toLowerCase().indexOf(needle);
  if (i < 0) return <>{text}</>;
  return (
    <>
      {text.slice(0, i)}
      <mark className="rounded-[3px] bg-brass-wash text-ink">{text.slice(i, i + needle.length)}</mark>
      {text.slice(i + needle.length)}
    </>
  );
}
