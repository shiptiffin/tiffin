import { useSyncExternalStore } from "react";
import { Pause, Play, RotateCcw, X } from "lucide-react";
import { ApiError } from "@/api/client";
import { queryClient } from "@/api/queries";
import { mod, type StorageBucket } from "@/api/modules";
import { cn } from "@/lib/cn";
import { bytes, count } from "@/lib/format";
import { mimeOf, typeAllowed, typesWords } from "./words";

/**
 * Uploads from the console. Small files go in one request; anything over
 * 8 MB goes in parts through the API (the same multipart upload S3 clients
 * use), three parts at a time, each retried on a dropped connection. Pause
 * stops sending and keeps the parts already stored; Resume asks the box
 * which parts it has and sends the rest, even after a reload (the upload id
 * is remembered per file). Size and type rules are checked before a byte
 * moves. Uploads keep going while you browse other folders.
 */
export type Upload = {
  id: number;
  project: string;
  bucket: string;
  key: string;
  file: File;
  type: string;
  loaded: number;
  state: "waiting" | "uploading" | "paused" | "done" | "failed" | "cancelled";
  error?: string;
  uploadId?: string;
};

const SMALL = 8 * 1024 * 1024;
const FILES_AT_ONCE = 2;
const PARTS_AT_ONCE = 3;

let uploads: Upload[] = [];
let seq = 0;
const subs = new Set<() => void>();
const live = new Map<number, Set<XMLHttpRequest>>();
const progress = new Map<number, Map<number, number>>(); // bytes in flight per part

function set(next: Upload[]) {
  uploads = next;
  subs.forEach((f) => f());
}
function patch(id: number, p: Partial<Upload>) {
  set(uploads.map((u) => (u.id === id ? { ...u, ...p } : u)));
}
const get = (id: number) => uploads.find((u) => u.id === id);

const subscribe = (f: () => void) => {
  subs.add(f);
  return () => subs.delete(f);
};

/** This bucket's uploads, newest first. */
export function useUploads(project: string, bucket: string): Upload[] {
  const all = useSyncExternalStore(subscribe, () => uploads);
  return all.filter((u) => u.project === project && u.bucket === bucket);
}

const resumeKey = (u: Upload) => `files-upload:${u.project}/${u.bucket}/${u.key}:${u.file.size}:${u.file.lastModified}`;
const remember = (u: Upload, id?: string) => {
  try {
    if (id) localStorage.setItem(resumeKey(u), id);
    else localStorage.removeItem(resumeKey(u));
  } catch {
    // private windows: resuming after a reload just starts again
  }
};
const recall = (u: Upload) => {
  try {
    return localStorage.getItem(resumeKey(u)) ?? undefined;
  } catch {
    return undefined;
  }
};

/** Why a bucket won't take a file, before uploading it, or "". */
export function refusal(b: StorageBucket | undefined, file: File): string {
  if (!b) return "";
  if (b.maxFileSize && file.size > b.maxFileSize)
    return `Too big: ${b.name} takes files up to ${bytes(b.maxFileSize)}, and this one is ${bytes(file.size)}.`;
  const type = mimeOf(file.name, file.type);
  if (!typeAllowed(b.allowedTypes, type)) return `${b.name} takes ${typesWords(b.allowedTypes).toLowerCase()} only; this is ${type}.`;
  return "";
}

/** Queues files (each with its key) for a bucket and starts sending them. */
export function addUploads(project: string, b: StorageBucket | undefined, bucket: string, files: Array<{ file: File; key: string }>) {
  const added: Upload[] = files.map(({ file, key }) => {
    const why = refusal(b, file);
    return {
      id: ++seq,
      project,
      bucket,
      key,
      file,
      type: mimeOf(file.name, file.type),
      loaded: 0,
      state: why ? "failed" : "waiting",
      error: why || undefined,
    };
  });
  set([
    ...added.reverse(),
    ...uploads.filter((u) => !(u.project === project && u.bucket === bucket && u.state === "done" && added.some((a) => a.key === u.key))),
  ]);
  pump();
}

export function pause(id: number) {
  const u = get(id);
  if (!u || u.state !== "uploading") return;
  patch(id, { state: "paused" });
  live.get(id)?.forEach((x) => x.abort());
}

export function resume(id: number) {
  const u = get(id);
  if (!u || (u.state !== "paused" && u.state !== "failed")) return;
  patch(id, { state: "waiting", error: undefined });
  pump();
}

export async function cancel(id: number) {
  const u = get(id);
  if (!u) return;
  patch(id, { state: "cancelled" });
  live.get(id)?.forEach((x) => x.abort());
  remember(u);
  if (u.uploadId) await mod.uploadAbort(u.project, u.bucket, u.uploadId, u.key).catch(() => undefined);
  setTimeout(() => set(uploads.filter((x) => x.id !== id)), 1200);
}

/** Clears finished and failed uploads from the list. */
export function clearDone(project: string, bucket: string) {
  set(uploads.filter((u) => !(u.project === project && u.bucket === bucket && ["done", "failed", "cancelled"].includes(u.state))));
}

function pump() {
  const busy = uploads.filter((u) => u.state === "uploading").length;
  const next = uploads
    .filter((u) => u.state === "waiting")
    .reverse()
    .slice(0, Math.max(0, FILES_AT_ONCE - busy));
  for (const u of next) void send(u.id);
}

let refreshTimer: ReturnType<typeof setTimeout> | undefined;
function refreshSoon(project: string, bucket: string) {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => {
    void queryClient.invalidateQueries({ queryKey: ["objects", project, bucket] });
    void queryClient.invalidateQueries({ queryKey: ["storage", project] });
  }, 400);
}

async function send(id: number) {
  const u = get(id);
  if (!u) return;
  patch(id, { state: "uploading", error: undefined });
  progress.set(id, new Map());
  try {
    if (u.file.size <= SMALL) await sendSmall(u);
    else await sendParts(u);
    if (get(id)?.state !== "uploading") return;
    patch(id, { state: "done", loaded: u.file.size });
    remember(u);
    refreshSoon(u.project, u.bucket);
  } catch (e) {
    const now = get(id);
    if (!now || now.state !== "uploading") return; // paused or cancelled
    patch(id, { state: "failed", error: words(e) });
  } finally {
    live.delete(id);
    progress.delete(id);
    pump();
  }
}

function report(id: number) {
  const inFlight = [...(progress.get(id)?.values() ?? [])].reduce((a, b) => a + b, 0);
  const u = get(id);
  if (u) patch(id, { loaded: Math.min(u.file.size, done.get(id)! + inFlight) });
}
const done = new Map<number, number>(); // bytes of finished parts

async function sendSmall(u: Upload) {
  const fd = new FormData();
  fd.set("key", u.key);
  fd.set("file", new File([u.file], u.file.name, { type: u.type }));
  done.set(u.id, 0);
  await xhr(u.id, "POST", `/v1/projects/${encodeURIComponent(u.project)}/storage/buckets/${encodeURIComponent(u.bucket)}/objects`, fd, 0);
}

async function sendParts(u: Upload) {
  const size = u.file.size;
  let s;
  const stored = u.uploadId ?? recall(u);
  try {
    s = await mod.uploadStart(u.project, u.bucket, { key: u.key, size, contentType: u.type, uploadId: stored });
  } catch (e) {
    if (!(stored && e instanceof ApiError && e.status === 404)) throw e;
    remember(u); // that upload is gone: start again
    s = await mod.uploadStart(u.project, u.bucket, { key: u.key, size, contentType: u.type });
  }
  patch(u.id, { uploadId: s.uploadId });
  remember(u, s.uploadId);
  const partSize = s.partSize;
  const n = Math.ceil(size / partSize);
  const sizeOf = (i: number) => Math.min(partSize, size - (i - 1) * partSize);
  const have = new Set((s.parts ?? []).filter((p) => p.size === sizeOf(p.n)).map((p) => p.n));
  done.set(
    u.id,
    [...have].reduce((a, i) => a + sizeOf(i), 0),
  );
  report(u.id);
  const todo = Array.from({ length: n }, (_, i) => i + 1).filter((i) => !have.has(i));
  const worker = async () => {
    for (let i = todo.shift(); i !== undefined; i = todo.shift()) {
      if (get(u.id)?.state !== "uploading") return;
      const blob = u.file.slice((i - 1) * partSize, (i - 1) * partSize + sizeOf(i));
      await retry(() => xhr(u.id, "PUT", mod.uploadPartUrl(u.project, u.bucket, s.uploadId, i, u.key), blob, i), u.id);
      done.set(u.id, done.get(u.id)! + blob.size);
      progress.get(u.id)?.delete(i);
      report(u.id);
    }
  };
  await Promise.all(Array.from({ length: Math.min(PARTS_AT_ONCE, Math.max(1, todo.length)) }, worker));
  if (get(u.id)?.state !== "uploading") return;
  await retry(() => mod.uploadComplete(u.project, u.bucket, s.uploadId, u.key), u.id);
}

/** Dropped connections and busy servers get three more tries. */
async function retry<T>(fn: () => Promise<T>, id: number): Promise<T> {
  for (let i = 1; ; i++) {
    try {
      return await fn();
    } catch (e) {
      const again = !(e instanceof ApiError) || e.status === 0 || e.status === 429 || e.status >= 500;
      if (i >= 4 || !again || get(id)?.state !== "uploading") throw e;
      await new Promise((r) => setTimeout(r, 500 * 2 ** i));
    }
  }
}

/** One request with upload progress (fetch has none). */
function xhr(id: number, method: string, url: string, body: Blob | FormData, part: number): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const x = new XMLHttpRequest();
    x.open(method, url);
    x.setRequestHeader("Accept", "application/json");
    if (body instanceof Blob) x.setRequestHeader("Content-Type", "application/octet-stream");
    x.upload.onprogress = (e) => {
      progress.get(id)?.set(part, e.loaded);
      report(id);
    };
    const all = live.get(id) ?? new Set();
    all.add(x);
    live.set(id, all);
    const finish = () => all.delete(x);
    x.onload = () => {
      finish();
      let data: Record<string, unknown> = {};
      try {
        data = JSON.parse(x.responseText || "{}");
      } catch {
        // not JSON
      }
      if (x.status >= 200 && x.status < 300) return resolve(data);
      reject(
        new ApiError({
          status: x.status,
          code: (data.code ?? "internal") as ApiError["problem"]["code"],
          title: String(data.title ?? x.statusText),
          detail: data.detail as string | undefined,
          hint: data.hint as string | undefined,
        }),
      );
    };
    x.onerror = () => {
      finish();
      reject(new ApiError({ status: 0, code: "internal", title: "Offline", detail: "The connection to the box dropped." }));
    };
    x.onabort = () => {
      finish();
      reject(new DOMException("stopped", "AbortError"));
    };
    x.send(body);
  });
}

function words(e: unknown): string {
  if (e instanceof ApiError) return e.problem.detail ?? e.message;
  return e instanceof Error ? e.message : "The upload failed.";
}

/** "Uploading 2 files · 118 MB of 210 MB": the line a screen reader hears. */
export function summary(list: Upload[]): string {
  const going = list.filter((u) => u.state === "uploading" || u.state === "waiting");
  const failed = list.filter((u) => u.state === "failed").length;
  if (going.length === 0) {
    const ok = list.filter((u) => u.state === "done").length;
    return [ok && `Uploaded ${count(ok, "file")}`, failed && `${count(failed, "file")} didn’t upload`].filter(Boolean).join(" · ") || "";
  }
  const total = going.reduce((a, u) => a + u.file.size, 0);
  const sent = going.reduce((a, u) => a + u.loaded, 0);
  return `Uploading ${count(going.length, "file")} · ${bytes(sent)} of ${bytes(total)}`;
}

/** The uploads, bottom right: a summary for screen readers, a bar per file, pause, resume and cancel. */
export function UploadTray({ project, bucket }: { project: string; bucket: string }) {
  const list = useUploads(project, bucket);
  if (list.length === 0) return null;
  const going = list.some((u) => u.state === "uploading" || u.state === "waiting" || u.state === "paused");
  const total = list.reduce((a, u) => a + u.file.size, 0);
  const sent = list.reduce((a, u) => a + (u.state === "done" ? u.file.size : u.loaded), 0);
  return (
    <section
      aria-label="Uploads"
      className="fixed right-4 bottom-4 z-40 w-[min(24rem,calc(100vw-2rem))] overflow-hidden rounded-[12px] border border-rule-2 bg-paper-raised shadow-overlay animate-pop"
    >
      <div className="flex items-center gap-3 border-b border-rule px-4 py-2.5">
        <p className="min-w-0 flex-1 truncate text-sm font-[550] text-ink" role="status" aria-live="polite">
          {summary(list)}
        </p>
        {!going && (
          <button type="button" onClick={() => clearDone(project, bucket)} className="text-sm text-ink-3 hover:text-ink">
            Clear
          </button>
        )}
      </div>
      {going && (
        <div className="h-1 bg-paper-sunk" aria-hidden>
          <div
            className="h-full bg-brass transition-[width] duration-300 motion-reduce:transition-none"
            style={{ width: `${total ? (sent / total) * 100 : 0}%` }}
          />
        </div>
      )}
      <ul className="max-h-64 divide-y divide-rule overflow-y-auto">
        {list.map((u) => (
          <UploadRow key={u.id} u={u} />
        ))}
      </ul>
    </section>
  );
}

function UploadRow({ u }: { u: Upload }) {
  const pct = u.file.size ? Math.round((u.loaded / u.file.size) * 100) : 100;
  const big = u.file.size > SMALL;
  const state =
    u.state === "uploading"
      ? `${bytes(u.loaded)} of ${bytes(u.file.size)}`
      : u.state === "waiting"
        ? "Waiting"
        : u.state === "paused"
          ? `Paused at ${pct}%`
          : u.state === "done"
            ? bytes(u.file.size)
            : u.state === "cancelled"
              ? "Cancelled"
              : "Didn’t upload";
  const name = u.key.slice(u.key.lastIndexOf("/") + 1);
  return (
    <li className="px-4 py-2.5">
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-[0.78125rem] text-ink" title={u.key}>
          {name}
        </span>
        <span className={cn("shrink-0 text-xs tnum", u.state === "failed" ? "text-danger" : u.state === "done" ? "text-ok" : "text-ink-3")}>
          {state}
        </span>
        {u.state === "uploading" && big && (
          <IconButton label={`Pause ${name}`} onClick={() => pause(u.id)}>
            <Pause />
          </IconButton>
        )}
        {(u.state === "paused" || (u.state === "failed" && !!u.uploadId)) && (
          <IconButton label={`Resume ${name}`} onClick={() => resume(u.id)}>
            {u.state === "failed" ? <RotateCcw /> : <Play />}
          </IconButton>
        )}
        {(u.state === "uploading" || u.state === "waiting" || u.state === "paused") && (
          <IconButton label={`Cancel ${name}`} onClick={() => void cancel(u.id)}>
            <X />
          </IconButton>
        )}
      </div>
      {(u.state === "uploading" || u.state === "paused") && (
        <div
          role="progressbar"
          aria-label={`${name} upload`}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={pct}
          className="mt-1.5 h-1 overflow-hidden rounded-full bg-paper-sunk"
        >
          <div className={cn("h-full rounded-full", u.state === "paused" ? "bg-ink-4" : "bg-brass")} style={{ width: `${pct}%` }} />
        </div>
      )}
      {u.error && <p className="mt-1 text-xs text-danger">{u.error}</p>}
      {u.state === "uploading" && big && pct < 100 && (
        <p className="mt-1 text-xs text-ink-3">Sends in parts and picks up where it left off if the connection drops.</p>
      )}
    </li>
  );
}

function IconButton({ label, onClick, children }: { label: string; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      title={label}
      className="grid size-6 shrink-0 place-items-center rounded-[5px] text-ink-3 hover:bg-paper-sunk hover:text-ink [&_svg]:size-3.5"
    >
      {children}
    </button>
  );
}

/** Files and folders from a drop (folders read recursively), with paths relative to the drop. */
export async function droppedFiles(dt: DataTransfer): Promise<Array<{ file: File; path: string }>> {
  const entries = [...dt.items].map((i) => i.webkitGetAsEntry?.()).filter((e): e is FileSystemEntry => !!e);
  if (entries.length === 0) return [...dt.files].map((file) => ({ file, path: file.name }));
  const out: Array<{ file: File; path: string }> = [];
  const walk = async (e: FileSystemEntry, dir: string): Promise<void> => {
    if (e.isFile) {
      const file = await new Promise<File>((res, rej) => (e as FileSystemFileEntry).file(res, rej));
      out.push({ file, path: dir + file.name });
    } else if (e.isDirectory) {
      const reader = (e as FileSystemDirectoryEntry).createReader();
      for (;;) {
        const batch = await new Promise<FileSystemEntry[]>((res, rej) => reader.readEntries(res, rej));
        if (batch.length === 0) break;
        for (const c of batch) await walk(c, `${dir}${e.name}/`);
      }
    }
  };
  for (const e of entries) await walk(e, "");
  return out;
}
