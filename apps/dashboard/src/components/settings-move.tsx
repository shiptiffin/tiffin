import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { request, uploadFile } from "@/api/client";
import type { components } from "@/api/schema";
import { CopyValue } from "@/components/copy";
import { HazardDialog } from "@/components/hazard";
import { PilotLight } from "@/components/pilot";
import { Code, ProblemNote, sentence } from "@/components/problem";
import { SegMeter } from "@/components/seg-meter";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/choice";
import { cn } from "@/lib/cn";
import { bytes, countWords, dec, int, ms, plainWords } from "@/lib/format";
import { full, relative } from "@/lib/time";

type Export = components["schemas"]["PortableExport"];
type Import = components["schemas"]["PortableImport"];
type ImportPreview = {
  import: string;
  existingProjects: string[];
  overwrites: string[];
  keeps: string[];
  downtime: string;
  safetyBackup: string;
};

const e = encodeURIComponent;
const move = {
  exports: () => request<Export[] | null>("GET", "/v1/box/exports").then((x) => x ?? []),
  exp: (id: string) => request<Export>("GET", `/v1/box/exports/${e(id)}`),
  startExport: (body: { includeKey: boolean; withHistory: boolean; store: boolean }) => request<Export>("POST", "/v1/box/exports", body),
  deleteExport: (id: string) => request<void>("DELETE", `/v1/box/exports/${e(id)}`),
  imports: () => request<Import[] | null>("GET", "/v1/box/imports").then((x) => x ?? []),
  imp: (id: string) => request<Import>("GET", `/v1/box/imports/${e(id)}`),
  discard: (id: string) => request<void>("DELETE", `/v1/box/imports/${e(id)}`),
  apply: (id: string, body: { replace: boolean; secretsKey?: string; confirm?: string }) => request<Import>("POST", `/v1/box/imports/${e(id)}/apply`, body),
};

const partNames: Record<string, string> = {
  platform: "Platform state",
  postgres: "Postgres",
  valkey: "Valkey",
  storage: "Buckets and objects",
  email: "Email",
  analytics: "Analytics",
  observe: "Errors and alert rules",
  images: "App images",
  sites: "Static sites",
  git: "Git repositories",
  certs: "HTTPS certificates",
  history: "History",
  edge: "HTTPS certificate authority",
  "runtime-static": "Static sites",
  "storage-trash": "Storage trash",
};
const itemWords: Record<string, [string, string]> = { postgres: ["database", "databases"], images: ["image", "images"], valkey: ["key", "keys"] };

function Parts({ parts }: { parts?: Record<string, components["schemas"]["Stats"]> }) {
  const list = Object.entries(parts ?? {}).filter(([, s]) => s.bytes > 0 || (s.items ?? 0) > 0);
  if (!list.length) return null;
  return (
    <ul className="mt-2 grid gap-x-6 gap-y-0.5 text-[0.8125rem] sm:grid-cols-2">
      {list.map(([k, s]) => (
        <li key={k} className="flex justify-between gap-3 border-b border-rule py-1">
          <span className="text-ink-2">{partNames[k] ?? k}</span>
          <span className="text-ink-3 tnum">
            {s.items ? `${int(s.items)} ${(itemWords[k] ?? ["item", "items"])[s.items === 1 ? 0 : 1]}, ` : ""}
            {bytes(s.bytes)}
          </span>
        </li>
      ))}
    </ul>
  );
}

/** Export: pack the whole box into one file, with or without its key; progress; then the download. */
export function ExportBox({ canExport }: { canExport: boolean }) {
  const qc = useQueryClient();
  const [includeKey, setIncludeKey] = useState(false);
  const [withHistory, setWithHistory] = useState(false);
  const list = useQuery({
    queryKey: ["box-exports"],
    queryFn: move.exports,
    retry: false,
    refetchInterval: (q) => ((q.state.data ?? []).some((x) => x.status === "running" || (x.status === "pending" && x.mode === "stored")) ? 1000 : false),
  });
  const start = useMutation({
    mutationFn: () => move.startExport({ includeKey, withHistory, store: true }),
    onSettled: () => qc.invalidateQueries({ queryKey: ["box-exports"] }),
  });
  const del = useMutation({
    mutationFn: (id: string) => move.deleteExport(id),
    onSuccess: () => toast({ title: "Deleted the archive from the box. The record stays." }),
    onSettled: () => qc.invalidateQueries({ queryKey: ["box-exports"] }),
  });
  const recent = (list.data ?? []).filter((x) => x.status !== "expired").slice(0, 3);
  const busy = recent.some((x) => x.status === "running" || x.status === "pending");
  return (
    <div>
      <div className="flex flex-col gap-2.5">
        <label className="flex cursor-pointer items-start gap-3">
          <Checkbox className="mt-0.5" checked={withHistory} onCheckedChange={(v) => setWithHistory(v === true)} />
          <span className="text-[0.875rem] text-ink">
            Include history
            <span className="block text-[0.8125rem] text-ink-3">Logs, metrics, build logs and database snapshots. Bigger, and rarely needed.</span>
          </span>
        </label>
        <label className="flex cursor-pointer items-start gap-3">
          <Checkbox className="mt-0.5" checked={includeKey} onCheckedChange={(v) => setIncludeKey(v === true)} />
          <span className="text-[0.875rem] text-ink">
            Put the box key inside
            <span className={cn("block text-[0.8125rem]", includeKey ? "text-danger" : "text-ink-3")}>
              {includeKey
                ? "Anyone holding the file can read every secret in it. Keep it like a password."
                : "Without it, secrets stay encrypted to this box’s key and the import asks for that key."}
            </span>
          </span>
        </label>
      </div>
      <div className="mt-4 flex flex-wrap items-center gap-3">
        <Button variant="primary" size="lg" disabled={!canExport || busy || start.isPending} onClick={() => start.mutate()}>
          {start.isPending ? "Starting…" : "Export the box"}
        </Button>
        <span className="text-[0.8125rem] text-ink-3">
          {canExport ? "Written on the box first, then yours to download. Apps pause writes for under a second." : "The owner or an admin can export the box."}
        </span>
      </div>
      {start.isError && <ProblemNote className="mt-3" error={start.error} />}
      {recent.length > 0 && (
        <ul className="mt-5 divide-y divide-rule border-y border-rule">
          {recent.map((x) => (
            <ExportRow key={x.id} x={x} onDelete={() => del.mutate(x.id)} deleting={del.isPending} />
          ))}
        </ul>
      )}
    </div>
  );
}

function ExportRow({ x, onDelete, deleting }: { x: Export; onDelete: () => void; deleting: boolean }) {
  const running = x.status === "running" || (x.status === "pending" && x.mode === "stored");
  return (
    <li className="py-3">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="min-w-0">
          <p className="flex items-center gap-2 text-[0.875rem] text-ink">
            {running && <PilotLight state="busy" label="Exporting" />}
            <span className="ident">{x.fileName}</span>
            {x.sizeBytes ? <span className="text-ink-3 tnum">{bytes(x.sizeBytes)}</span> : null}
          </p>
          <p className="text-[0.8125rem] text-ink-3" title={full(x.createdAt)}>
            {running
              ? sentence(plainWords(x.phase ?? "starting"))
              : x.status === "failed"
                ? ""
                : `Made ${relative(x.finishedAt ?? x.createdAt)}${x.durationMs ? ` in ${ms(x.durationMs)}` : ""}${x.writesPausedMs ? `; writes paused for ${ms(x.writesPausedMs)}` : ""}${x.withHistory ? "; with history" : ""}.`}
          </p>
          {x.status === "failed" && <p className="text-[0.8125rem] text-danger">{sentence(x.error ?? "It stopped.")}</p>}
        </div>
        {x.status === "done" && (
          <span className="flex items-center gap-1">
            <Button asChild variant="secondary">
              <a href={x.download} download={x.fileName}>
                Download {x.sizeBytes ? bytes(x.sizeBytes) : ""}
              </a>
            </Button>
            {x.mode === "stored" && (
              <Button variant="ghost" onClick={onDelete} disabled={deleting}>
                Delete from the box
              </Button>
            )}
          </span>
        )}
      </div>
      {running && (
        <div className="mt-2 grid max-w-[30rem] grid-cols-[minmax(0,1fr)_auto] items-start gap-3">
          <SegMeter value={x.percent} label="Export progress" valueText={`${x.percent} percent`} scale />
          <span className="text-[0.8125rem] text-ink-2 tnum">
            {dec(x.percent, 0)}&#8239;% · {bytes(x.writtenBytes)}
          </span>
        </div>
      )}
      {x.status === "done" && (
        <div className="mt-2 text-[0.8125rem]">
          {x.sha256 && (
            <p className="flex items-center gap-2 text-ink-3">
              SHA-256 <CopyValue value={x.sha256} display={`${x.sha256.slice(0, 16)}…`} />
            </p>
          )}
          <p className={cn("mt-1 max-w-[42rem]", x.key.included ? "text-danger" : "text-ink-2")}>
            <Code text={plainWords(x.key.note)} />
          </p>
          <Parts parts={x.parts} />
        </div>
      )}
    </li>
  );
}

/**
 * Import: upload a .tiffin file, read what the box verified, then apply it
 * through the guard (read what it replaces, type the box's name).
 */
export function ImportBox({ boxName, projects, isOwner }: { boxName: string; projects: string[]; isOwner: boolean }) {
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [sent, setSent] = useState<{ name: string; size: number; at: number } | null>(null);
  const [current, setCurrent] = useState<string | null>(null);
  const [key, setKey] = useState("");
  const [applying, setApplying] = useState(false);
  const up = useMutation({
    mutationFn: (f: File) => uploadFile<Import>("/v1/box/imports", f, (n) => setSent({ name: f.name, size: f.size, at: n })),
    onSuccess: (r) => {
      setCurrent(r.id);
      qc.invalidateQueries({ queryKey: ["box-imports"] });
    },
    onSettled: () => setSent(null),
  });
  const list = useQuery({ queryKey: ["box-imports"], queryFn: move.imports, retry: false });
  const id = current ?? (list.data ?? []).find((x) => x.status !== "done")?.id ?? null;
  const rec = useQuery({
    queryKey: ["box-import", id],
    queryFn: () => move.imp(id!),
    enabled: !!id,
    retry: 3,
    refetchInterval: (q) => (q.state.data && ["applying", "restarting", "converging"].includes(q.state.data.status) ? 1500 : false),
  });
  const discard = useMutation({
    mutationFn: (x: string) => move.discard(x),
    onSuccess: () => {
      setCurrent(null);
      toast({ title: "Discarded the upload. Nothing on this box changed." });
      qc.invalidateQueries({ queryKey: ["box-imports"] });
    },
  });
  const r = rec.data;
  const s = r?.source;
  const replace = projects.length > 0;
  const needKey = s && !s.includesKey;
  return (
    <div>
      <input
        ref={input}
        type="file"
        accept=".tiffin,application/octet-stream"
        className="sr-only"
        aria-label="Choose a .tiffin file"
        onChange={(ev) => {
          const f = ev.target.files?.[0];
          if (f) up.mutate(f);
          ev.target.value = "";
        }}
      />
      {!r || r.status === "done" ? (
        <div className="flex flex-wrap items-center gap-3">
          <Button size="lg" disabled={!isOwner || up.isPending} onClick={() => input.current?.click()}>
            {up.isPending ? "Uploading…" : "Choose a .tiffin file…"}
          </Button>
          <span className="text-[0.8125rem] text-ink-3">{isOwner ? "Nothing on this box changes until you apply it." : "Only the owner can import."}</span>
        </div>
      ) : null}
      {sent && (
        <div className="mt-3 grid max-w-[30rem] grid-cols-[minmax(0,1fr)_auto] items-start gap-3">
          <SegMeter value={sent.size ? (sent.at / sent.size) * 100 : 0} label="Upload progress" scale />
          <span className="text-[0.8125rem] text-ink-2 tnum">
            {bytes(sent.at)} of {bytes(sent.size)}
          </span>
        </div>
      )}
      {up.isError && <ProblemNote className="mt-3" error={up.error} />}

      {r && s && r.status !== "done" && (
        <div className="mt-1 rounded-[10px] border border-rule-2 bg-paper-raised px-5 py-4">
          <p className="label">Verified on the box</p>
          <p className="mt-1.5 text-[0.9375rem] text-ink">
            Every entry matches the archive’s own digest. It is {s.hostname.replace(/^lima-/, "")}
            {s.domain ? ` (${s.domain})` : ""}, made {relative(s.createdAt)} by Tiffin {s.tiffinVersion.replace(/^v/, "")}.
          </p>
          <ul className="mt-3 grid gap-x-6 text-[0.8125rem] sm:grid-cols-2">
            {[
              ["Projects", s.projects?.length ? s.projects.join(", ") : "none"],
              ["Databases", countWords(s.databases?.length ?? 0, "database")],
              ["App images", int(s.images)],
              ["Size", bytes(r.sizeBytes)],
              ["Box key", s.includesKey ? "inside the archive" : "not inside: paste it below"],
              ["History", s.withHistory ? "included" : "not included"],
            ].map(([k, v]) => (
              <li key={k} className="flex justify-between gap-3 border-b border-rule py-1.5">
                <span className="text-ink-3">{k}</span>
                <span className={cn("text-right text-ink-2", k === "Box key" && !s.includesKey && "text-warn-ink")}>{v}</span>
              </li>
            ))}
          </ul>
          <p className="ident mt-2 text-[0.71875rem] text-ink-3">sha256 {r.sha256.slice(0, 24)}…</p>

          {r.status === "uploaded" || r.status === "failed" ? (
            <>
              {r.status === "failed" && <p className="mt-3 text-[0.875rem] text-danger">{sentence(r.error ?? "The last apply stopped.")}</p>}
              {needKey && (
                <label className="mt-4 block">
                  <span className="text-[0.875rem] text-ink">The source box’s key</span>
                  <span className="block text-[0.8125rem] text-ink-3">
                    The contents of <code className="ident">/var/lib/tiffin/platform/secrets.key</code> on the old box (<span className="ident">AGE-SECRET-KEY-1…</span>).
                  </span>
                  <textarea
                    value={key}
                    onChange={(ev) => setKey(ev.target.value)}
                    rows={2}
                    spellCheck={false}
                    autoComplete="off"
                    className="ident mt-2 w-full rounded-md border border-rule bg-paper px-3 py-2 text-ink outline-none focus-visible:border-brass"
                  />
                </label>
              )}
              <div className="mt-4 border-t border-rule pt-4">
                <p className="text-[0.875rem] text-danger">
                  Applying this replaces {replace ? `everything on ${boxName}: ${countWords(projects.length, "project")} (${projects.join(", ")}), their data, ` : `${boxName}’s `}
                  settings, tokens and people with the archive’s.{replace ? " A full backup of this box is taken first." : ""}
                </p>
                <div className="mt-3 flex flex-wrap gap-2">
                  <Button variant="danger-quiet" size="lg" className="border border-danger-rule" disabled={!isOwner || (needKey && !key.trim())} onClick={() => setApplying(true)}>
                    Review what it replaces…
                  </Button>
                  <Button variant="ghost" size="lg" disabled={discard.isPending} onClick={() => discard.mutate(r.id)}>
                    Discard the upload
                  </Button>
                </div>
              </div>
            </>
          ) : (
            <div className="mt-4 border-t border-rule pt-4">
              <p className="flex items-center gap-2 text-[0.875rem] text-ink">
                <PilotLight state="busy" label="Importing" />
                {sentence(plainWords(r.phase ?? r.status))}
              </p>
              <div className="mt-2 max-w-[30rem]">
                <SegMeter value={r.percent} label="Import progress" scale />
              </div>
              <p className="mt-2 text-[0.8125rem] text-ink-3">The box restarts once along the way; this page reconnects by itself.</p>
            </div>
          )}
          {discard.isError && <ProblemNote className="mt-3" error={discard.error} />}
        </div>
      )}

      {(list.data ?? []).filter((x) => x.status === "done").slice(0, 1).map((x) => (
        <p key={x.id} className={cn("mt-4 text-[0.84375rem]", x.healthy ? "text-ink-2" : "text-warn-ink")}>
          Imported {x.source.hostname.replace(/^lima-/, "")} {relative(x.finishedAt ?? x.uploadedAt)}
          {x.durationMs ? ` in ${ms(x.durationMs)}` : ""}.{" "}
          {x.healthy ? "Every project converged and every check passes." : `Still not green: ${(x.failing ?? []).map((f) => f.name).join(", ")}.`}
          {x.safetyBackup ? ` The box as it was is in backup ${x.safetyBackup}.` : ""}
        </p>
      ))}

      {r && (
        <HazardDialog<ImportPreview, Import>
          key={r.id + String(applying)}
          open={applying}
          onOpenChange={setApplying}
          title={`Replace ${boxName} with ${s?.hostname.replace(/^lima-/, "") ?? "the archive"}?`}
          word={boxName}
          action={`Replace ${boxName}`}
          run={(confirm) => move.apply(r.id, { replace, secretsKey: needKey ? key.trim() : undefined, confirm })}
          renderPreview={(p) => (
            <div className="flex flex-col gap-4 text-[0.84375rem]">
              <div className="border-y border-danger-rule py-3">
                <p className="font-[550] text-danger">This replaces:</p>
                <ul className="mt-2 flex list-disc flex-col gap-1.5 pl-5 text-ink-2">
                  {p.overwrites.map((o) => (
                    <li key={o}>{sentence(plainWords(o))}</li>
                  ))}
                </ul>
              </div>
              <div>
                <p className="font-[550] text-ink">Stays as it is:</p>
                <ul className="mt-2 flex list-disc flex-col gap-1 pl-5 text-ink-2">
                  {p.keeps.map((o) => (
                    <li key={o}>{sentence(o)}</li>
                  ))}
                </ul>
              </div>
              <p className="text-ink-2">
                {sentence(p.downtime)} Safety copy: {p.safetyBackup}.
              </p>
            </div>
          )}
          onDone={() => {
            setApplying(false);
            qc.invalidateQueries({ queryKey: ["box-import", r.id] });
            toast({ title: "Importing. The box restarts once; this page reconnects by itself." });
          }}
        />
      )}
    </div>
  );
}
