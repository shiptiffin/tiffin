import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowUpRight, ChevronRight, CornerLeftUp, Eye, File, FileImage, FileText, Folder, Link2, Search, Trash2, Upload, X } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { mod, mq, type StorageBucket, type StorageObject, type TrashEntry } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { Command, CopyButton } from "@/components/copy";
import { Reading, Readings, Rows, Section } from "@/components/data-parts";
import { useTitle } from "@/components/favicon";
import { Crumbs, Empty, Page, PageHeader, Skeleton, Untrusted, NotOnBox } from "@/components/page";
import { ConnectButton } from "@/components/connect";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { ReadOnlyBanner } from "@/components/read-only";
import { SegMeter } from "@/components/seg-meter";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { bytes, bytesParts, count, int, pct, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { change, pendingFor, usePending, type BucketAccess } from "@/lib/staged";
import { full, relative } from "@/lib/time";

const shortDate = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" });

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
  const used = bytesParts(s.filesBytes);
  const files = buckets.reduce((n, b) => n + b.objects, 0);
  // The storage limit counts the project's databases with its files.
  const share = quota > 0 ? s.usedBytes / quota : 0;
  return (
    <Page wide>
      <PageHeader
        eyebrow={<StorageCrumbs project={project} />}
        title="Files"
        actions={<ConnectButton part="files" project={project} />}
        lede={
          <>
            S3-compatible buckets on the box's own disk. Your apps already have the keys, so <code className="ident text-ink">Bun.s3</code> and any
            AWS SDK just work.
          </>
        }
      />
      <ReadOnlyBanner project={project} className="mb-6" />

      <Readings className="grid-cols-2 lg:grid-cols-[1.6fr_1fr_1fr]">
        <Reading
          className="col-span-2 lg:col-span-1"
          label="Used"
          value={used.value}
          unit={`${used.unit} of ${quota > 0 ? bytes(quota, 0) : "no limit"}`}
        >
          {quota > 0 && (
            <SegMeter
              className="mt-2.5"
              label="Storage used of this project's limit"
              value={share * 100}
              scale
              warnAt={0.8}
              fullAt={0.95}
              valueText={pct(share, share < 0.01 ? 2 : 0)}
            />
          )}
          <p className="mt-2 text-xs text-ink-3">
            {quota > 0 ? (
              <>
                With its database, {share < 0.001 ? "under 0.1 %" : pct(share, share < 0.1 ? 1 : 0)} of{" "}
                {s.quotaSource === "box-default" ? "the box's default storage limit" : "its storage limit"}, measured {relative(s.measuredAt)}.
              </>
            ) : (
              <>No storage limit. Measured {relative(s.measuredAt)}.</>
            )}
          </p>
        </Reading>
        <Reading
          label="Buckets"
          value={int(buckets.length)}
          sub={buckets.length ? `${words(buckets.filter((b) => b.public).length, true)} public` : undefined}
        />
        <Reading label="Files" value={int(files)} sub={`region ${s.region}`} />
      </Readings>

      <Section
        className="mt-10"
        id="buckets"
        label="Buckets"
        aside={buckets.length > 0 ? "public or private is a change to tiffin.config.ts" : undefined}
      >
        {buckets.length === 0 ? (
          <Empty title="No buckets yet">
            Add one under <code className="font-mono text-ink">services.storage.buckets</code> in tiffin.config.ts, then plan and apply.
          </Empty>
        ) : (
          <Rows>
            <li aria-hidden className="hidden grid-cols-[minmax(0,1fr)_10.5rem_5.5rem_5rem_1rem] gap-x-6 py-2 sm:grid">
              <span className="label">Bucket</span>
              <span className="label">Who can read</span>
              <span className="label text-right">Size</span>
              <span className="label text-right">Files</span>
              <span />
            </li>
            {buckets.map((b) => (
              <BucketRow key={b.name} project={project} b={b} canStage={can("apply:reversible")} />
            ))}
          </Rows>
        )}
      </Section>

      <div className="mt-12 grid gap-x-12 gap-y-12 lg:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]">
        <UseIt project={project} buckets={buckets} endpoint={s.endpoint} region={s.region} canReveal={can("apply:irreversible")} />
        <TrashList project={project} entries={trash.data ?? []} canPurge={can("apply:irreversible")} canRestore={can("apply:reversible")} />
      </div>
    </Page>
  );
}

function StorageCrumbs({ project, bucket }: { project: string; bucket?: boolean }) {
  return (
    <Crumbs
      items={[
        { label: project, to: "/projects/$project", params: { project }, mono: true },
        ...(bucket ? [{ label: "Files", to: "/projects/$project/storage", params: { project } }] : []),
      ]}
    />
  );
}

function BucketRow({ project, b, canStage }: { project: string; b: StorageBucket; canStage: boolean }) {
  const edits = usePending(project);
  const staged = pendingFor(edits, `bucket:${b.name}`);
  const live: BucketAccess = b.public ? "public" : "private";
  const shown = staged?.kind === "bucket" ? staged.to : live;
  return (
    <li>
      <div className="group relative grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-6 gap-y-2 py-3 transition-colors duration-[var(--dur-state)] hover:bg-paper-sunk sm:-mx-3 sm:grid-cols-[minmax(0,1fr)_10.5rem_5.5rem_5rem_1rem] sm:px-3">
        <span className="min-w-0">
          <Link
            to="/projects/$project/storage/$bucket"
            params={{ project, bucket: b.name }}
            className="font-mono text-[0.84375rem] text-ink after:absolute after:inset-0 after:content-['']"
          >
            {b.name}
          </Link>
          {b.state === "pending" && <span className="ml-2 text-sm text-brass-ink">being created</span>}
          <span className="mt-0.5 block text-sm text-ink-3 sm:truncate">
            {staged ? (
              <span className="text-brass-ink">
                {shown === "public" ? "Opening it to anyone with the link…" : "Making it private…"}
              </span>
            ) : shown === "public" ? (
              "Anyone with the link can read files"
            ) : (
              "Only signed links can read files"
            )}
            <span className="max-sm:hidden"> · {b.s3Name}</span>
          </span>
        </span>
        <span className="relative z-[1] row-span-2 self-center sm:row-span-1">
          <AccessLever
            name={b.name}
            live={live}
            staged={staged?.kind === "bucket" ? staged.to : undefined}
            disabled={!canStage}
            onPick={(to) => change(project, { kind: "bucket", bucket: b.name, from: live, to }, { immediate: true })}
          />
        </span>
        <span className="col-start-1 text-sm text-ink-2 tnum sm:col-start-auto sm:text-right sm:text-base">
          {bytes(b.bytes)}
          <span className="text-ink-3 sm:hidden"> · {count(b.objects, "file")}</span>
        </span>
        <span className="hidden text-right text-sm text-ink-3 tnum sm:block">{int(b.objects)}</span>
        <ChevronRight className="hidden size-4 text-ink-4 transition-transform group-hover:translate-x-0.5 group-hover:text-ink-2 sm:block" />
      </div>
    </li>
  );
}

/**
 * Private or public, as a two-position lever with printed labels. Moving it
 * changes tiffin.config.ts at once (in brass while it applies; making it
 * public asks first, since it reaches outside the box).
 */
function AccessLever({
  name,
  live,
  staged,
  disabled,
  onPick,
}: {
  name: string;
  live: BucketAccess;
  staged?: BucketAccess;
  disabled?: boolean;
  onPick: (to: BucketAccess) => void;
}) {
  const shown = staged ?? live;
  return (
    <span
      role="radiogroup"
      aria-label={`${name}: who can read files${staged ? `, changing to ${staged}` : ""}`}
      className="inline-flex h-7 items-stretch rounded-[7px] border border-rule-2 bg-paper-sunk p-0.5"
    >
      {(["private", "public"] as const).map((v) => {
        const on = shown === v;
        const was = !!staged && live === v;
        return (
          <button
            key={v}
            type="button"
            role="radio"
            aria-checked={on}
            disabled={disabled}
            onClick={() => !on && onPick(v)}
            className={cn(
              "rounded-[5px] border px-2.5 text-[0.71875rem] font-[550] tracking-[0.04em] uppercase transition-colors duration-[var(--dur-state)] disabled:cursor-default",
              on && !staged && "border-rule-2 bg-paper-raised text-ink shadow-[var(--top-light),0_1px_1px_oklch(0.2_0.01_60/0.08)]",
              on && staged && "border-brass bg-brass-wash text-brass-ink",
              !on && !was && "border-transparent text-ink-3 enabled:hover:text-ink",
              was && "border-dashed border-rule-3 text-ink-3",
            )}
          >
            {v}
          </button>
        );
      })}
    </span>
  );
}

function UseIt({
  project,
  buckets,
  endpoint,
  region,
  canReveal,
}: {
  project: string;
  buckets: StorageBucket[];
  endpoint: string;
  region: string;
  canReveal: boolean;
}) {
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
    <Section id="use" label="Use it from your app">
      <div className="overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk">
        <div className="flex items-center justify-between border-b border-rule py-1 pr-1.5 pl-3.5">
          <span className="ident text-ink-3">upload.ts</span>
          <CopyButton value={snippet} label="Copy code" />
        </div>
        <pre className="overflow-x-auto px-3.5 py-3 font-mono text-[0.75rem] leading-5 text-ink-2">
          <code>{snippet}</code>
        </pre>
      </div>
      <dl className="mt-4 grid grid-cols-[6rem_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-sm">
        <dt className="text-ink-3">Endpoint</dt>
        <dd className="truncate font-mono text-[0.78125rem] text-ink-2">{endpoint.replace(/^https?:\/\//, "")}</dd>
        <dt className="text-ink-3">Region</dt>
        <dd className="font-mono text-[0.78125rem] text-ink-2">{region}</dd>
      </dl>
      <div className="mt-4 flex flex-col gap-3 border-t border-rule pt-4 sm:flex-row sm:items-start sm:justify-between">
        <p className="text-sm text-ink-3">
          <span className="text-ink-2">Keys for local tools.</span>{" "}
          {canReveal
            ? "They can read and delete every file in this project, so only reveal them on a screen you trust."
            : "Revealing them needs a key with full access."}
        </p>
        {!creds && canReveal && (
          <Button
            size="sm"
            className="self-start"
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
      {err ? <ProblemNote className="mt-3" error={err} /> : null}
      {creds && (
        <Command
          wrap
          className="mt-3"
          cmd={Object.entries(creds)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([k, v]) => `${k}=${v}`)
            .join(" ")}
        />
      )}
    </Section>
  );
}

function TrashList({ project, entries, canPurge, canRestore }: { project: string; entries: TrashEntry[]; canPurge: boolean; canRestore: boolean }) {
  const qc = useQueryClient();
  const edits = usePending(project);
  const [purging, setPurging] = useState<TrashEntry | null>(null);
  return (
    <Section id="trash" label="Trash" aside="deleted buckets wait 7 days, files and all">
      {entries.length === 0 ? (
        <p className="border-y border-rule py-4 text-base text-ink-3">Empty. A bucket you remove lands here first.</p>
      ) : (
        <Rows>
          {entries.map((t) => {
            const staged = pendingFor(edits, `bucket:${t.bucket}`);
            return (
              <li key={t.id} className="flex items-start gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <p className="text-base text-ink">
                    <span className="font-mono text-[0.84375rem]">{t.bucket}</span>
                    <span className="text-ink-3 tnum">
                      {" "}
                      · {count(t.objects, "file")}, {bytes(t.bytes)}
                    </span>
                  </p>
                  <p className="mt-0.5 text-sm text-ink-3">
                    Deleted {relative(t.deletedAt)}, gone for good on{" "}
                    <time dateTime={t.expiresAt} title={full(t.expiresAt)}>
                      {shortDate.format(new Date(t.expiresAt))}
                    </time>
                    .
                  </p>
                  {staged && <p className="mt-1 text-sm text-brass-ink">Restoring it, private, with its files…</p>}
                </div>
                <span className="flex shrink-0 gap-1">
                  {canRestore && !staged && (
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => change(project, { kind: "bucket", bucket: t.bucket, from: "absent", to: "private" }, { immediate: true })}
                      title="Adds the bucket back to tiffin.config.ts, files and all"
                    >
                      Restore
                    </Button>
                  )}
                  {canPurge && (
                    <Button variant="danger-quiet" size="sm" onClick={() => setPurging(t)}>
                      Purge
                    </Button>
                  )}
                </span>
              </li>
            );
          })}
        </Rows>
      )}
      <Confirm
        open={!!purging}
        onClose={() => setPurging(null)}
        title={`Purge ${purging?.bucket ?? "this bucket"} for good?`}
        body={`Its ${purging ? count(purging.objects, "file") : "files"} are deleted now instead of in 7 days. Nothing can bring them back after this.`}
        action="Purge now"
        run={() => mod.purge(purging!.id)}
        done={() => qc.invalidateQueries({ queryKey: ["trash"] })}
      />
    </Section>
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

/** Pretty-prints JSON files; everything else stays exactly as stored. */
function prettyText(key: string, text: string) {
  if (!/\.json$/i.test(key)) return text;
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

const browserCols = "sm:grid-cols-[minmax(0,1fr)_5.5rem_8.5rem_4rem]";

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
  const up = parts.slice(0, -1).join("/") + (parts.length > 1 ? "/" : "");

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
          title={bucket}
          lede={
            b
              ? `${b.public ? "Public: anyone with the link can read files." : "Private: only signed links can read files."} ${count(b.objects, "file")}, ${bytes(b.bytes)}. S3 name ${b.s3Name}.`
              : undefined
          }
          actions={
            writer && (
              <>
                <input ref={input} type="file" multiple className="hidden" onChange={(e) => uploadFiles([...(e.target.files ?? [])])} />
                <Button variant="primary" onClick={() => input.current?.click()}>
                  <Upload />
                  Upload{prefix ? ` to ${parts[parts.length - 1]}` : ""}
                </Button>
              </>
            )
          }
        />

        <div className="mt-8 flex flex-col gap-3 sm:flex-row sm:items-center">
          <nav aria-label="Folder" className="-ml-1.5 flex min-w-0 flex-1 flex-wrap items-center gap-0.5 font-mono text-[0.84375rem]">
            <button
              onClick={() => go({})}
              className={cn(
                "rounded-[5px] px-1.5 py-0.5 transition-colors hover:bg-paper-sunk",
                parts.length === 0 ? "text-ink" : "text-ink-3 hover:text-ink",
              )}
              aria-current={parts.length === 0 ? "location" : undefined}
            >
              {bucket}
            </button>
            {parts.map((p, i) => (
              <span key={i} className="flex items-center gap-0.5">
                <span className="text-ink-4">/</span>
                <button
                  onClick={() => go({ prefix: parts.slice(0, i + 1).join("/") + "/" })}
                  className={cn(
                    "rounded-[5px] px-1.5 py-0.5 transition-colors hover:bg-paper-sunk",
                    i === parts.length - 1 ? "text-ink" : "text-ink-3 hover:text-ink",
                  )}
                  aria-current={i === parts.length - 1 ? "location" : undefined}
                >
                  {p}
                </button>
              </span>
            ))}
          </nav>
          <label className="relative flex h-8 w-full items-center sm:w-60">
            <Search aria-hidden className="pointer-events-none absolute left-2.5 size-3.5 text-ink-3" />
            <input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter this folder"
              aria-label="Filter this folder"
              className="h-8 w-full rounded-[7px] border border-rule-2 bg-paper-raised pr-2.5 pl-8 text-sm text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass"
            />
          </label>
        </div>

        <div className="mt-4 grid gap-x-8 gap-y-6 lg:grid-cols-[minmax(0,1fr)_23rem]">
          <div className="min-w-0">
            <div className={cn("hidden gap-x-4 border-t border-rule py-2 sm:grid", browserCols)} aria-hidden>
              <span className="label pl-7">Name</span>
              <span className="label text-right">Size</span>
              <span className="label">Modified</span>
              <span />
            </div>
            {list.isPending && (
              <div className="space-y-2 border-t border-rule py-3">
                {[0, 1, 2, 3].map((i) => (
                  <Skeleton key={i} className="h-6" />
                ))}
              </div>
            )}
            {list.isError && <ProblemNote className="my-4" error={list.error} />}
            <ul className="divide-y divide-rule border-y border-rule">
              {prefix && (
                <li>
                  <button
                    onClick={() => go({ prefix: up })}
                    className="flex w-full items-center gap-3 py-2 text-left text-sm text-ink-3 transition-colors hover:bg-paper-sunk hover:text-ink sm:-mx-2 sm:w-[calc(100%+1rem)] sm:px-2"
                  >
                    <CornerLeftUp className="size-4" /> Up to {parts.length > 1 ? parts[parts.length - 2] : bucket}
                  </button>
                </li>
              )}
              {shownFolders.map((p) => (
                <li key={p}>
                  <button
                    onClick={() => go({ prefix: p })}
                    className={cn(
                      "grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 py-2 text-left transition-colors hover:bg-paper-sunk sm:-mx-2 sm:w-[calc(100%+1rem)] sm:px-2",
                      browserCols,
                    )}
                  >
                    <span className="flex min-w-0 items-center gap-3">
                      <Folder className="size-4 shrink-0 text-ink-3" />
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
                  <li key={o.key} className={cn("group relative", active && "bg-paper-sunk")}>
                    {active && <span aria-hidden className="absolute inset-y-1.5 -left-3 w-[2px] rounded-full bg-brass max-sm:hidden" />}
                    <button
                      onClick={() => go({ prefix, file: active ? undefined : o.key })}
                      className={cn(
                        "grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 py-2 text-left transition-colors hover:bg-paper-sunk sm:-mx-2 sm:w-[calc(100%+1rem)] sm:px-2",
                        browserCols,
                      )}
                      aria-pressed={active}
                    >
                      <span className="flex min-w-0 items-center gap-3">
                        <FileIcon name={name} />
                        <span className="truncate font-mono text-[0.8125rem] text-ink">{name}</span>
                      </span>
                      <span className="text-right text-sm text-ink-2 tnum">{bytes(o.size)}</span>
                      <span className="hidden text-sm text-ink-3 sm:block" title={full(o.lastModified)}>
                        {relative(o.lastModified)}
                      </span>
                    </button>
                    <span className="absolute top-1/2 right-0 hidden -translate-y-1/2 gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100 sm:flex">
                      <LinkButton project={project} bucket={bucket} objKey={o.key} />
                      {writer && (
                        <button
                          onClick={() => setDeleting(o)}
                          aria-label={`Delete ${name}`}
                          title="Delete"
                          className="grid size-7 place-items-center rounded-[6px] text-ink-3 hover:bg-danger-wash hover:text-danger"
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
              <div className="border-b border-rule px-6 py-12 text-center">
                <p className="text-md text-ink">{filter ? "Nothing matches that filter." : "This folder is empty."}</p>
                {writer && !filter && <p className="mt-1 text-base text-ink-3">Drop files anywhere on this page to upload them here.</p>}
              </div>
            )}
            {next && (
              <div className="py-3 text-center">
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
            {list.isSuccess && (shownFolders.length > 0 || shownObjects.length > 0) && writer && (
              <p className="mt-3 text-sm text-ink-3 max-sm:hidden">
                Drop files anywhere on this page to upload them into <span className="font-mono text-ink-2">{prefix || "the top level"}</span>.
                Deleting a file is immediate: single files skip the trash.
              </p>
            )}
          </div>

          <aside className="min-w-0 max-lg:order-first lg:sticky lg:top-6 lg:self-start" aria-label="Preview">
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
              <div className="hidden border-y border-dashed border-rule-3 px-5 py-10 text-center lg:block">
                <p className="text-base text-ink-2">Pick a file to preview it.</p>
                <p className="mt-1 text-sm text-ink-3">Images and text show here; PDFs and anything else open in a new tab with a signed link.</p>
              </div>
            )}
          </aside>
        </div>
      </Page>

      {dragging && (
        <div className="pointer-events-none fixed inset-0 z-40 grid place-items-center bg-[oklch(0.2_0.01_60/0.28)] p-6 animate-fade">
          <div className="grid place-items-center rounded-[14px] border-2 border-dashed border-brass bg-paper-raised px-16 py-12 text-center shadow-overlay animate-pop">
            <Upload className="size-6 text-brass-ink" />
            <p className="mt-3 text-xl font-[550] text-ink">Drop to upload</p>
            <p className="mt-1 font-mono text-sm text-ink-3">
              into {bucket}/{prefix}
            </p>
          </div>
        </div>
      )}

      {uploads.length > 0 && (
        <div
          className="fixed right-4 bottom-4 z-40 w-[min(22rem,calc(100vw-2rem))] overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised shadow-overlay animate-pop"
          role="status"
        >
          <p className="label border-b border-rule px-4 py-2.5">Uploads</p>
          <ul className="max-h-60 divide-y divide-rule overflow-y-auto">
            {uploads.map((u) => (
              <li key={u.key} className="flex items-start gap-3 px-4 py-2 text-sm">
                <PilotLight className="mt-1.5" state={u.state === "done" ? "on" : u.state === "failed" ? "fault" : "busy"} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-mono text-xs text-ink">{u.name}</span>
                  {u.error && <span className="block text-xs text-danger">{u.error}</span>}
                </span>
                <span className="text-xs text-ink-3">{u.state === "uploading" ? "uploading…" : u.state === "done" ? "done" : "failed"}</span>
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
      className="grid size-7 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-press hover:text-ink"
    >
      {done ? <span className="text-xs text-ok">✓</span> : <Link2 className="size-3.5" />}
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
  const [copied, setCopied] = useState(false);
  const [dims, setDims] = useState<string | null>(null);
  const name = o.key.split("/").pop() ?? o.key;
  const publicUrl = bucket?.public && bucket.publicUrl ? `${bucket.publicUrl}/${o.key.split("/").map(encodeURIComponent).join("/")}` : undefined;
  const open = async () => {
    const p = link.data ?? (await link.mutateAsync());
    window.open(p.url, "_blank", "noopener,noreferrer");
  };

  return (
    <div className="overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)] animate-pop">
      <div className="flex items-center gap-2 border-b border-rule py-1.5 pr-1.5 pl-3.5">
        <FileIcon name={name} />
        <p className="min-w-0 flex-1 truncate font-mono text-[0.8125rem] text-ink">{name}</p>
        <button
          onClick={onClose}
          aria-label="Close preview"
          className="grid size-7 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-sunk hover:text-ink"
        >
          <X className="size-4" />
        </button>
      </div>
      <div className="bg-paper-sunk">
        {kind === "image" && content.data?.base64 && (
          <div className="grid min-h-48 place-items-center bg-[repeating-conic-gradient(var(--paper-press)_0_25%,transparent_0_50%)] bg-[length:14px_14px] p-5">
            <img
              src={`data:${content.data.contentType};base64,${content.data.base64}`}
              alt={name}
              onLoad={(e) => setDims(`${int(e.currentTarget.naturalWidth)} × ${int(e.currentTarget.naturalHeight)}`)}
              className="max-h-72 object-contain"
            />
          </div>
        )}
        {kind === "text" && content.data && (
          <Untrusted className="rounded-none border-0" label="File contents, shown as plain text">
            <pre className="max-h-80 overflow-auto px-3.5 py-3 font-mono text-[0.75rem] leading-5 whitespace-pre-wrap text-ink-2">
              {content.data.text != null ? prettyText(o.key, content.data.text) : "(binary)"}
            </pre>
          </Untrusted>
        )}
        {content.isPending && content.fetchStatus !== "idle" && <Skeleton className="m-4 h-40" />}
        {(kind === "pdf" || kind === "file" || !small) && (
          <div className="px-5 py-8 text-center">
            <FileIcon name={name} className="mx-auto size-6" />
            <p className="mt-3 text-sm text-ink-2">
              {kind === "pdf"
                ? "PDFs open in a new tab, with a link that works for an hour."
                : !small
                  ? "Too big to preview here."
                  : "No preview for this kind of file."}
            </p>
            {kind === "pdf" && (
              <Button size="sm" className="mt-3" onClick={open}>
                <ArrowUpRight />
                Open {name}
              </Button>
            )}
          </div>
        )}
        {content.isError && <ProblemNote className="m-4" error={content.error} />}
      </div>
      <dl className="grid grid-cols-[5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 border-t border-rule px-3.5 py-3 text-sm">
        <dt className="text-ink-3">Size</dt>
        <dd className="text-ink-2 tnum">
          {bytes(o.size)}
          {o.size >= 1024 && <span className="text-ink-3"> ({int(o.size)} bytes)</span>}
          {dims && <span className="text-ink-3"> · {dims} px</span>}
        </dd>
        <dt className="text-ink-3">Modified</dt>
        <dd className="text-ink-2" title={full(o.lastModified)}>
          {relative(o.lastModified)}
        </dd>
        <dt className="text-ink-3">Key</dt>
        <dd className="min-w-0 truncate font-mono text-xs leading-[1.1875rem] text-ink-2" title={o.key}>
          {o.key}
        </dd>
        <dt className="text-ink-3">ETag</dt>
        <dd className="truncate font-mono text-xs leading-[1.1875rem] text-ink-3">{o.etag.replace(/"/g, "")}</dd>
      </dl>
      <div className="flex flex-wrap items-center gap-2 border-t border-rule px-3.5 py-3">
        <Button size="sm" variant="secondary" onClick={open}>
          <ArrowUpRight />
          Open
        </Button>
        <Button
          size="sm"
          variant="ghost"
          onClick={async () => {
            const p = link.data ?? (await link.mutateAsync());
            if (await copyText(p.url)) {
              setCopied(true);
              setTimeout(() => setCopied(false), 1600);
            }
          }}
        >
          <Link2 />
          {copied ? "Copied, works for an hour" : "Copy link"}
        </Button>
        {publicUrl && <CopyButton value={publicUrl} label="Copy public URL" />}
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
