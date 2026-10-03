import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowUpRight, ChevronRight, Eye, File, FileImage, FileText, Folder, Globe, KeyRound, Link2, Lock, Trash2, Upload, X } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { mod, mq, type StorageBucket, type StorageObject, type TrashEntry } from "@/api/modules";
import { Meter } from "@/components/chart";
import { Command, CopyButton } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { Crumbs, Empty, Page, PageHeader, Skeleton, Untrusted, NotOnBox } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { bytes, num } from "@/lib/format";
import { useMe } from "@/lib/me";
import { expiry, full, relative } from "@/lib/time";
import { Confirm } from "@/components/confirm";

// ------------------------------------------------------------------ overview

export function StoragePage({ project }: { project: string }) {
  useTitle(`${project} · Storage`);
  const info = useQuery(mq.storage(project));
  const trash = useQuery(mq.trash(project));
  const { can } = useMe();

  if (info.isError && notOnBox(info.error)) return <NotOnBox what="Buckets" />;
  if (info.isPending)
    return (
      <Page wide>
        <Skeleton className="h-10 w-64" />
        <Skeleton className="mt-8 h-28 w-full" />
      </Page>
    );
  if (info.isError)
    return (
      <Page wide>
        <ProblemNote error={info.error} title="Couldn't load storage" />
      </Page>
    );

  const s = info.data;
  const buckets = s.buckets ?? [];
  const quota = s.quotaBytes;
  return (
    <Page wide>
      <PageHeader
        eyebrow={<StorageCrumbs project={project} />}
        title="Storage"
        lede={
          <>
            S3-compatible buckets on the box's own disk. Your apps already have the keys: <code className="font-mono text-ink">Bun.s3</code> and any
            AWS SDK just work.
          </>
        }
      />

      <section className="mt-10 grid gap-px overflow-hidden rounded-xl border border-rule bg-rule sm:grid-cols-[1.4fr_1fr_1fr]">
        <div className="bg-raised p-5">
          <p className="text-2xs font-medium tracking-wider text-ink-3 uppercase">Used</p>
          <p className="mt-2 flex items-baseline gap-2">
            <span className="display text-3xl text-ink tnum">{bytes(s.usedBytes)}</span>
            <span className="text-base text-ink-3">of {quota > 0 ? bytes(quota, 0) : "unlimited"}</span>
          </p>
          {quota > 0 && <Meter ratio={s.usedBytes / quota} className="mt-3" label="Storage used of quota" />}
          <p className="mt-2 text-sm text-ink-3">
            {s.quotaSource === "box-default" ? "The box's default limit" : "This project's own limit"} · measured {relative(s.measuredAt)}
          </p>
        </div>
        <Fact label="Buckets" value={num(buckets.length)} sub={`${buckets.filter((b) => b.public).length} public`} />
        <Fact label="Files" value={num(buckets.reduce((n, b) => n + b.objects, 0))} sub={`region ${s.region}`} />
      </section>

      <section className="mt-12" aria-labelledby="buckets">
        <h2 id="buckets" className="display-italic mb-3 text-xl text-ink">
          Buckets
        </h2>
        {buckets.length === 0 ? (
          <Empty icon={<Folder />} title="No buckets yet">
            Add one under <code className="font-mono text-ink">services.storage.buckets</code> in tiffin.config.ts, then plan and apply.
          </Empty>
        ) : (
          <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
            {buckets.map((b, k) => (
              <BucketRow key={b.name} project={project} b={b} share={s.usedBytes ? b.bytes / s.usedBytes : 0} k={k} />
            ))}
          </ul>
        )}
      </section>

      <div className="mt-12 grid gap-10 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]">
        <UseIt project={project} buckets={buckets} canReveal={can("apply:irreversible")} />
        <TrashList project={project} entries={trash.data ?? []} canPurge={can("apply:irreversible")} />
      </div>
    </Page>
  );
}

function StorageCrumbs({ project, bucket }: { project: string; bucket?: boolean }) {
  return (
    <Crumbs
      items={[
        { label: project, to: "/projects/$project", params: { project }, mono: true },
        ...(bucket ? [{ label: "Storage", to: "/projects/$project/storage", params: { project } }] : []),
      ]}
    />
  );
}

function Fact({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="bg-raised p-5">
      <p className="text-2xs font-medium tracking-wider text-ink-3 uppercase">{label}</p>
      <p className="display mt-2 text-3xl text-ink tnum">{value}</p>
      {sub && <p className="mt-2 text-sm text-ink-3">{sub}</p>}
    </div>
  );
}

function BucketRow({ project, b, share, k }: { project: string; b: StorageBucket; share: number; k: number }) {
  return (
    <li className="group relative animate-rise" style={{ animationDelay: `${k * 40}ms` }}>
      <Link
        to="/projects/$project/storage/$bucket"
        params={{ project, bucket: b.name }}
        className="grid grid-cols-[2rem_1fr_auto] items-center gap-x-4 gap-y-1 px-4 py-4 transition-colors hover:bg-hover/50 sm:grid-cols-[2rem_minmax(0,1fr)_9rem_7rem_1.25rem] sm:px-5"
      >
        <span className={cn("grid size-8 place-items-center rounded-lg", b.public ? "bg-out-wash text-out" : "bg-hover text-ink-3")}>
          {b.public ? <Globe className="size-4" /> : <Lock className="size-4" />}
        </span>
        <span className="min-w-0">
          <span className="flex items-center gap-2">
            <span className="text-md font-medium text-ink">{b.name}</span>
            {b.state === "pending" && <span className="rounded-full bg-brass-wash px-2 py-px text-xs text-brass-ink">being created</span>}
          </span>
          <span className="block truncate text-sm text-ink-3">
            {b.public ? "Public: anyone with a link can read files" : "Private: signed requests only"} ·{" "}
            <code className="font-mono text-xs">{b.s3Name}</code>
          </span>
        </span>
        <span className="hidden sm:block">
          <span className="block text-right font-mono text-sm text-ink-2 tnum">{bytes(b.bytes)}</span>
          <Meter ratio={share} className="mt-1.5 h-1" label={`${b.name}: share of project storage`} />
        </span>
        <span className="text-right text-sm text-ink-3 tnum">
          {num(b.objects)} {b.objects === 1 ? "file" : "files"}
        </span>
        <ChevronRight className="hidden size-4 text-ink-4 transition-transform group-hover:translate-x-0.5 group-hover:text-ink sm:block" />
      </Link>
    </li>
  );
}

function UseIt({ project, buckets, canReveal }: { project: string; buckets: StorageBucket[]; canReveal: boolean }) {
  const [creds, setCreds] = useState<Record<string, string> | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const first = buckets.find((b) => !b.public) ?? buckets[0];
  const envName = first ? `S3_BUCKET_${first.name.toUpperCase().replace(/-/g, "_")}` : "S3_BUCKET";
  const snippet = `import { s3 } from "bun";

// Apps get S3_* env vars, and Bun.s3 reads them.
const bucket = process.env.${envName};
const file = s3.file("avatars/ada.png", { bucket });

await file.write(photo, { type: "image/png" });
const link = file.presign({ expiresIn: 600 }); // 10 min`;
  return (
    <section aria-labelledby="use">
      <h2 id="use" className="display-italic mb-3 text-xl text-ink">
        Use it from your app
      </h2>
      <div className="overflow-hidden rounded-xl border border-rule bg-paper-sunk">
        <div className="flex items-center justify-between border-b border-rule px-4 py-2">
          <span className="font-mono text-xs text-ink-3">upload.ts</span>
          <CopyButton value={snippet} label="Copy code" />
        </div>
        <pre className="overflow-x-auto px-4 py-3 font-mono text-[0.78rem] leading-5 text-ink-2">
          <code>{snippet}</code>
        </pre>
      </div>
      <div className="mt-4 rounded-xl border border-rule bg-raised/60 p-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-base text-ink">
            <KeyRound className="mr-1.5 mb-0.5 inline size-4 text-ink-3" />
            Credentials for local tools
          </p>
          {!creds && canReveal && (
            <Button
              size="sm"
              onClick={async () => {
                setErr(null);
                try {
                  setCreds(await mod.credentials(project));
                } catch (e) {
                  setErr(e);
                }
              }}
            >
              <Eye />
              Show the S3 env
            </Button>
          )}
        </div>
        <p className="mt-1 text-sm text-ink-3">
          {canReveal
            ? "The key can read and delete every file in this project, so only reveal it on a screen you trust."
            : "Revealing the key needs a token that may destroy data (apply:irreversible)."}
        </p>
        {err ? <ProblemNote className="mt-3" error={err} /> : null}
        {creds && (
          <Command
            className="mt-3"
            cmd={Object.entries(creds)
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([k, v]) => `${k}=${v}`)
              .join(" ")}
          />
        )}
      </div>
    </section>
  );
}

function TrashList({ project, entries, canPurge }: { project: string; entries: TrashEntry[]; canPurge: boolean }) {
  const qc = useQueryClient();
  const [purging, setPurging] = useState<TrashEntry | null>(null);
  return (
    <section aria-labelledby="trash">
      <h2 id="trash" className="display-italic mb-3 text-xl text-ink">
        Trash
      </h2>
      {entries.length === 0 ? (
        <p className="rounded-xl border border-dashed border-rule-strong px-4 py-5 text-base text-ink-3">
          Empty. Deleted buckets wait here for 7 days, files and all.
        </p>
      ) : (
        <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
          {entries.map((t) => (
            <li key={t.id} className="flex items-start gap-3 px-4 py-3">
              <Trash2 className="mt-1 size-4 shrink-0 text-ink-3" />
              <div className="min-w-0 flex-1">
                <p className="text-base text-ink">
                  <span className="font-medium">{t.bucket}</span>{" "}
                  <span className="text-ink-3">
                    · {num(t.objects)} {t.objects === 1 ? "file" : "files"} · {bytes(t.bytes)}
                  </span>
                </p>
                <p className="text-sm text-ink-3">
                  Deleted {relative(t.deletedAt)} · gone for good {expiry(t.expiresAt)}
                </p>
                <p className="mt-1 text-sm text-ink-2">
                  To restore it, add it back to tiffin.config.ts or{" "}
                  <Link to="/" search={{ project, risk: "irreversible" }} className="text-brass-ink underline underline-offset-4 hover:text-ink">
                    undo the change
                  </Link>{" "}
                  that removed it.
                </p>
              </div>
              {canPurge && (
                <Button variant="danger-quiet" size="sm" onClick={() => setPurging(t)}>
                  Purge
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      <Confirm
        open={!!purging}
        onClose={() => setPurging(null)}
        title={`Purge ${purging?.bucket ?? "this bucket"} for good?`}
        body={`Its ${purging ? num(purging.objects) : ""} files are deleted now instead of in 7 days. Nothing can bring them back after this.`}
        action="Purge now"
        run={() => mod.purge(purging!.id)}
        done={() => qc.invalidateQueries({ queryKey: ["trash"] })}
      />
    </section>
  );
}

// ------------------------------------------------------------------ browser

type UploadItem = { name: string; key: string; state: "uploading" | "done" | "failed"; error?: string };

const imageExt = /\.(png|jpe?g|gif|webp|avif|svg|ico)$/i;
const textExt = /\.(txt|md|json|csv|tsv|log|xml|yml|yaml|toml|ini|html?|css|js|ts|tsx|jsx|sql|sh|env)$|(^|\/)(robots\.txt|README)$/i;

function kindOf(key: string): "image" | "text" | "pdf" | "file" {
  if (imageExt.test(key)) return "image";
  if (/\.pdf$/i.test(key)) return "pdf";
  if (textExt.test(key)) return "text";
  return "file";
}

function FileIcon({ name, className }: { name: string; className?: string }) {
  const k = kindOf(name);
  const C = k === "image" ? FileImage : k === "text" || k === "pdf" ? FileText : File;
  return <C className={cn("size-4 shrink-0 text-ink-3", className)} />;
}

export function BucketPage({ project, bucket, prefix = "", file }: { project: string; bucket: string; prefix?: string; file?: string }) {
  useTitle(`${bucket} · Storage`);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const info = useQuery(mq.storage(project));
  const list = useQuery(mq.objects(project, bucket, prefix));
  const { can } = useMe();
  const writer = can("apply:reversible");
  const [filter, setFilter] = useState("");
  // Extra pages for the current folder; another folder starts from scratch.
  const [paged, setPaged] = useState<{ prefix: string; more: StorageObject[]; cursor?: string }>({ prefix, more: [] });
  const more = paged.prefix === prefix ? paged.more : [];
  const cursor = paged.prefix === prefix ? paged.cursor : undefined;
  const [dragging, setDragging] = useState(false);
  const [uploads, setUploads] = useState<UploadItem[]>([]);
  const [deleting, setDeleting] = useState<StorageObject | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const depth = useRef(0);

  const b = info.data?.buckets?.find((x) => x.name === bucket);
  const go = (search: { prefix?: string; file?: string }) =>
    navigate({
      to: "/projects/$project/storage/$bucket",
      params: { project, bucket },
      search: { prefix: search.prefix || undefined, file: search.file },
    });

  const next = cursor ?? list.data?.nextCursor;

  const uploadFiles = useCallback(
    async (files: File[]) => {
      if (!writer || files.length === 0) return;
      const items = files.map((f) => ({ name: f.name, key: prefix + f.name, state: "uploading" as const }));
      setUploads((u) => [...items, ...u].slice(0, 8));
      for (const f of files) {
        const key = prefix + f.name;
        try {
          if (f.size > 10 * 1024 * 1024) throw new Error("Over 10 MB: upload it from your app with a presigned PUT URL.");
          await mod.upload(project, bucket, key, f);
          setUploads((u) => u.map((x) => (x.key === key ? { ...x, state: "done" } : x)));
        } catch (e) {
          const msg = e instanceof ApiError ? (e.problem.detail ?? e.message) : e instanceof Error ? e.message : "Upload failed";
          setUploads((u) => u.map((x) => (x.key === key ? { ...x, state: "failed", error: msg } : x)));
        }
      }
      qc.invalidateQueries({ queryKey: ["objects", project, bucket] });
      qc.invalidateQueries({ queryKey: ["storage", project] });
      setTimeout(() => setUploads((u) => u.filter((x) => x.state !== "done")), 2500);
    },
    [bucket, prefix, project, qc, writer],
  );

  if (list.isError && notOnBox(list.error)) return <NotOnBox what="Buckets" />;

  const objects = [...(list.data?.objects ?? []), ...more].filter((o) => o.key !== prefix);
  const folders = list.data?.prefixes ?? [];
  const f = filter.toLowerCase();
  const shownFolders = folders.filter((p) => p.slice(prefix.length).toLowerCase().includes(f));
  const shownObjects = objects.filter((o) => o.key.slice(prefix.length).toLowerCase().includes(f));
  const selected = file ? objects.find((o) => o.key === file) : undefined;
  const parts = prefix.split("/").filter(Boolean);

  return (
    <div
      className="relative"
      onDragEnter={(e) => {
        if (!writer || !e.dataTransfer.types.includes("Files")) return;
        depth.current++;
        setDragging(true);
      }}
      onDragLeave={() => {
        depth.current = Math.max(0, depth.current - 1);
        if (depth.current === 0) setDragging(false);
      }}
      onDragOver={(e) => writer && e.preventDefault()}
      onDrop={(e) => {
        e.preventDefault();
        depth.current = 0;
        setDragging(false);
        void uploadFiles([...e.dataTransfer.files]);
      }}
    >
      <Page full>
        <PageHeader
          eyebrow={<StorageCrumbs project={project} bucket />}
          title={
            <span className="flex items-center gap-3">
              {bucket}
              {b && (
                <span
                  className={cn(
                    "inline-flex h-6 items-center gap-1.5 rounded-full px-2.5 font-sans text-sm",
                    b.public ? "bg-out-wash text-out" : "bg-hover text-ink-2",
                  )}
                >
                  {b.public ? <Globe className="size-3.5" /> : <Lock className="size-3.5" />}
                  {b.public ? "Public" : "Private"}
                </span>
              )}
            </span>
          }
          lede={b ? `${num(b.objects)} files · ${bytes(b.bytes)} · S3 name ${b.s3Name}` : undefined}
          actions={
            writer && (
              <>
                <input ref={input} type="file" multiple className="hidden" onChange={(e) => uploadFiles([...(e.target.files ?? [])])} />
                <Button variant="primary" onClick={() => input.current?.click()}>
                  <Upload />
                  Upload
                </Button>
              </>
            )
          }
        />

        <div className="mt-8 flex flex-col gap-3 sm:flex-row sm:items-center">
          <nav aria-label="Folder" className="flex min-w-0 flex-1 flex-wrap items-center gap-1 font-mono text-sm">
            <button onClick={() => go({})} className={cn("rounded px-1.5 py-0.5 hover:bg-hover", parts.length === 0 ? "text-ink" : "text-ink-3")}>
              {bucket}
            </button>
            {parts.map((p, i) => (
              <span key={i} className="flex items-center gap-1">
                <span className="text-ink-4">/</span>
                <button
                  onClick={() => go({ prefix: parts.slice(0, i + 1).join("/") + "/" })}
                  className={cn("rounded px-1.5 py-0.5 hover:bg-hover", i === parts.length - 1 ? "text-ink" : "text-ink-3")}
                >
                  {p}
                </button>
              </span>
            ))}
          </nav>
          <input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter this folder"
            aria-label="Filter this folder"
            className="h-8 w-full rounded-md border border-rule bg-paper px-2.5 text-sm text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass sm:w-56"
          />
        </div>

        <div className="mt-3 grid gap-4 lg:grid-cols-[minmax(0,1fr)_22rem]">
          <div className="min-w-0 overflow-hidden rounded-xl border border-rule bg-raised/60">
            <div className="hidden grid-cols-[minmax(0,1fr)_6rem_9rem_4.5rem] gap-4 border-b border-rule px-4 py-2 text-2xs font-medium tracking-wider text-ink-3 uppercase sm:grid">
              <span>Name</span>
              <span className="text-right">Size</span>
              <span>Modified</span>
              <span />
            </div>
            {list.isPending && (
              <div className="space-y-2 p-4">
                {[0, 1, 2, 3].map((i) => (
                  <Skeleton key={i} className="h-6" />
                ))}
              </div>
            )}
            {list.isError && <ProblemNote className="m-4" error={list.error} />}
            <ul className="divide-y divide-rule/70">
              {prefix && (
                <li>
                  <button
                    onClick={() => go({ prefix: parts.slice(0, -1).join("/") + (parts.length > 1 ? "/" : "") })}
                    className="flex w-full items-center gap-3 px-4 py-2.5 text-left text-base text-ink-3 hover:bg-hover/60"
                  >
                    <Folder className="size-4" /> ..
                  </button>
                </li>
              )}
              {shownFolders.map((p) => (
                <li key={p}>
                  <button
                    onClick={() => go({ prefix: p })}
                    className="grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-4 px-4 py-2.5 text-left hover:bg-hover/60 sm:grid-cols-[minmax(0,1fr)_6rem_9rem_4.5rem]"
                  >
                    <span className="flex min-w-0 items-center gap-3">
                      <Folder className="size-4 shrink-0 fill-brass-wash text-brass-ink" />
                      <span className="truncate font-mono text-[0.8125rem] text-ink">{p.slice(prefix.length)}</span>
                    </span>
                    <ChevronRight className="size-4 justify-self-end text-ink-4 sm:col-start-4" />
                  </button>
                </li>
              ))}
              {shownObjects.map((o) => {
                const name = o.key.slice(prefix.length);
                const active = o.key === file;
                return (
                  <li key={o.key} className={cn("group relative", active && "bg-hover")}>
                    <button
                      onClick={() => go({ prefix, file: active ? undefined : o.key })}
                      className="grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 px-4 py-2.5 text-left hover:bg-hover/60 sm:grid-cols-[minmax(0,1fr)_6rem_9rem_4.5rem]"
                      aria-pressed={active}
                    >
                      <span className="flex min-w-0 items-center gap-3">
                        <FileIcon name={name} />
                        <span className="truncate font-mono text-[0.8125rem] text-ink">{name}</span>
                      </span>
                      <span className="text-right font-mono text-xs text-ink-2 tnum">{bytes(o.size)}</span>
                      <span className="hidden text-sm text-ink-3 sm:block" title={full(o.lastModified)}>
                        {relative(o.lastModified)}
                      </span>
                    </button>
                    <span className="absolute top-1/2 right-3 hidden -translate-y-1/2 gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100 sm:flex">
                      <LinkButton project={project} bucket={bucket} objKey={o.key} />
                      {writer && (
                        <button
                          onClick={() => setDeleting(o)}
                          aria-label={`Delete ${name}`}
                          className="grid size-7 place-items-center rounded-md text-ink-3 hover:bg-irr-wash hover:text-irr"
                        >
                          <Trash2 className="size-3.5" />
                        </button>
                      )}
                    </span>
                  </li>
                );
              })}
            </ul>
            {list.isSuccess && shownFolders.length + shownObjects.length === 0 && (
              <div className="px-6 py-14 text-center">
                <Upload className="mx-auto size-6 text-ink-4" />
                <p className="mt-3 text-md text-ink">{filter ? "Nothing matches that filter." : "This folder is empty."}</p>
                {writer && !filter && <p className="mt-1 text-base text-ink-3">Drop files anywhere on this page to upload them here.</p>}
              </div>
            )}
            {next && (
              <div className="border-t border-rule p-3 text-center">
                <Button
                  size="sm"
                  onClick={async () => {
                    const r = await mod.objects(project, bucket, prefix, next);
                    setPaged({ prefix, more: [...more, ...(r.objects ?? [])], cursor: r.nextCursor ?? "" });
                  }}
                >
                  Load more
                </Button>
              </div>
            )}
          </div>

          <aside className="min-w-0 lg:sticky lg:top-20 lg:self-start">
            {selected ? (
              <Preview
                key={selected.key}
                project={project}
                bucket={b}
                bucketName={bucket}
                o={selected}
                onClose={() => go({ prefix })}
                onDelete={writer ? () => setDeleting(selected) : undefined}
              />
            ) : (
              <div className="hidden rounded-xl border border-dashed border-rule-strong px-5 py-10 text-center lg:block">
                <Eye className="mx-auto size-5 text-ink-4" />
                <p className="mt-3 text-base text-ink-2">Pick a file to preview it.</p>
                <p className="mt-1 text-sm text-ink-3">Images and text show here; anything else opens with a signed link.</p>
              </div>
            )}
          </aside>
        </div>
      </Page>

      {dragging && (
        <div className="pointer-events-none fixed inset-0 z-40 grid place-items-center bg-[oklch(0.15_0.01_60/0.35)] p-6 backdrop-blur-[2px] animate-fade">
          <div className="grid place-items-center rounded-2xl border-2 border-dashed border-brass bg-raised/95 px-16 py-12 text-center shadow-pop animate-pop">
            <Upload className="size-7 text-brass-ink" />
            <p className="display mt-3 text-2xl text-ink">Drop to upload</p>
            <p className="mt-1 font-mono text-sm text-ink-3">
              into {bucket}/{prefix}
            </p>
          </div>
        </div>
      )}

      {uploads.length > 0 && (
        <div
          className="fixed right-4 bottom-4 z-40 w-[min(22rem,calc(100vw-2rem))] overflow-hidden rounded-xl border border-rule bg-raised shadow-pop animate-pop"
          role="status"
        >
          <p className="border-b border-rule px-4 py-2 text-sm font-medium text-ink">Uploads</p>
          <ul className="max-h-60 overflow-y-auto">
            {uploads.map((u) => (
              <li key={u.key} className="flex items-start gap-3 px-4 py-2 text-sm">
                <span
                  className={cn(
                    "mt-1.5 size-1.5 shrink-0 rounded-full",
                    u.state === "done" ? "bg-rev" : u.state === "failed" ? "bg-irr" : "animate-pulse bg-brass",
                  )}
                />
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-mono text-xs text-ink">{u.name}</span>
                  {u.error && <span className="block text-xs text-irr">{u.error}</span>}
                </span>
                <span className="text-xs text-ink-3">{u.state === "uploading" ? "uploading…" : u.state}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      <Confirm
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title={`Delete ${deleting?.key.split("/").pop() ?? "this file"}?`}
        body="Deleting a file is immediate and final: unlike buckets, single files don't go to the trash."
        action="Delete file"
        run={() => mod.deleteObject(project, bucket, deleting!.key)}
        done={() => {
          if (deleting?.key === file) go({ prefix });
          qc.invalidateQueries({ queryKey: ["objects", project, bucket] });
          qc.invalidateQueries({ queryKey: ["storage", project] });
        }}
      />
    </div>
  );
}

function LinkButton({ project, bucket, objKey }: { project: string; bucket: string; objKey: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      aria-label="Copy a link that works for an hour"
      title="Copy a link that works for an hour"
      onClick={async () => {
        const p = await mod.presign(project, bucket, objKey);
        if (await copyText(p.url)) {
          setDone(true);
          setTimeout(() => setDone(false), 1400);
        }
      }}
      className="grid size-7 place-items-center rounded-md text-ink-3 hover:bg-hover hover:text-ink"
    >
      {done ? <span className="text-xs text-rev">✓</span> : <Link2 className="size-3.5" />}
    </button>
  );
}

function Preview({
  project,
  bucket,
  bucketName,
  o,
  onClose,
  onDelete,
}: {
  project: string;
  bucket?: StorageBucket;
  bucketName: string;
  o: StorageObject;
  onClose: () => void;
  onDelete?: () => void;
}) {
  const kind = kindOf(o.key);
  const small = o.size <= 1024 * 1024;
  const content = useQuery({
    queryKey: ["object", project, bucketName, o.key, o.etag],
    queryFn: () => mod.object(project, bucketName, o.key),
    enabled: small && (kind === "image" || kind === "text"),
    staleTime: Infinity,
  });
  const link = useMutation({ mutationFn: () => mod.presign(project, bucketName, o.key) });
  const name = o.key.split("/").pop() ?? o.key;
  const publicUrl = bucket?.public && bucket.publicUrl ? `${bucket.publicUrl}/${o.key.split("/").map(encodeURIComponent).join("/")}` : undefined;

  return (
    <div className="overflow-hidden rounded-xl border border-rule bg-raised animate-pop">
      <div className="flex items-center gap-2 border-b border-rule px-4 py-2.5">
        <FileIcon name={name} />
        <p className="min-w-0 flex-1 truncate font-mono text-sm text-ink">{name}</p>
        <button
          onClick={onClose}
          aria-label="Close preview"
          className="grid size-7 place-items-center rounded-md text-ink-3 hover:bg-hover hover:text-ink"
        >
          <X className="size-4" />
        </button>
      </div>
      <div className="bg-paper-sunk">
        {kind === "image" && content.data?.base64 && (
          <div className="grid place-items-center bg-[repeating-conic-gradient(var(--hover)_0_25%,transparent_0_50%)] bg-[length:16px_16px] p-4">
            <img
              src={`data:${content.data.contentType};base64,${content.data.base64}`}
              alt={name}
              className="max-h-72 rounded-md object-contain shadow-pop"
            />
          </div>
        )}
        {kind === "text" && content.data && (
          <Untrusted className="rounded-none border-0" label="File contents, shown as plain text">
            <pre className="max-h-72 overflow-auto px-4 py-3 font-mono text-[0.75rem] leading-5 whitespace-pre-wrap text-ink-2">
              {content.data.text ?? "(binary)"}
            </pre>
          </Untrusted>
        )}
        {content.isPending && content.fetchStatus !== "idle" && <Skeleton className="m-4 h-40" />}
        {(kind === "pdf" || kind === "file" || !small) && (
          <div className="px-5 py-8 text-center">
            <FileIcon name={name} className="mx-auto size-8" />
            <p className="mt-3 text-sm text-ink-3">
              {kind === "pdf"
                ? "PDFs open in a new tab with a signed link."
                : !small
                  ? "Too big to preview here."
                  : "No preview for this kind of file."}
            </p>
          </div>
        )}
        {content.isError && <ProblemNote className="m-4" error={content.error} />}
      </div>
      <dl className="grid grid-cols-[5.5rem_1fr] gap-x-3 gap-y-1.5 border-t border-rule px-4 py-3 text-sm">
        <dt className="text-ink-3">Size</dt>
        <dd className="text-ink-2 tnum">{bytes(o.size)}</dd>
        <dt className="text-ink-3">Modified</dt>
        <dd className="text-ink-2">{full(o.lastModified)}</dd>
        <dt className="text-ink-3">Key</dt>
        <dd className="min-w-0 truncate font-mono text-xs text-ink-2" title={o.key}>
          {o.key}
        </dd>
        <dt className="text-ink-3">ETag</dt>
        <dd className="truncate font-mono text-xs text-ink-3">{o.etag.replace(/"/g, "")}</dd>
      </dl>
      <div className="flex flex-wrap gap-2 border-t border-rule px-4 py-3">
        <Button
          size="sm"
          variant="primary"
          onClick={async () => {
            const p = link.data ?? (await link.mutateAsync());
            window.open(p.url, "_blank", "noopener,noreferrer");
          }}
        >
          <ArrowUpRight />
          Open
        </Button>
        <Button
          size="sm"
          onClick={async () => {
            const p = link.data ?? (await link.mutateAsync());
            await copyText(p.url);
          }}
        >
          <Link2 />
          {link.data ? "Copied · 1 hour link" : "Copy link"}
        </Button>
        {publicUrl && <CopyButton value={publicUrl} label="Copy public URL" className="size-7 rounded-md border border-rule" />}
        {onDelete && (
          <Button size="sm" variant="danger-quiet" className="ml-auto" onClick={onDelete}>
            <Trash2 />
            Delete
          </Button>
        )}
      </div>
    </div>
  );
}
