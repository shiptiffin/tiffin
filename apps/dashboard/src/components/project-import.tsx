import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api, isProblem, uploadFile, type ArchiveSummary, type ProjectJob } from "@/api/client";
import { q } from "@/api/queries";
import { JobOutcome, JobProgress } from "@/components/project-copy";
import { ProblemNote } from "@/components/problem";
import { SegMeter } from "@/components/seg-meter";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Radio, RadioGroup } from "@/components/ui/choice";
import { cn } from "@/lib/cn";
import { bytes, countWords } from "@/lib/format";
import { useMe } from "@/lib/me";
import { useDebounced } from "@/lib/debounced";
import { checkName, slugify, type NameCheck } from "@/lib/starters";
import { relative } from "@/lib/time";

/** Secrets sealed to another box's key: the import needs that key, or goes without them. */
const sealedElsewhere = (s?: ArchiveSummary) => !!s && (s.secrets ?? []).length > 0 && !s.secretsPlain && !s.secretsHere;

/** Why the box refused a name for the import. */
function nameRefused(name: string, e: unknown): string {
  if (isProblem(e) && e.status === 403) return `You can’t create a project called ${name}.`;
  if (isProblem(e) && e.status === 409 && /destroyed/.test(e.problem.detail ?? ""))
    return `A project called ${name} was deleted less than 7 days ago, and its old data would come back. Give this one another name.`;
  if (isProblem(e) && e.status === 409) return `There’s already a project called ${name} here. Give this one another name.`;
  return e instanceof Error ? e.message : "Couldn’t check the name.";
}

/**
 * New project › Import a .tiffin file: the box reads its first megabyte (what's
 * inside, and whether it can import it at all), then it uploads (nothing
 * changes yet) while you pick a free name and what to do about secrets locked
 * to the old box; Import makes the project beside the others and the page
 * follows the job until it's done.
 */
export function useProjectImport(taken: { projects: string[]; routes: string[] }) {
  const qc = useQueryClient();
  const [sent, setSent] = useState<{ file: string; size: number; at: number } | null>(null);
  const [file, setFile] = useState<{ name: string; size: number } | null>(null);
  const [id, setId] = useState<string | null>(null);
  const [typed, setTyped] = useState<string | null>(null);
  const [secrets, setSecrets] = useState<"key" | "without">("key");
  const [key, setKey] = useState("");
  // What the file's first megabyte says it holds, read before the upload.
  const [head, setHead] = useState<ArchiveSummary | null>(null);

  const up = useMutation({
    mutationFn: async (f: File) => {
      setId(null);
      setSent({ file: f.name, size: f.size, at: 0 });
      // A file this box would refuse (a whole-box export, a newer Tiffin's) is refused before the upload.
      const c = await api.checkImport({ head: f.slice(0, 1 << 20) });
      setHead(c.source ?? null);
      setFile({ name: f.name, size: f.size });
      setTyped(null);
      setKey("");
      setSecrets("key");
      return uploadFile<ProjectJob>("/v1/project-imports", f, (n) => setSent({ file: f.name, size: f.size, at: n }));
    },
    onSuccess: (j) => {
      qc.setQueryData(["project-job", j.id], j);
      setId(j.id);
    },
    onError: () => {
      setHead(null);
      setFile(null);
    },
    onSettled: () => setSent(null),
  });
  const job = useQuery(q.projectJob(id ?? ""));
  const j = id ? job.data : undefined;
  const s = j?.source ?? head ?? undefined;
  const name = typed ?? s?.project ?? "";
  // The box says whether the name is free: taken, or deleted less than 7 days ago (its old data would come back).
  const asked = useDebounced(name, 250);
  const free = useQuery({
    queryKey: ["import-name", asked],
    queryFn: () => api.checkImport({ name: asked }),
    enabled: !!s && checkName(asked, taken).ok,
    retry: false,
    staleTime: 10_000,
  });
  const local: NameCheck = s ? checkName(name, taken) : { ok: false, why: "" };
  const check: NameCheck = !local.ok
    ? local
    : asked !== name || free.isPending
      ? { ok: false, why: "" }
      : free.error
        ? { ok: false, why: nameRefused(name, free.error) }
        : { ok: true };
  const sealed = sealedElsewhere(s);
  const keyOk = !sealed || secrets === "without" || key.trim().startsWith("AGE-SECRET-KEY-1");
  // The name and secrets can be chosen while the file uploads.
  const editable = j ? j.status === "uploaded" || (j.status === "failed" && !j.created) : !!head && up.isPending;

  const apply = useMutation({
    mutationFn: () => api.applyImport(id!, { name, ...(sealed ? (secrets === "without" ? { withoutSecrets: true } : { secretsKey: key.trim() }) : {}) }),
    onSuccess: (r) => {
      qc.setQueryData(["project-job", r.id], r);
      void qc.invalidateQueries({ queryKey: ["project-job", r.id] });
    },
  });
  const discard = useMutation({
    mutationFn: () => api.discardImport(id!),
    onSuccess: () => {
      setId(null);
      setHead(null);
      setFile(null);
      apply.reset();
      toast({ title: "Discarded the upload. Nothing on this box changed." });
    },
  });
  const done = j?.status === "done" || (j?.status === "failed" && j.created);
  useEffect(() => {
    if (done) {
      void qc.invalidateQueries({ queryKey: ["projects"] });
      void qc.invalidateQueries({ queryKey: ["changes"] });
    }
  }, [done, qc]);

  const ready = !!j && editable && check.ok && keyOk && !apply.isPending;
  return {
    up,
    sent,
    file,
    job: j,
    source: s,
    name,
    setName: (v: string) => setTyped(slugify(v)),
    check,
    sealed,
    secrets,
    setSecrets,
    key,
    setKey,
    editable,
    apply,
    discard,
    ready,
    submit: () => {
      if (ready) apply.mutate();
    },
  };
}

export type ProjectImport = ReturnType<typeof useProjectImport>;

/** The file: choose or drop it, then the upload's progress. */
export function ImportFile({ imp }: { imp: ProjectImport }) {
  const input = useRef<HTMLInputElement>(null);
  const [over, setOver] = useState(false);
  const { can } = useMe();
  const allowed = can("apply:reversible");
  const pick = (f?: File) => {
    if (f && allowed && !imp.up.isPending) imp.up.mutate(f);
  };
  return (
    <div className="mt-4">
      <input
        ref={input}
        type="file"
        accept=".tiffin,application/octet-stream"
        className="sr-only"
        tabIndex={-1}
        aria-label="Choose a .tiffin file"
        onChange={(e) => {
          pick(e.target.files?.[0]);
          e.target.value = "";
        }}
      />
      {imp.sent ? (
        <div className="max-w-[30rem]" aria-live="polite">
          <p className="text-[0.875rem] text-ink">Uploading {imp.sent.file}</p>
          <div className="mt-2 grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
            <SegMeter value={imp.sent.size ? (imp.sent.at / imp.sent.size) * 100 : 0} label="Upload progress" />
            <span className="text-[0.8125rem] text-ink-2 tnum">
              {bytes(imp.sent.at)} of {bytes(imp.sent.size)}
            </span>
          </div>
        </div>
      ) : imp.job && imp.file ? (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-[10px] border border-rule-2 bg-paper-raised px-4 py-3">
          <p className="min-w-0 text-[0.875rem] text-ink">
            <span className="ident break-all">{imp.file.name}</span> <span className="text-ink-3 tnum">{bytes(imp.file.size)}</span>
          </p>
          {imp.editable && (
            <Button type="button" variant="ghost" size="md" disabled={imp.discard.isPending} onClick={() => imp.discard.mutate()}>
              Discard
            </Button>
          )}
        </div>
      ) : (
        <div
          onDragOver={(e) => {
            e.preventDefault();
            setOver(true);
          }}
          onDragLeave={() => setOver(false)}
          onDrop={(e) => {
            e.preventDefault();
            setOver(false);
            pick(e.dataTransfer.files?.[0]);
          }}
          className={cn("rounded-[12px] border border-dashed px-5 py-6 text-center transition-colors", over ? "border-brass bg-brass-wash" : "border-rule-3")}
        >
          <Button type="button" size="lg" disabled={!allowed} onClick={() => input.current?.click()}>
            Choose a .tiffin file…
          </Button>
          <p className="mt-2 text-[0.8125rem] text-ink-3">
            {allowed ? "Or drop it here. Make one with Export in any project’s Settings. Nothing changes until you press Import." : "You can’t create projects on this box."}
          </p>
        </div>
      )}
      {imp.up.isError && <ProblemNote className="mt-3" error={imp.up.error} />}
      {imp.discard.isError && <ProblemNote className="mt-3" error={imp.discard.error} />}
    </div>
  );
}

/** Name and secrets, once the file is up. */
export function ImportSteps({ imp }: { imp: ProjectImport }) {
  const s = imp.source;
  if (!s || !imp.editable) return null;
  const secretNames = (s.secrets ?? []).join(", ");
  return (
    <>
      <section className="mb-10" aria-label="Name">
        <h2 className="label mb-3">Name</h2>
        <label htmlFor="iname" className="sr-only">
          Project name
        </label>
        <input
          id="iname"
          value={imp.name}
          onChange={(e) => imp.setName(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={!imp.check.ok}
          aria-describedby="iname-note"
          className="ident h-10 w-full rounded-[8px] border border-rule-2 bg-paper-raised px-3 text-[0.9375rem] text-ink outline-none transition-[border-color,box-shadow] focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger"
        />
        <p id="iname-note" className="mt-2 min-h-5 text-sm" aria-live="polite">
          {!imp.check.ok ? (
            <span className="text-danger">{"why" in imp.check ? imp.check.why : ""}</span>
          ) : imp.name !== s.project ? (
            <span className="text-ink-3">Under a new name its apps get new addresses. Custom domains and GitHub deploys stay with {s.project}.</span>
          ) : (
            <span className="text-ink-3">The same name it had on {s.domain || "the other box"}.</span>
          )}
        </p>
      </section>

      {imp.sealed && (
        <section className="mb-10" aria-label="Secrets">
          <h2 className="label mb-3">Secrets</h2>
          <p className="mb-3 max-w-[38rem] text-[0.875rem] text-ink-2">
            Its {countWords((s.secrets ?? []).length, "secret")} are locked to the box it came from{s.domain ? ` (${s.domain})` : ""}. That box’s key unlocks them.
          </p>
          <RadioGroup value={imp.secrets} onValueChange={(v) => imp.setSecrets(v as "key" | "without")} className="divide-y divide-rule border-y border-rule">
            <label className={cn("flex cursor-pointer gap-3 px-2 py-2.5 hover:bg-paper-sunk", imp.secrets === "key" && "bg-paper-sunk")}>
              <Radio value="key" className="mt-0.5" />
              <span>
                <span className="block text-[0.875rem] font-[550] text-ink">Paste the old box’s key</span>
                <span className="block text-[0.8125rem] text-ink-3">
                  On that box it’s in <span className="ident">/var/lib/tiffin/platform/secrets.key</span> and starts with <span className="ident">AGE-SECRET-KEY-1</span>.
                </span>
              </span>
            </label>
            <label className={cn("flex cursor-pointer gap-3 px-2 py-2.5 hover:bg-paper-sunk", imp.secrets === "without" && "bg-paper-sunk")}>
              <Radio value="without" className="mt-0.5" />
              <span>
                <span className="block text-[0.875rem] font-[550] text-ink">Import without secrets</span>
                <span className="block text-[0.8125rem] text-ink-3">
                  You set them again afterwards: <span className="ident">{secretNames}</span>.
                </span>
              </span>
            </label>
          </RadioGroup>
          {imp.secrets === "key" && (
            <textarea
              value={imp.key}
              onChange={(e) => imp.setKey(e.target.value)}
              rows={2}
              spellCheck={false}
              autoComplete="off"
              aria-label="The old box’s key"
              placeholder="AGE-SECRET-KEY-1…"
              className="ident mt-3 w-full rounded-[8px] border border-rule-2 bg-paper-raised px-3 py-2 text-[0.8125rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]"
            />
          )}
        </section>
      )}
    </>
  );
}

/** The right-hand panel: what the file holds, then Import, its progress and how it ended. */
export function ImportPanel({ imp }: { imp: ProjectImport }) {
  const s = imp.source;
  const j = imp.job;
  const n = (s?.secrets ?? []).length;
  const rows: Array<[string, string, string?]> = s
    ? [
        ["Apps", s.apps?.length ? s.apps.join(", ") : "None"],
        ...(s.postgres ? ([["Database", "Yes", "Postgres"]] as Array<[string, string, string]>) : []),
        ...(s.buckets?.length ? ([["Files", s.buckets.join(", ")]] as Array<[string, string]>) : []),
        ...(s.valkey ? ([["KV", "Yes", "Valkey"]] as Array<[string, string, string]>) : []),
        ...(n
          ? ([[n === 1 ? "Secret" : "Secrets", `${n}, ${s.secretsPlain ? "in plain text" : s.secretsHere ? "readable on this box" : "locked to the old box"}`]] as Array<[string, string]>)
          : []),
        ["History", s.history ? "Included" : "Not included"],
      ]
    : [];
  return (
    <aside aria-label="What’s inside" className="min-w-0 overflow-hidden rounded-[14px] border border-rule-2 bg-paper-raised shadow-raised lg:sticky lg:top-8">
      <div className="border-b border-rule px-5 pt-4 pb-3.5">
        <h2 className="label">What’s inside</h2>
        <p className="mt-2 text-[0.9375rem] leading-[1.375rem] font-[550] tracking-[-0.01em] text-ink">
          {s ? `${s.project}, from ${s.domain || "another box"}` : "Choose a .tiffin file to see what’s inside."}
        </p>
        {s && (
          <p className="mt-0.5 text-[0.8125rem] text-ink-3">
            Exported {relative(s.exportedAt)} with Tiffin {s.tiffinVersion.replace(/^v/, "")}.
          </p>
        )}
      </div>
      {rows.length > 0 && (
        <dl className="divide-y divide-rule px-5 text-[0.84375rem]">
          {rows.map(([k, v, sub]) => (
            <div key={k} className="flex items-baseline justify-between gap-4 py-2.5">
              <dt className="text-ink-3">
                {k}
                {sub && <span className="ml-1.5 text-[0.75rem] text-ink-4">{sub}</span>}
              </dt>
              <dd className={cn("min-w-0 text-right break-words text-ink", k.startsWith("Secret") && imp.sealed && "text-warn-ink")}>{v}</dd>
            </div>
          ))}
        </dl>
      )}
      <div className="border-t border-rule px-5 py-4">
        {j?.status === "running" ? (
          <JobProgress job={j} />
        ) : j && (j.status === "done" || j.created) ? (
          <JobOutcome job={j} />
        ) : (
          <>
            {j?.status === "failed" && <JobOutcome job={j} />}
            {!!imp.apply.error && <ProblemNote error={imp.apply.error} className="mb-3" />}
            <Button type="submit" variant="primary" size="lg" className={cn("w-full", j?.status === "failed" && "mt-3")} disabled={!imp.ready}>
              {imp.apply.isPending ? "Starting…" : s && imp.check.ok ? `Import ${imp.name}` : "Import"}
            </Button>
            <p className="mt-2.5 text-center text-xs text-ink-3">It’s made beside your other projects; nothing here is replaced.</p>
          </>
        )}
      </div>
    </aside>
  );
}
