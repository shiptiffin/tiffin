import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ChevronDown, Download, FolderInput, FolderPlus, Search, Settings2, Trash2, Upload, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { mod, mq, type StorageBucket, type StorageObject } from "@/api/modules";
import { useTitle } from "@/components/favicon";
import { Crumbs, NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { ReadOnlyBanner } from "@/components/read-only";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/ui/dropdown";
import { Input } from "@/components/ui/input";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { bytes, count, int } from "@/lib/format";
import { PARTS } from "@/lib/names";
import { useCommand, useKeyHelp, useShortcut } from "@/lib/shortcuts";
import { Segmented } from "@/routes/kv/parts";
import { Browser, type BrowserHandle, type Entry, type Sort, type View } from "./browser";
import { FilePanel, publicLink } from "./panel";
import { BucketSettings } from "./settings";
import { addUploads, droppedFiles, UploadTray } from "./uploads";
import { baseName, byName, EXPIRIES, resizable } from "./words";
import { FilesWrites, useFiles } from "./write";

const PAGE = 1000;
const none = new Set<string>();
const MAX_PAGES = 100; // 100,000 files in one folder before "load more"

const readView = (): View => {
  try {
    return localStorage.getItem("files-view") === "grid" ? "grid" : "list";
  } catch {
    return "list";
  }
};

/** One bucket: its folders and files, uploads, and a file's panel. */
export function BucketPage({ project, bucket, prefix = "", file }: { project: string; bucket: string; prefix?: string; file?: string }) {
  return (
    <FilesWrites project={project} bucket={bucket}>
      <BucketView project={project} bucket={bucket} prefix={prefix} file={file} />
    </FilesWrites>
  );
}

function BucketView({ project, bucket, prefix, file }: { project: string; bucket: string; prefix: string; file?: string }) {
  useTitle(`${bucket} · ${PARTS.storage.name}`);
  const navigate = useNavigate();
  const { run, canWrite } = useFiles();
  const info = useQuery(mq.storage(project));
  const b = info.data?.buckets?.find((x) => x.name === bucket);
  const transforms = !!info.data?.imageTransforms;

  // The search and the selection belong to a folder: another folder starts afresh.
  const [found, setFound] = useState({ at: prefix, text: "" });
  const search = found.at === prefix ? found.text : "";
  const setSearch = (text: string) => setFound({ at: prefix, text });
  const [typed, setTyped] = useState({ at: prefix, q: "" });
  const q = typed.at === prefix ? typed.q : "";
  useEffect(() => {
    const t = setTimeout(() => setTyped({ at: prefix, q: search.trim() }), search ? 200 : 0);
    return () => clearTimeout(t);
  }, [search, prefix]);
  const flat = !!q;

  const list = useInfiniteQuery({
    queryKey: ["objects", project, bucket, prefix, q],
    queryFn: ({ pageParam }) => mod.objects(project, bucket, prefix + q, pageParam || undefined, { flat, limit: PAGE }),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextCursor || undefined,
    retry: (n, e) => !(e instanceof ApiError && e.status < 500) && n < 2,
    refetchInterval: (query) => (query.state.error instanceof ApiError && query.state.error.status === 409 ? 2000 : false),
    placeholderData: (prev, query) => (query?.queryKey[3] === prefix ? prev : undefined),
  });
  // Load every page of a folder, so sorting and select-all see all of it.
  const pages = list.data?.pages.length ?? 0;
  useEffect(() => {
    if (list.hasNextPage && !list.isFetchingNextPage && pages < MAX_PAGES) void list.fetchNextPage();
  }, [list, pages]);

  const [view, setViewState] = useState<View>(readView);
  const setView = (v: View) => {
    setViewState(v);
    try {
      localStorage.setItem("files-view", v);
    } catch {
      // the choice lasts for this visit
    }
  };
  const [sort, setSort] = useState<Sort>({ by: "name", dir: "asc" });
  const [picked, setPicked] = useState({ at: "", ids: new Set<string>() });
  const selected = picked.at === `${prefix}\n${q}` ? picked.ids : none;
  const setSelected = (ids: Set<string>) => setPicked({ at: `${prefix}\n${q}`, ids });
  const [made, setMade] = useState<string[]>([]); // folders made here, empty until a file lands

  const entries = useMemo<Entry[]>(() => {
    const pagesData = list.data?.pages ?? [];
    const folders = new Set<string>();
    for (const p of pagesData) for (const f of p.prefixes ?? []) folders.add(f);
    if (!flat) for (const f of made) if (f.startsWith(prefix) && f !== prefix && !f.slice(prefix.length, -1).includes("/")) folders.add(f);
    const files: Entry[] = [];
    for (const p of pagesData)
      for (const o of p.objects ?? [])
        if (o.key !== prefix && !o.key.endsWith("/")) files.push({ id: o.key, kind: "file", o, name: o.key.slice(prefix.length) });
    const dir = sort.dir === "asc" ? 1 : -1;
    const cmp = (x: Entry, y: Entry) => {
      if (x.kind === "file" && y.kind === "file") {
        if (sort.by === "size" && x.o.size !== y.o.size) return (x.o.size - y.o.size) * dir;
        if (sort.by === "modified" && x.o.lastModified !== y.o.lastModified) return x.o.lastModified < y.o.lastModified ? -dir : dir;
      }
      return byName(x.name, y.name) * (sort.by === "name" ? dir : 1);
    };
    const fs: Entry[] = [...folders].map((f) => ({ id: f, kind: "folder" as const, prefix: f, name: f.slice(prefix.length, -1) }));
    return [...fs.sort(cmp), ...files.sort(cmp)];
  }, [list.data, flat, made, prefix, sort]);

  const open = file ? entries.find((x) => x.kind === "file" && x.o.key === file) : undefined;
  const openFile = open?.kind === "file" ? open.o : undefined;
  const go = useCallback(
    (s: { prefix?: string; file?: string }) =>
      void navigate({
        to: "/projects/$project/storage/$bucket",
        params: { project, bucket },
        search: { prefix: s.prefix || undefined, file: s.file || undefined },
      }),
    [navigate, project, bucket],
  );
  const parts = prefix.split("/").filter(Boolean);
  const parent = parts.length > 1 ? `${parts.slice(0, -1).join("/")}/` : "";
  // Keyboard moves between folders keep the keyboard in the list: up lands on the folder you left, in on its first entry.
  const [focusNext, setFocusNext] = useState<{ at: string; id?: string } | null>(null);
  const up = prefix
    ? () => {
        setFocusNext({ at: parent, id: prefix });
        go({ prefix: parent });
      }
    : undefined;

  // ---- actions
  const browser = useRef<BrowserHandle>(null);
  useEffect(() => {
    if (!focusNext || focusNext.at !== prefix || !list.isSuccess || entries.length === 0) return;
    browser.current?.focus(focusNext.id);
    setFocusNext(null); // eslint-disable-line react-hooks/set-state-in-effect
  }, [focusNext, prefix, list.isSuccess, entries]);
  const searchRef = useRef<HTMLInputElement>(null);
  const filesInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const [renaming, setRenaming] = useState<Entry | null>(null);
  const [moving, setMoving] = useState<Entry[] | null>(null);
  const [linking, setLinking] = useState<StorageObject | null>(null);
  const [newFolder, setNewFolder] = useState(false);
  const [settings, setSettings] = useState(false);
  const [replacing, setReplacing] = useState<Array<{ file: File; key: string }> | null>(null);

  const byId = (ids: string[]) => entries.filter((x) => ids.includes(x.id));
  const remove = async (ids: string[]) => {
    const picked = byId(ids);
    const files = picked.flatMap((x) => (x.kind === "file" ? [x.o.key] : []));
    if (files.length)
      await run("delete", { keys: files }, (r) =>
        r.files === 1 ? `Deleted ${baseName(files[0])}.` : `Deleted ${count(r.files, "file")}, ${bytes(r.bytes)}.`,
      ).catch(() => undefined);
    for (const f of picked.filter((x) => x.kind === "folder")) {
      if (f.kind !== "folder") continue;
      if (made.includes(f.prefix)) {
        setMade((m) => m.filter((x) => x !== f.prefix));
        continue;
      }
      await run("delete", { prefix: f.prefix }, (r) => `Deleted the ${f.name} folder: ${count(r.files, "file")}, ${bytes(r.bytes)}.`).catch(
        () => undefined,
      );
    }
    setSelected(new Set());
    if (file && files.includes(file)) go({ prefix });
  };
  const copyLink = async (o: StorageObject) => {
    if (b?.public && b.publicUrl) {
      if (await copyText(publicLink(b, o.key))) toast({ title: `Copied the link to ${baseName(o.key)}.` });
    } else setLinking(o);
  };
  const queue = (picked: Array<{ file: File; path: string }>) => {
    if (!canWrite || picked.length === 0) return;
    const items = picked.map((p) => ({ file: p.file, key: prefix + p.path }));
    const existing = new Set(entries.flatMap((x) => (x.kind === "file" ? [x.o.key] : [])));
    if (items.some((i) => existing.has(i.key))) setReplacing(items);
    else addUploads(project, b, bucket, items);
  };
  const fromInput = (fl: FileList | null) => queue([...(fl ?? [])].map((f) => ({ file: f, path: f.webkitRelativePath || f.name })));

  // ---- shortcuts: in the shell's ? sheet, and ⌘K's "Upload a file" runs ours
  useShortcut("/", "Find files by name", () => searchRef.current?.focus());
  useShortcut("v", "List or grid", () => setView(view === "list" ? "grid" : "list"));
  useCommand(canWrite ? { id: "upload-file", label: "Upload files", keys: "u", keywords: ["files", "put", "add"], run: () => filesInput.current?.click() } : null);
  useCommand(canWrite ? { id: "new-folder", label: "New folder", keys: "n", keywords: ["folder", "directory", "make"], run: () => setNewFolder(true) } : null);
  useShortcut("Escape", "Close the file", () => go({ prefix }), "On this page", !!file);
  useKeyHelp("Files", KEYS);

  // ---- drag and drop
  const [dragging, setDragging] = useState(false);
  const depth = useRef(0);

  if (list.isError && notOnBox(list.error)) return <NotOnBox what="Buckets" />;
  const making = list.error instanceof ApiError && list.error.status === 409;
  const loading = list.isPending || (list.isFetching && !list.isFetchingNextPage && !list.data);
  const total = b?.objects;
  const thumb = (o: StorageObject, w: number) =>
    transforms && resizable(o.key) ? mod.fileUrl(project, bucket, o.key, { w: w === 64 ? 64 : 256, q: 75, f: "webp" }) : undefined;
  const selectedFiles = byId([...selected]);
  const selBytes = selectedFiles.reduce((a, x) => a + (x.kind === "file" ? x.o.size : 0), 0);

  return (
    <div
      className="relative"
      onDragEnter={(e) => {
        if (!canWrite || !e.dataTransfer.types.includes("Files")) return;
        depth.current++;
        setDragging(true);
      }}
      onDragLeave={() => {
        depth.current = Math.max(0, depth.current - 1);
        if (depth.current === 0) setDragging(false);
      }}
      onDragOver={(e) => canWrite && e.preventDefault()}
      onDrop={(e) => {
        e.preventDefault();
        depth.current = 0;
        setDragging(false);
        void droppedFiles(e.dataTransfer).then(queue);
      }}
    >
      <Page full>
        <PageHeader
          eyebrow={
            <Crumbs
              items={[
                { label: project, to: "/projects/$project", params: { project }, mono: true },
                { label: PARTS.storage.name, to: "/projects/$project/storage", params: { project } },
              ]}
            />
          }
          title={<span className="font-mono tracking-[-0.02em]">{bucket}</span>}
          lede={
            b ? (
              <>
                {total !== undefined && `${count(total, "file")}, ${bytes(b.bytes)}.`} {b.public ? "Anyone with a link can open them." : "They open only with links that expire."}
              </>
            ) : undefined
          }
          actions={
            <>
              {b && (
                <span
                  className={cn(
                    "inline-flex h-8 items-center rounded-full border px-3 text-[0.8125rem]",
                    b.public ? "border-brass text-ink" : "border-rule-2 text-ink-2",
                  )}
                >
                  {b.public ? "Anyone can read" : "Private"}
                </span>
              )}
              {canWrite && b && (
                <Button onClick={() => setSettings(true)}>
                  <Settings2 />
                  Settings
                </Button>
              )}
              {canWrite && (
                <Menu>
                  <MenuTrigger asChild>
                    <Button variant="primary" title="Upload (U)">
                      <Upload />
                      Upload
                      <ChevronDown className="-mr-1 opacity-70" />
                    </Button>
                  </MenuTrigger>
                  <MenuContent align="end">
                    <MenuItem onSelect={() => filesInput.current?.click()}>Files…</MenuItem>
                    <MenuItem onSelect={() => folderInput.current?.click()}>A folder…</MenuItem>
                  </MenuContent>
                </Menu>
              )}
              <input ref={filesInput} type="file" multiple hidden onChange={(e) => (fromInput(e.target.files), (e.target.value = ""))} />
              <input
                ref={folderInput}
                type="file"
                multiple
                hidden
                {...({ webkitdirectory: "" } as Record<string, string>)}
                onChange={(e) => (fromInput(e.target.files), (e.target.value = ""))}
              />
            </>
          }
        />
        <ReadOnlyBanner project={project} className="mt-6" />

        <div className="mt-7 flex flex-col gap-2.5 md:flex-row md:items-center">
          <nav aria-label="Folder" className="-ml-1.5 flex min-w-0 flex-1 flex-wrap items-center gap-0.5 font-mono text-[0.84375rem]">
            {[bucket, ...parts].map((p, i) => {
              const here = i === parts.length;
              return (
                <span key={i} className="flex items-center gap-0.5">
                  {i > 0 && <span className="text-ink-4">/</span>}
                  <button
                    type="button"
                    onClick={() => go({ prefix: i === 0 ? "" : `${parts.slice(0, i).join("/")}/` })}
                    aria-current={here ? "location" : undefined}
                    className={cn(
                      "rounded-[5px] px-1.5 py-0.5 transition-colors hover:bg-paper-sunk",
                      here ? "text-ink" : "text-ink-3 hover:text-ink",
                    )}
                  >
                    {p}
                  </button>
                </span>
              );
            })}
          </nav>
          <div className="flex items-center gap-2">
            <label className="relative flex h-8 min-w-0 flex-1 items-center md:w-64 md:flex-none">
              <Search aria-hidden className="pointer-events-none absolute left-2.5 size-3.5 text-ink-3" />
              <input
                ref={searchRef}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape" && search) setSearch("");
                  if (e.key === "ArrowDown") {
                    e.preventDefault();
                    browser.current?.focus();
                  }
                }}
                placeholder={prefix ? `Find in ${parts[parts.length - 1]} by name` : "Find files by name"}
                aria-label="Find files whose names start with"
                spellCheck={false}
                className="h-8 w-full rounded-[7px] border border-rule-2 bg-paper-raised pr-8 pl-8 text-sm text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass"
              />
              {search ? (
                <button
                  type="button"
                  onClick={() => setSearch("")}
                  aria-label="Clear the search"
                  className="absolute right-1 grid size-6 place-items-center rounded-[5px] text-ink-3 hover:bg-paper-sunk"
                >
                  <X className="size-3.5" />
                </button>
              ) : (
                <kbd className="kbd absolute right-1.5" aria-hidden>
                  /
                </kbd>
              )}
            </label>
            {canWrite && (
              <Button size="icon" variant="ghost" onClick={() => setNewFolder(true)} aria-label="New folder (N)" title="New folder (N)">
                <FolderPlus />
              </Button>
            )}
            <Segmented
              label="View"
              value={view}
              onChange={setView}
              options={[
                { value: "list", label: "List" },
                { value: "grid", label: "Grid" },
              ]}
              className="[&_button]:px-2"
            />
          </div>
        </div>

        {selected.size > 0 && (
          <div
            role="region"
            aria-label="Selection"
            className="mt-3 flex flex-wrap items-center gap-2 rounded-[10px] border border-rule-2 bg-paper-raised py-1.5 pr-1.5 pl-3 shadow-[var(--top-light)]"
          >
            <p className="text-sm text-ink" aria-live="polite">
              {int(selected.size)} selected{selBytes > 0 && <span className="text-ink-3"> · {bytes(selBytes)}</span>}
            </p>
            <span className="flex-1" />
            {selectedFiles.every((x) => x.kind === "file") && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  for (const x of selectedFiles) {
                    if (x.kind !== "file") continue;
                    const a = document.createElement("a");
                    a.href = mod.fileUrl(project, bucket, x.o.key, { download: true });
                    a.download = baseName(x.o.key);
                    a.click();
                  }
                }}
              >
                <Download />
                Download
              </Button>
            )}
            {canWrite && (
              <Button size="sm" variant="ghost" onClick={() => setMoving(selectedFiles)}>
                <FolderInput />
                Move
              </Button>
            )}
            {canWrite && (
              <Button size="sm" variant="danger-quiet" onClick={() => void remove([...selected])}>
                <Trash2 />
                Delete
              </Button>
            )}
            <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())}>
              Clear
            </Button>
          </div>
        )}

        <div className="mt-3 grid gap-x-8 gap-y-6 lg:grid-cols-[minmax(0,1fr)_25rem]">
          <div className={cn("min-w-0", openFile && "max-lg:hidden")}>
            {loading && !making && (
              <div className="space-y-2 border-t border-rule pt-3" aria-busy>
                {["w-[60%]", "w-[45%]", "w-[70%]", "w-[52%]", "w-[38%]"].map((w) => (
                  <Skeleton key={w} className={cn("h-7", w)} />
                ))}
              </div>
            )}
            {making && <p className="border-y border-rule py-10 text-center text-base text-ink-3">Making the bucket on the box…</p>}
            {list.isError && !making && <ProblemNote className="my-4" error={list.error} />}
            {list.isSuccess && entries.length === 0 && (
              <div className="rounded-[10px] border border-dashed border-rule-3 px-6 py-14 text-center">
                <p className="text-md text-ink">{q ? `Nothing here starts with “${q}”.` : prefix ? "This folder is empty." : "No files yet."}</p>
                {canWrite && !q && <p className="mt-1 text-base text-ink-3">Drop files or folders anywhere on this page, or use Upload.</p>}
              </div>
            )}
            {list.isSuccess && entries.length > 0 && (
              <>
                <Browser
                  ref={browser}
                  label={prefix ? `Files in ${prefix}` : `Files in ${bucket}`}
                  entries={entries}
                  view={view}
                  sort={sort}
                  onSort={setSort}
                  selected={selected}
                  onSelect={setSelected}
                  active={file}
                  onOpen={(x) => {
                    if (x.kind === "file") return go({ prefix, file: x.o.key === file ? undefined : x.o.key });
                    setFocusNext({ at: x.prefix });
                    go({ prefix: x.prefix });
                  }}
                  onUp={up}
                  onEscape={file ? () => go({ prefix }) : undefined}
                  onFind={() => searchRef.current?.focus()}
                  onDelete={canWrite ? (ids) => void remove(ids) : undefined}
                  onRename={canWrite ? setRenaming : undefined}
                  onLink={(x) => x.kind === "file" && void copyLink(x.o)}
                  download={(key) => mod.fileUrl(project, bucket, key, { download: true })}
                  thumb={thumb}
                  className="max-h-[max(24rem,calc(100dvh-20rem))]"
                />
                <p className="mt-2 flex flex-wrap justify-between gap-x-4 gap-y-1 text-xs text-ink-3" aria-live="polite">
                  <span>
                    {q
                      ? `${count(entries.length, "file")} starting with “${q}”`
                      : `${count(entries.filter((x) => x.kind === "file").length, "file")}`}
                    {list.hasNextPage ? (pages >= MAX_PAGES ? " shown; there are more" : " so far, loading the rest…") : ""}
                  </span>
                  {canWrite && !openFile && <span className="max-sm:hidden">Drop files anywhere to upload them here · ? for shortcuts</span>}
                </p>
                {list.hasNextPage && pages >= MAX_PAGES && (
                  <Button size="sm" className="mt-2" onClick={() => void list.fetchNextPage()}>
                    Load more
                  </Button>
                )}
              </>
            )}
          </div>
          <aside aria-label="File" className={cn("min-w-0 lg:sticky lg:top-6 lg:self-start", !openFile && "max-lg:hidden")}>
            {openFile && b ? (
              <FilePanel
                key={openFile.key}
                project={project}
                bucket={b}
                o={openFile}
                transforms={transforms}
                onClose={() => go({ prefix })}
                onRename={canWrite ? () => setRenaming(open!) : undefined}
                onMove={canWrite ? () => setMoving([open!]) : undefined}
                onDelete={canWrite ? () => void remove([openFile.key]) : undefined}
              />
            ) : file && list.isSuccess && !list.hasNextPage ? (
              <p className="rounded-[10px] border border-dashed border-rule-3 px-5 py-10 text-center text-base text-ink-3">
                That file isn’t here any more.
              </p>
            ) : (
              <div className="hidden rounded-[12px] border border-dashed border-rule-3 px-6 py-12 text-center lg:block">
                <p className="text-base text-ink-2">Pick a file to see it here.</p>
                <p className="mt-1 text-sm text-ink-3">Images, video, audio, PDFs and text preview; images get a resized-link builder.</p>
              </div>
            )}
          </aside>
        </div>
      </Page>

      {dragging && (
        <div className="pointer-events-none fixed inset-0 z-40 grid place-items-center bg-[var(--scrim)] p-6 animate-fade">
          <div className="grid place-items-center rounded-[14px] border-2 border-dashed border-brass bg-paper-raised px-16 py-12 text-center shadow-overlay">
            <Upload className="size-6 text-brass-ink" />
            <p className="mt-3 text-xl font-[550] text-ink">Drop to upload</p>
            <p className="mt-1 font-mono text-sm text-ink-3">
              into {bucket}/{prefix}
            </p>
          </div>
        </div>
      )}

      <UploadTray project={project} bucket={bucket} />
      {b && <BucketSettings project={project} b={b} open={settings} onOpenChange={setSettings} />}
      <NameDialog
        open={!!renaming}
        onOpenChange={(o) => !o && setRenaming(null)}
        title={renaming?.kind === "folder" ? "Rename folder" : "Rename file"}
        label="New name"
        initial={renaming?.kind === "folder" ? renaming.name : renaming ? baseName(renaming.id) : ""}
        action="Rename"
        check={(v) =>
          v.includes("/")
            ? "A name can’t have a / in it; use Move to put it in a folder."
            : entries.some((x) => x.name === v && x !== renaming)
              ? "There’s already something with that name here."
              : ""
        }
        onDone={async (name) => {
          const x = renaming!;
          setRenaming(null);
          if (x.kind === "folder") {
            const to = `${x.prefix.slice(0, x.prefix.length - x.name.length - 1)}${name}/`;
            if (made.includes(x.prefix)) return setMade((m) => [...m.filter((f) => f !== x.prefix), to]);
            await run("move", { prefix: x.prefix, to }, (r) => `Renamed the ${x.name} folder to ${name} (${count(r.files, "file")}).`).catch(
              () => undefined,
            );
          } else {
            const to = x.o.key.slice(0, x.o.key.length - baseName(x.o.key).length) + name;
            const r = await run("move", { keys: [x.o.key], to }, () => `Renamed ${baseName(x.o.key)} to ${name}.`).catch(() => undefined);
            if (r && file === x.o.key) go({ prefix, file: to });
          }
        }}
      />
      <NameDialog
        open={!!moving}
        onOpenChange={(o) => !o && setMoving(null)}
        title={moving?.length === 1 ? `Move ${moving[0].name}` : `Move ${count(moving?.length ?? 0, "item")}`}
        label="To the folder"
        description="A folder in this bucket, such as photos/2026. Leave it empty for the top level. Folders that don’t exist yet are made."
        initial={prefix.replace(/\/$/, "")}
        action="Move"
        allowEmpty
        check={(v) => (v.split("/").some((s) => s === "." || s === "..") ? "Use plain folder names." : "")}
        onDone={async (raw) => {
          const dest = raw.replace(/^\/+|\/+$/g, "") ? `${raw.replace(/^\/+|\/+$/g, "")}/` : "";
          const items = moving ?? [];
          setMoving(null);
          setSelected(new Set());
          const keys = items.flatMap((x) => (x.kind === "file" ? [x.o.key] : []));
          const where = dest ? dest.slice(0, -1) : "the top level";
          if (keys.length)
            await run("move", { keys, to: dest }, (r) =>
              r.files === 1 ? `Moved ${baseName(keys[0])} to ${where}.` : `Moved ${count(r.files, "file")} to ${where}.`,
            ).catch(() => undefined);
          for (const x of items) {
            if (x.kind !== "folder" || made.includes(x.prefix)) continue;
            await run(
              "move",
              { prefix: x.prefix, to: `${dest}${x.name}/` },
              (r) => `Moved the ${x.name} folder to ${where} (${count(r.files, "file")}).`,
            ).catch(() => undefined);
          }
          if (file && keys.includes(file)) go({ prefix: dest, file: dest + baseName(file) });
        }}
      />
      <NameDialog
        open={newFolder}
        onOpenChange={setNewFolder}
        title="New folder"
        label="Name"
        description="It stays here while it’s empty; upload files into it to keep it."
        initial=""
        action="Make folder"
        check={(v) =>
          v.includes("/")
            ? "A name can’t have a / in it."
            : entries.some((x) => x.kind === "folder" && x.name === v)
              ? "There’s already a folder with that name."
              : ""
        }
        onDone={(name) => {
          setNewFolder(false);
          const f = `${prefix}${name}/`;
          setMade((m) => [...m, f]);
          go({ prefix: f });
        }}
      />
      {linking && b && <LinkDialog project={project} bucket={b} o={linking} onClose={() => setLinking(null)} />}
      <ReplaceDialog
        items={replacing}
        onClose={() => setReplacing(null)}
        onReplace={() => {
          addUploads(project, b, bucket, replacing ?? []);
          setReplacing(null);
        }}
        onKeepBoth={() => {
          const existing = new Set(entries.flatMap((x) => (x.kind === "file" ? [x.o.key] : [])));
          addUploads(
            project,
            b,
            bucket,
            (replacing ?? []).map((i) => ({ ...i, key: freeName(i.key, existing) })),
          );
          setReplacing(null);
        }}
      />
    </div>
  );
}

/** "photo.jpg" → "photo 2.jpg" (or 3, …), a name nothing has yet. */
function freeName(key: string, taken: Set<string>): string {
  const slash = key.lastIndexOf("/");
  const dot = key.lastIndexOf(".");
  const [stem, ext] = dot > slash + 1 ? [key.slice(0, dot), key.slice(dot)] : [key, ""];
  for (let i = 2; ; i++) if (!taken.has(`${stem} ${i}${ext}`)) return `${stem} ${i}${ext}`;
}

/** One text field and a button: rename, move, new folder. */
function NameDialog({
  open,
  onOpenChange,
  title,
  label,
  description,
  initial,
  action,
  check,
  allowEmpty,
  onDone,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: string;
  label: string;
  description?: string;
  initial: string;
  action: string;
  check: (v: string) => string;
  allowEmpty?: boolean;
  onDone: (v: string) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        {open && <NameForm {...{ onOpenChange, title, label, description, initial, action, check, allowEmpty, onDone }} />}
      </DialogContent>
    </Dialog>
  );
}

function NameForm({
  onOpenChange,
  title,
  label,
  description,
  initial,
  action,
  check,
  allowEmpty,
  onDone,
}: {
  onOpenChange: (o: boolean) => void;
  title: string;
  label: string;
  description?: string;
  initial: string;
  action: string;
  check: (v: string) => string;
  allowEmpty?: boolean;
  onDone: (v: string) => void;
}) {
  const [v, setV] = useState(initial);
  const t = v.trim();
  const problem = t ? check(t) : "";
  const ok = (allowEmpty || !!t) && !problem && (t !== initial || allowEmpty);
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (ok) onDone(t);
      }}
    >
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        {description && <DialogDescription>{description}</DialogDescription>}
      </DialogHeader>
      <DialogBody>
        <label className="block">
          <span className="text-sm text-ink-2">{label}</span>
          <Input
            autoFocus
            value={v}
            onChange={(e) => setV(e.target.value)}
            onFocus={(e) => {
              const dot = e.target.value.lastIndexOf(".");
              e.target.setSelectionRange(0, dot > 0 ? dot : e.target.value.length);
            }}
            spellCheck={false}
            aria-invalid={!!problem || undefined}
            aria-describedby="name-problem"
            className="mt-1 font-mono"
          />
          <span id="name-problem" role="alert" className="mt-1 block min-h-5 text-sm text-danger">
            {problem}
          </span>
        </label>
      </DialogBody>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!ok}>
          {action}
        </Button>
      </DialogFooter>
    </form>
  );
}

/** A private file's link: how long it works, then Copy. */
function LinkDialog({ project, bucket, o, onClose }: { project: string; bucket: StorageBucket; o: StorageObject; onClose: () => void }) {
  const [ttl, setTtl] = useState(EXPIRIES[0].seconds);
  const [err, setErr] = useState<unknown>(null);
  return (
    <Dialog open onOpenChange={(x) => !x && onClose()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Link to {baseName(o.key)}</DialogTitle>
          <DialogDescription>
            {bucket.name} is private, so the link works for a while and then stops. Anyone who has it can open the file until then.
          </DialogDescription>
        </DialogHeader>
        <DialogBody>
          <Segmented
            label="Works for"
            value={String(ttl)}
            onChange={(v) => setTtl(Number(v))}
            options={EXPIRIES.map((x) => ({ value: String(x.seconds), label: x.label }))}
          />
          {err ? <ProblemNote className="mt-3" error={err} /> : null}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            onClick={async () => {
              try {
                const l = await mod.fileLink(project, bucket.name, { key: o.key, expiresIn: ttl });
                if (await copyText(l.url))
                  toast({ title: `Copied a link to ${baseName(o.key)}.`, detail: `It works for ${EXPIRIES.find((x) => x.seconds === ttl)?.label}.` });
                onClose();
              } catch (e) {
                setErr(e);
              }
            }}
          >
            Copy link
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ReplaceDialog({
  items,
  onClose,
  onReplace,
  onKeepBoth,
}: {
  items: Array<{ file: File; key: string }> | null;
  onClose: () => void;
  onReplace: () => void;
  onKeepBoth: () => void;
}) {
  return (
    <Dialog open={!!items} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Some of these are here already</DialogTitle>
          <DialogDescription>Replacing a file can’t be undone. Keep both to upload them with a number added to the name.</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={onKeepBoth}>Keep both</Button>
          <Button variant="danger" onClick={onReplace}>
            Replace
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Keys the file list handles itself, for the ? sheet. */
const KEYS: Array<[string, string]> = [
  ["↑ ↓ ← →", "Move through files"],
  ["Enter", "Open a file or folder"],
  ["Space", "Select"],
  ["⇧ ↑ ↓", "Select a run of files"],
  ["⌘ A", "Select everything here"],
  ["F2", "Rename"],
  ["Delete", "Delete (with Undo)"],
  ["⌥ ↑", "Up a folder"],
  ["Esc", "Clear the selection, then close the file"],
];
