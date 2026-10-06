import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowLeft, ArrowUpRight, Download, FolderInput, Link2, Pencil, Trash2, X } from "lucide-react";
import { useState } from "react";
import { mod, type StorageBucket, type StorageObject } from "@/api/modules";
import { CopyButton } from "@/components/copy";
import { MiniSelect } from "@/components/data-parts";
import { Skeleton, Untrusted } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { bytes, int } from "@/lib/format";
import { full, relative } from "@/lib/time";
import { Segmented } from "@/routes/kv/parts";
import { FileGlyph } from "./browser";
import { baseName, EXPIRIES, kindOf, QUALITIES, resizable, WIDTHS } from "./words";

const shortWhen = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

/** Pretty-prints JSON files; everything else stays exactly as stored. */
function prettyText(key: string, text: string) {
  if (!/\.json$/i.test(key)) return text;
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

/** One file: a preview, what it is, links to it, and (images) a resized-link builder. */
export function FilePanel({
  project,
  bucket,
  o,
  transforms,
  onClose,
  onRename,
  onMove,
  onDelete,
}: {
  project: string;
  bucket: StorageBucket;
  o: StorageObject;
  transforms: boolean;
  onClose: () => void;
  onRename?: () => void;
  onMove?: () => void;
  onDelete?: () => void;
}) {
  const name = baseName(o.key);
  const kind = kindOf(o.key);
  const url = (p: { w?: number; q?: number; f?: string; download?: boolean } = {}) => mod.fileUrl(project, bucket.name, o.key, p);
  // The stored type, from the first byte (listings don't carry it).
  const head = useQuery({
    queryKey: ["file-head", project, bucket.name, o.key, o.etag],
    queryFn: async () => {
      const r = await fetch(url(), { headers: { Range: "bytes=0-0" }, credentials: "same-origin" });
      return { type: r.ok ? (r.headers.get("Content-Type") ?? "") : "" };
    },
    staleTime: Infinity,
  });
  const small = o.size <= 1024 * 1024;
  const text = useQuery({
    queryKey: ["object", project, bucket.name, o.key, o.etag],
    queryFn: () => mod.object(project, bucket.name, o.key),
    enabled: kind === "text" && small,
    staleTime: Infinity,
  });
  const [dims, setDims] = useState<string | null>(null);
  const resize = transforms && resizable(o.key);
  // A big image previews resized; a small one as stored, so its size in pixels is true.
  const previewSrc = resize && o.size > 1.5 * 1024 * 1024 ? url({ w: 828, q: 75, f: "webp" }) : url();

  return (
    <section aria-labelledby="file-name" className="overflow-hidden rounded-[12px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)]">
      <div className="flex items-center gap-2 border-b border-rule py-1.5 pr-1.5 pl-2.5">
        <button
          type="button"
          onClick={onClose}
          aria-label="Back to the files"
          className="grid size-7 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-sunk hover:text-ink lg:hidden"
        >
          <ArrowLeft className="size-4" />
        </button>
        <FileGlyph name={name} className="max-lg:hidden" />
        <h2 id="file-name" className="min-w-0 flex-1 truncate font-mono text-[0.8125rem] text-ink" title={o.key}>
          {o.key}
        </h2>
        <CopyButton value={o.key} label="Copy the file's key" className="size-7" />
        <button
          type="button"
          onClick={onClose}
          aria-label="Close (Esc)"
          title="Close (Esc)"
          className="grid size-7 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-sunk hover:text-ink max-lg:hidden"
        >
          <X className="size-4" />
        </button>
      </div>

      <div className="bg-paper-sunk">
        {kind === "image" && (
          <div className="grid min-h-48 place-items-center bg-[repeating-conic-gradient(var(--paper-press)_0_25%,transparent_0_50%)] bg-[length:14px_14px] p-4">
            <img
              src={previewSrc}
              alt={`Preview of ${name}`}
              onLoad={(e) => previewSrc === url() && setDims(`${int(e.currentTarget.naturalWidth)}×${int(e.currentTarget.naturalHeight)}`)}
              className="max-h-72 max-w-full object-contain"
            />
          </div>
        )}
        {kind === "video" && (
          <video controls preload="metadata" src={url()} className="block max-h-80 w-full bg-ink" aria-label={`Video ${name}`}>
            <track kind="captions" />
          </video>
        )}
        {kind === "audio" && (
          <div className="px-4 py-6">
            <audio controls preload="metadata" src={url()} className="w-full" aria-label={`Audio ${name}`} />
          </div>
        )}
        {kind === "pdf" && <iframe src={url()} title={`PDF ${name}`} className="block h-96 w-full bg-paper" />}
        {kind === "text" && small && text.data && (
          <Untrusted className="rounded-none border-0" label="File contents, shown as plain text">
            <pre tabIndex={0} aria-label="File contents" className="max-h-80 overflow-auto px-3.5 py-3 font-mono text-[0.75rem] leading-5 whitespace-pre-wrap text-ink-2">
              {text.data.text != null ? prettyText(o.key, text.data.text) : "This file isn’t text."}
            </pre>
          </Untrusted>
        )}
        {kind === "text" && small && text.isPending && <Skeleton className="m-4 h-32" />}
        {text.isError && <ProblemNote className="m-4" error={text.error} />}
        {(kind === "file" || (kind === "text" && !small)) && (
          <div className="px-5 py-8 text-center">
            <FileGlyph name={name} className="mx-auto size-6" />
            <p className="mt-2 text-sm text-ink-2">
              {kind === "text" ? "Too big to show here. Download it to read it." : "No preview for this kind of file."}
            </p>
          </div>
        )}
      </div>

      <dl className="grid grid-cols-[5.5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 border-t border-rule px-4 py-3 text-sm">
        <dt className="text-ink-3">Size</dt>
        <dd className="text-ink-2 tnum">
          {bytes(o.size)}
          {dims && <span className="text-ink-3"> · {dims} px</span>}
          {o.size >= 1024 && <span className="block text-xs text-ink-3">{int(o.size)} bytes</span>}
        </dd>
        <dt className="text-ink-3">Type</dt>
        <dd className="truncate font-mono text-[0.78125rem] leading-5 text-ink-2">{head.data?.type || "…"}</dd>
        <dt className="text-ink-3">Modified</dt>
        <dd className="text-ink-2" title={full(o.lastModified)}>
          {relative(o.lastModified)}
        </dd>
        <dt className="text-ink-3">ETag</dt>
        <dd className="truncate font-mono text-[0.78125rem] leading-5 text-ink-3" title={o.etag}>
          {o.etag.replace(/"/g, "")}
        </dd>
      </dl>

      <div className="flex flex-wrap items-center gap-1.5 border-t border-rule px-4 py-3">
        <Button size="sm" asChild>
          <a href={url({ download: true })} download={name}>
            <Download />
            Download
          </a>
        </Button>
        {(kind === "pdf" || kind === "image" || kind === "video") && (
          <Button size="sm" variant="ghost" asChild>
            <a href={url()} target="_blank" rel="noreferrer">
              <ArrowUpRight />
              Open
            </a>
          </Button>
        )}
        {onRename && (
          <Button size="sm" variant="ghost" onClick={onRename} title="Rename (F2)">
            <Pencil />
            Rename
          </Button>
        )}
        {onMove && (
          <Button size="sm" variant="ghost" onClick={onMove}>
            <FolderInput />
            Move
          </Button>
        )}
        {onDelete && (
          <Button size="sm" variant="danger-quiet" className="ml-auto" onClick={onDelete} title="Delete (Delete)">
            <Trash2 />
            Delete
          </Button>
        )}
      </div>

      <LinkPart project={project} bucket={bucket} o={o} />
      {kind === "image" && resize && <Resizer project={project} bucket={bucket} o={o} />}
    </section>
  );
}

/** The link to the file: plain for a public bucket, signed with an expiry for a private one. */
function LinkPart({ project, bucket, o }: { project: string; bucket: StorageBucket; o: StorageObject }) {
  const [ttl, setTtl] = useState(EXPIRIES[0].seconds);
  const [copied, setCopied] = useState<string | null>(null);
  const make = useMutation({
    mutationFn: () => mod.fileLink(project, bucket.name, { key: o.key, expiresIn: ttl }),
    onSuccess: async (l) => {
      if (await copyText(l.url)) setCopied(l.expiresAt ?? "");
    },
  });
  if (bucket.public && bucket.publicUrl)
    return (
      <div className="border-t border-rule px-4 py-3">
        <p className="label">Public link</p>
        <div className="mt-1.5 flex items-center gap-1 rounded-[8px] border border-rule bg-paper-sunk py-1 pr-1 pl-2.5">
          <code className="min-w-0 flex-1 truncate font-mono text-[0.75rem] text-ink-2">{publicLink(bucket, o.key)}</code>
          <CopyButton value={publicLink(bucket, o.key)} label="Copy the public link" className="size-7" />
        </div>
      </div>
    );
  return (
    <div className="border-t border-rule px-4 py-3">
      <p className="label">Private link</p>
      <p className="mt-1 text-sm text-ink-3">Anyone with the link can open the file until it expires.</p>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <label className="flex items-center gap-2 text-sm text-ink-2">
          Works for
          <MiniSelect
            aria-label="How long the link works"
            value={String(ttl)}
            onChange={(e) => setTtl(Number(e.target.value))}
            className="w-24 [&_select]:font-sans"
          >
            {EXPIRIES.map((x) => (
              <option key={x.seconds} value={x.seconds}>
                {x.label}
              </option>
            ))}
          </MiniSelect>
        </label>
        <Button size="sm" onClick={() => make.mutate()} disabled={make.isPending}>
          <Link2 />
          {copied !== null ? "Copied" : "Copy private link"}
        </Button>
      </div>
      {copied && <p className="mt-1.5 text-xs text-ink-3">Works until {shortWhen.format(new Date(copied))}.</p>}
      {make.isError && <ProblemNote className="mt-2" error={make.error} />}
    </div>
  );
}

export function publicLink(bucket: StorageBucket, key: string, q = "") {
  return `${bucket.publicUrl}/${key.split("/").map(encodeURIComponent).join("/")}${q}`;
}

const QUICK_WIDTHS = [0, 640, 1080, 1920];

/** Width, quality and format for a resized copy: a live preview, its size, the link and the next/image snippet. */
function Resizer({ project, bucket, o }: { project: string; bucket: StorageBucket; o: StorageObject }) {
  const [w, setW] = useState(1080);
  const [q, setQ] = useState(75);
  const [f, setF] = useState<"webp" | "avif" | "original">("webp");
  const params = { w: w || undefined, q, f };
  const shown = useQuery({
    queryKey: ["file-resized", project, bucket.name, o.key, o.etag, w, q, f],
    queryFn: async () => {
      // Fetched once for its size; the preview then comes from the browser's cache (the page's CSP has no blob: images).
      const src = mod.fileUrl(project, bucket.name, o.key, params);
      const r = await fetch(src, { credentials: "same-origin" });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      const blob = await r.blob();
      return { size: blob.size, src, type: blob.type };
    },
    staleTime: Infinity,
    gcTime: 60_000,
    placeholderData: (prev) => prev,
  });
  const qs = `?${[w && `w=${w}`, q !== 75 && `q=${q}`, `f=${f}`].filter(Boolean).join("&")}`;
  const signed = useQuery({
    queryKey: ["file-link", project, bucket.name, o.key, w, q, f],
    queryFn: () => mod.fileLink(project, bucket.name, { key: o.key, w: w || undefined, q, f, expiresIn: 3600 }),
    enabled: !bucket.public,
    staleTime: 30 * 60_000,
  });
  const link = bucket.public ? publicLink(bucket, o.key, qs) : signed.data?.url;
  const src = bucket.public ? `publicUrl("${bucket.name}", "${o.key}")` : `signedUrl("${bucket.name}", "${o.key}")`;
  const snippet = `// image-loader.ts
export { default } from "@shiptiffin/sdk/next/image-loader";

// next.config.ts: images: { loader: "custom", loaderFile: "./image-loader.ts" }

import { ${bucket.public ? "publicUrl" : "signedUrl"} } from "@shiptiffin/sdk/storage";
<Image src={${src}} width={${w || 1200}} height={…} alt="" />`;
  const saved = shown.data && o.size > 0 ? 1 - shown.data.size / o.size : 0;

  return (
    <div className="border-t border-rule px-4 py-3">
      <p className="label">Resized link</p>
      <div className="mt-2 space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="w-14 text-sm text-ink-3">Width</span>
          <Segmented
            label="Width"
            value={String(QUICK_WIDTHS.includes(w) ? w : "more")}
            onChange={(v) => v !== "more" && setW(Number(v))}
            options={[
              ...QUICK_WIDTHS.map((x) => ({ value: String(x), label: x ? String(x) : "Full" })),
              ...(QUICK_WIDTHS.includes(w) ? [] : [{ value: "more", label: String(w) }]),
            ]}
          />
          <MiniSelect aria-label="Another width" value="" onChange={(e) => e.target.value && setW(Number(e.target.value))} className="w-[4.5rem]">
            <option value="">More</option>
            {WIDTHS.map((x) => (
              <option key={x} value={x}>
                {x}
              </option>
            ))}
          </MiniSelect>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <span className="w-14 text-sm text-ink-3">Quality</span>
          <Segmented
            label="Quality"
            value={String(q)}
            onChange={(v) => setQ(Number(v))}
            options={QUALITIES.map((x) => ({ value: String(x), label: String(x) }))}
          />
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <span className="w-14 text-sm text-ink-3">Format</span>
          <Segmented
            label="Format"
            value={f}
            onChange={setF}
            options={[
              { value: "webp", label: "WebP" },
              { value: "avif", label: "AVIF" },
              { value: "original", label: "Same" },
            ]}
          />
        </div>
      </div>
      <div className="mt-3 grid grid-cols-[5.5rem_minmax(0,1fr)] items-center gap-3">
        <div className="grid size-[5.5rem] place-items-center overflow-hidden rounded-[8px] border border-rule bg-[repeating-conic-gradient(var(--paper-press)_0_25%,transparent_0_50%)] bg-[length:10px_10px]">
          {shown.data ? (
            <img
              src={shown.data.src}
              alt={`Resized preview of ${baseName(o.key)}`}
              className={cn("max-h-full max-w-full object-contain", shown.isFetching && "opacity-60")}
            />
          ) : (
            <Skeleton className="size-full" />
          )}
        </div>
        <p className="text-sm text-ink-2" aria-live="polite">
          {shown.isError ? (
            <span className="text-danger">{(shown.error as Error).message}</span>
          ) : shown.data ? (
            <>
              <span className="tnum">{bytes(shown.data.size)}</span>
              <span className="text-ink-3">
                {" "}
                {saved > 0.01 ? `· ${Math.round(saved * 100)}% smaller than the original` : "· no smaller than the original"}
              </span>
              <span className="block font-mono text-xs text-ink-3">{shown.data.type}</span>
            </>
          ) : (
            "Resizing…"
          )}
        </p>
      </div>
      <div className="mt-3 flex items-center gap-1 rounded-[8px] border border-rule bg-paper-sunk py-1 pr-1 pl-2.5">
        <code className="min-w-0 flex-1 font-mono text-[0.75rem] break-all text-ink-2">{link ?? "…"}</code>
        {link && <CopyButton value={link} label="Copy the resized link" className="size-7 shrink-0 self-start" />}
      </div>
      {!bucket.public && <p className="mt-1 text-xs text-ink-3">Private bucket: this link works for an hour. In your app, signedUrl() makes them.</p>}
      <details className="group mt-3">
        <summary className="cursor-pointer text-sm text-ink-2 hover:text-ink">Use it with next/image</summary>
        <div className="mt-2 overflow-hidden rounded-[8px] border border-rule-2 bg-paper-sunk">
          <div className="flex items-center justify-between border-b border-rule py-1 pr-1 pl-3">
            <span className="ident text-ink-3">image-loader.ts</span>
            <CopyButton value={snippet} label="Copy the snippet" className="size-7" />
          </div>
          <pre tabIndex={0} aria-label="next/image snippet" className="overflow-x-auto px-3 py-2.5 font-mono text-[0.71875rem] leading-5 text-ink-2">
            <code>{snippet}</code>
          </pre>
        </div>
      </details>
    </div>
  );
}
