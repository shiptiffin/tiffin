import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ChevronRight, Globe, Lock, Plus, Settings2 } from "lucide-react";
import { useState } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { mod, mq, type StorageBucket, type StorageInfo, type TrashEntry } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { ConnectButton } from "@/components/connect";
import { FilesAccess } from "@/components/files-access";
import { Reading, Readings, Rows, Section } from "@/components/data-parts";
import { useTitle } from "@/components/favicon";
import { Crumbs, Empty, NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { ReadOnlyBanner } from "@/components/read-only";
import { SegMeter } from "@/components/seg-meter";
import { Button } from "@/components/ui/button";
import { Radio, RadioGroup } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { bytes, bytesParts, count, int, pct } from "@/lib/format";
import { useMe } from "@/lib/me";
import { PARTS } from "@/lib/names";
import { requestCommand, useCommand } from "@/lib/shortcuts";
import { change, pendingFor, usePending } from "@/lib/staged";
import { full, relative } from "@/lib/time";
import { BucketSettings } from "./settings";
import { BUCKET_NAME, typesWords } from "./words";

export { BucketPage } from "./bucket";

const shortDate = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", year: "numeric" });
const shortDay = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" });
const LEDE = "S3-compatible buckets.";

/**
 * Files: the project's buckets. Each row says who can read it, how big it
 * is and when it was made; New bucket, its settings and deleting it are
 * changes to tiffin.config.ts like any other (deleting asks first, naming
 * what goes). Connect shows the env apps already have.
 */
export function FilesPage({ project, isNew }: { project: string; isNew?: boolean }) {
  useTitle(`${project} · ${PARTS.storage.name}`);
  const info = useQuery(mq.storage(project));
  const trash = useQuery(mq.trash(project));
  const { can } = useMe();
  const [making, setMaking] = useState(!!isNew);
  const [settings, setSettings] = useState<string | null>(null);
  const writer = can("apply:reversible");
  const missing = info.error instanceof ApiError && info.error.status === 404;

  const navigate = useNavigate();
  useCommand(writer ? { id: "new-bucket", label: "New bucket", keys: "n", keywords: ["create", "files", "s3"], run: () => setMaking(true) } : null);
  // ⌘K's "Upload a file" lands here: go to the bucket (the only one, or the first) and upload there.
  useCommand(
    writer
      ? {
          id: "upload-file",
          label: "Upload files",
          run: () => {
            const first = info.data?.buckets?.[0];
            if (!first) return setMaking(true);
            requestCommand("upload-file");
            void navigate({ to: "/projects/$project/storage/$bucket", params: { project, bucket: first.name } });
          },
        }
      : null,
  );

  if (info.isError && notOnBox(info.error)) return <NotOnBox what="Buckets" />;
  const s = info.data;
  const buckets = s?.buckets ?? [];
  const open = buckets.find((b) => b.name === settings);

  return (
    <Page wide>
      <PageHeader
        eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project }, mono: true }]} />}
        title={PARTS.storage.name}
        lede={LEDE}
        actions={
          <>
            {!missing && <ConnectButton part="files" project={project} />}
            {writer && (
              <Button variant="primary" onClick={() => setMaking(true)} title="New bucket (N)">
                <Plus />
                New bucket
              </Button>
            )}
          </>
        }
      />
      <ReadOnlyBanner project={project} className="mt-6" />
      {info.isPending && (
        <div className="mt-8 space-y-6" aria-busy>
          <Skeleton className="h-20 w-full" />
          <Skeleton className="h-40 w-full" />
        </div>
      )}
      {info.isError && !missing && <ProblemNote className="mt-8" error={info.error} title="Couldn’t load the files" />}
      {missing && (
        <Empty className="mt-10" title={`${project} keeps no files yet`}>
          A bucket holds files your app stores: uploads, images, exports. Make one and your apps get its keys at once.
          {writer && (
            <span className="mt-4 block">
              <Button variant="primary" onClick={() => setMaking(true)}>
                <Plus />
                New bucket
              </Button>
            </span>
          )}
        </Empty>
      )}
      {s && <Usage s={s} buckets={buckets} />}
      {s && (
        <Section className="mt-10" id="buckets" label="Buckets">
          {buckets.length === 0 ? (
            <Empty title="No buckets yet">A bucket holds files your app stores. Make one with New bucket; your apps get its keys at once.</Empty>
          ) : (
            <Rows>
              <li aria-hidden className="hidden grid-cols-[minmax(0,1fr)_9rem_6rem_6rem_7rem_2rem] gap-x-5 py-2 sm:grid">
                <span className="label">Bucket</span>
                <span className="label">Access</span>
                <span className="label text-right">Size</span>
                <span className="label text-right">Files</span>
                <span className="label">Made</span>
                <span />
              </li>
              {buckets.map((b) => (
                <BucketRow key={b.name} project={project} b={b} onSettings={writer ? () => setSettings(b.name) : undefined} />
              ))}
            </Rows>
          )}
        </Section>
      )}
      {s && buckets.length > 0 && <FilesAccess project={project} s={s} />}
      {(trash.data ?? []).length > 0 && (
        <TrashList project={project} entries={trash.data ?? []} canPurge={can("apply:irreversible")} canRestore={writer} />
      )}
      <NewBucket project={project} taken={buckets.map((b) => b.name)} open={making} onOpenChange={setMaking} />
      {open && <BucketSettings project={project} b={open} open={!!open} onOpenChange={(o) => !o && setSettings(null)} />}
    </Page>
  );
}

function Usage({ s, buckets }: { s: StorageInfo; buckets: StorageBucket[] }) {
  const quota = s.quotaBytes;
  const used = bytesParts(s.filesBytes);
  const files = buckets.reduce((n, b) => n + b.objects, 0);
  const share = quota > 0 ? s.usedBytes / quota : 0;
  return (
    <Readings className="mt-8 grid-cols-2 lg:grid-cols-[1.6fr_1fr_1fr]">
      <Reading
        className="col-span-2 lg:col-span-1"
        label="Stored"
        value={used.value}
        unit={`${used.unit}${quota > 0 ? ` · limit ${bytes(quota, 0)} with the database` : ""}`}
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
          {quota > 0 ? `${pct(share, share < 0.1 ? 1 : 0)} of the limit, counting the database. ` : "No storage limit. "}Measured{" "}
          {relative(s.measuredAt)}.
        </p>
      </Reading>
      <Reading
        label="Buckets"
        value={int(buckets.length)}
        sub={buckets.length ? `${int(buckets.filter((b) => b.public).length)} public` : undefined}
      />
      <Reading label="Files" value={int(files)} sub={s.imageTransforms ? "Images resize on the fly" : undefined} />
    </Readings>
  );
}

function BucketRow({ project, b, onSettings }: { project: string; b: StorageBucket; onSettings?: () => void }) {
  const edits = usePending(project);
  const staged = pendingFor(edits, `bucket:${b.name}`);
  const isPublic = staged?.kind === "bucket" && staged.to !== "absent" ? staged.to === "public" : b.public;
  const rules = [
    b.maxFileSize ? `up to ${bytes(b.maxFileSize, 0)}` : "",
    b.allowedTypes?.length ? typesWords(b.allowedTypes).toLowerCase() : "",
  ].filter(Boolean);
  return (
    <li>
      <div className="group relative grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-5 gap-y-1 py-3 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover sm:-mx-3 sm:grid-cols-[minmax(0,1fr)_9rem_6rem_6rem_7rem_2rem] sm:px-3">
        <span className="min-w-0">
          <Link
            to="/projects/$project/storage/$bucket"
            params={{ project, bucket: b.name }}
            className="font-mono text-[0.875rem] text-ink outline-hidden after:absolute after:inset-0 after:content-[''] focus-visible:after:rounded-[8px] focus-visible:after:shadow-[inset_0_0_0_2px_var(--focus)]"
          >
            {b.name}
          </Link>
          {b.state === "pending" && <span className="ml-2 text-sm text-brass-ink">being made…</span>}
          {staged?.kind === "bucket" && staged.to === "absent" && <span className="ml-2 text-sm text-brass-ink">deleting…</span>}
          <span className="mt-0.5 block truncate text-sm text-ink-3">
            <span className="sm:hidden">{isPublic ? "Public · " : "Private · "}</span>
            {rules.length ? `Takes ${rules.join(", ")}` : "Takes any file"}
          </span>
        </span>
        <span
          className={cn("inline-flex items-center gap-1.5 text-sm max-sm:hidden", isPublic ? "text-ink" : "text-ink-2")}
          title={isPublic ? "Anyone with a file's address can open it" : "Files open only with a signed link that expires"}
        >
          {staged?.kind === "bucket" && staged.to !== "absent" ? (
            <span className="text-brass-ink">{isPublic ? "Making it public…" : "Making it private…"}</span>
          ) : (
            <>
              {isPublic ? <Globe className="size-3.5 text-brass-ink" aria-hidden /> : <Lock className="size-3.5 text-ink-3" aria-hidden />}
              {isPublic ? "Public" : "Private"}
            </>
          )}
        </span>
        <span className="col-start-1 text-sm text-ink-2 tnum sm:col-start-auto sm:text-right sm:text-base">
          {bytes(b.bytes)}
          <span className="text-ink-3 sm:hidden"> · {count(b.objects, "file")}</span>
        </span>
        <span className="hidden text-right text-sm text-ink-3 tnum sm:block">{int(b.objects)}</span>
        <span className="hidden text-sm text-ink-3 sm:block" title={b.createdAt ? full(b.createdAt) : undefined}>
          {b.createdAt ? shortDate.format(new Date(b.createdAt)) : "—"}
        </span>
        <span className="relative z-[1] hidden items-center justify-end sm:flex">
          {onSettings ? (
            <button
              type="button"
              onClick={onSettings}
              aria-label={`${b.name} settings`}
              title="Settings"
              className="grid size-7 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-press hover:text-ink"
            >
              <Settings2 className="size-4" />
            </button>
          ) : (
            <ChevronRight className="size-4 text-ink-4" />
          )}
        </span>
      </div>
    </li>
  );
}

/** A name and who can read it; the bucket is a change like any other. */
function NewBucket({ project, taken, open, onOpenChange }: { project: string; taken: string[]; open: boolean; onOpenChange: (o: boolean) => void }) {
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [access, setAccess] = useState<"private" | "public">("private");
  const n = name.trim();
  const problem = !n
    ? ""
    : !BUCKET_NAME.test(n)
      ? "Use lowercase letters, digits and dashes, starting with a letter."
      : taken.includes(n)
        ? `${project} already has a bucket called ${n}.`
        : `${project}-${n}`.length > 63
          ? "That name is too long for this project."
          : "";
  const make = () => {
    if (!n || problem) return;
    change(
      project,
      {
        kind: "set",
        path: ["services", "storage", "buckets", n],
        to: { public: access === "public" },
        what: `Add the ${n} bucket to ${project}`,
        undo: `the ${n} bucket is removed again`,
      },
      { immediate: true },
    );
    onOpenChange(false);
    setName("");
    void navigate({ to: "/projects/$project/storage/$bucket", params: { project, bucket: n } });
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            make();
          }}
        >
          <DialogHeader>
            <DialogTitle>New bucket</DialogTitle>
            <DialogDescription>Your apps get its name as S3_BUCKET_{(n || "name").toUpperCase().replace(/-/g, "_")} straight away.</DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-5">
            <label className="block">
              <span className="text-sm text-ink-2">Name</span>
              <Input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value.toLowerCase())}
                placeholder="uploads"
                spellCheck={false}
                aria-invalid={!!problem || undefined}
                aria-describedby="bucket-name-note"
                className="mt-1 font-mono"
              />
              <span id="bucket-name-note" className={problem ? "mt-1 block text-sm text-danger" : "mt-1 block text-xs text-ink-3"}>
                {problem || "Lowercase letters, digits and dashes."}
              </span>
            </label>
            <fieldset>
              <legend className="text-sm text-ink-2">Who can read files</legend>
              <RadioGroup value={access} onValueChange={(v) => setAccess(v as "private" | "public")} className="mt-2 space-y-2">
                <label className="flex items-start gap-2.5">
                  <Radio value="private" className="mt-0.5" />
                  <span className="text-base text-ink">
                    Only with a link that expires <span className="block text-sm text-ink-3">Right for uploads, invoices, anything personal.</span>
                  </span>
                </label>
                <label className="flex items-start gap-2.5">
                  <Radio value="public" className="mt-0.5" />
                  <span className="text-base text-ink">
                    Anyone with the link <span className="block text-sm text-ink-3">Right for images on public pages.</span>
                  </span>
                </label>
              </RadioGroup>
            </fieldset>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={!n || !!problem}>
              Make bucket
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function TrashList({ project, entries, canPurge, canRestore }: { project: string; entries: TrashEntry[]; canPurge: boolean; canRestore: boolean }) {
  const qc = useQueryClient();
  const edits = usePending(project);
  const [purging, setPurging] = useState<TrashEntry | null>(null);
  return (
    <Section className="mt-12" id="trash" label="Deleted buckets" aside="kept for 7 days, files and all">
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
                    {shortDay.format(new Date(t.expiresAt))}
                  </time>
                  .
                </p>
                {staged && <p className="mt-1 text-sm text-brass-ink">Bringing it back with its files…</p>}
              </div>
              <span className="flex shrink-0 gap-1">
                {canRestore && !staged && (
                  <Button
                    size="sm"
                    onClick={() => change(project, { kind: "bucket", bucket: t.bucket, from: "absent", to: "private" }, { immediate: true })}
                  >
                    Restore
                  </Button>
                )}
                {canPurge && (
                  <Button variant="danger-quiet" size="sm" onClick={() => setPurging(t)}>
                    Delete now
                  </Button>
                )}
              </span>
            </li>
          );
        })}
      </Rows>
      <Confirm
        open={!!purging}
        onClose={() => setPurging(null)}
        title={`Delete ${purging?.bucket ?? "this bucket"} for good?`}
        body={`Its ${purging ? `${count(purging.objects, "file")}, ${bytes(purging.bytes)},` : "files"} go now instead of in 7 days. Nothing can bring them back after this.`}
        action="Delete for good"
        run={() => mod.purge(purging!.id)}
        done={() => qc.invalidateQueries({ queryKey: ["trash"] })}
      />
    </Section>
  );
}
