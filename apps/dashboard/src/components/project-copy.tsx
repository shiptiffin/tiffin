import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, ChevronRight } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { api, ApiError, type ProjectJob, type ProjectState } from "@/api/client";
import { q } from "@/api/queries";
import { Command } from "@/components/copy";
import { DeleteProject } from "@/components/delete-project";
import { PilotLight } from "@/components/pilot";
import { ProblemNote, sentence } from "@/components/problem";
import { SegMeter } from "@/components/seg-meter";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/choice";
import { cn } from "@/lib/cn";
import { bytes, plainWords } from "@/lib/format";
import { useMe } from "@/lib/me";
import { checkName, slugify } from "@/lib/starters";
import { undoChange } from "@/lib/staged";

const field =
  "ident h-9 w-full rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.84375rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger";

/** A duplicate or import on its way: what it is doing, and how far along. */
export function JobProgress({ job }: { job: ProjectJob }) {
  return (
    <div className="max-w-[30rem]" aria-live="polite">
      <p className="flex items-center gap-2 text-[0.875rem] text-ink">
        <PilotLight state="busy" label="Working" />
        {sentence(plainWords(job.phase || "starting"))}
      </p>
      <div className="mt-2 grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
        <SegMeter value={job.percent} label="Progress" valueText={`${job.percent} percent`} />
        <span className="text-[0.8125rem] text-ink-2 tnum">{job.percent}&#8239;%</span>
      </div>
    </div>
  );
}

/** How a duplicate or import ended: the new project, its apps' addresses, and what didn't come across. */
export function JobOutcome({ job, children }: { job: ProjectJob; children?: ReactNode }) {
  const name = job.project ?? "";
  const failedApps = (job.apps ?? []).filter((a) => a.status === "failed");
  return (
    <div className="flex max-w-[40rem] flex-col gap-3 text-[0.875rem]" aria-live="polite">
      {job.status === "failed" && <ProblemNote error={new ApiError({ status: 0, code: "internal", title: "", detail: job.error ?? "It stopped.", hint: job.hint })} />}
      {job.created && (
        <p className="text-ink">
          {job.status === "done" ? (failedApps.length ? `${name} is made, but not every app started.` : `${name} is ready.`) : `${name} was made, but not everything came across.`}{" "}
          <Link to="/projects/$project" params={{ project: name }} className="inline-flex items-center gap-0.5 font-[550] text-ink underline decoration-rule-3 underline-offset-2 hover:decoration-ink">
            Open {name}
            <ChevronRight className="size-4 text-ink-4" />
          </Link>
        </p>
      )}
      {(job.apps ?? []).length > 0 && (
        <ul className="divide-y divide-rule border-y border-rule">
          {(job.apps ?? []).map((a) => (
            <li key={a.app} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 py-2">
              <span className="font-[550] text-ink">{a.app}</span>
              {a.status === "failed" ? (
                <span className="text-danger">Didn’t start{a.error ? `: ${a.error}` : "."}</span>
              ) : a.status === "none" ? (
                <span className="text-ink-3">Not deployed yet</span>
              ) : a.url ? (
                <a href={a.url} target="_blank" rel="noreferrer" className="ident inline-flex min-w-0 items-center gap-1 text-[0.8125rem] text-ink-2 hover:text-ink">
                  <span className="truncate">{a.url.replace(/^https?:\/\//, "")}</span>
                  <ArrowUpRight className="size-3.5 shrink-0 text-ink-3" />
                </a>
              ) : (
                <span className="text-ink-3">Live</span>
              )}
            </li>
          ))}
        </ul>
      )}
      {(job.secretsMissing ?? []).length > 0 && (
        <p className="text-warn-ink">
          Set these secrets by hand: <span className="ident text-[0.8125rem]">{job.secretsMissing!.join(", ")}</span>.{" "}
          {job.created && (
            <Link to="/projects/$project/env" params={{ project: name }} className="underline underline-offset-2">
              {name}’s environment variables
            </Link>
          )}
        </p>
      )}
      {(job.notes ?? []).map((n) => (
        <p key={n} className="text-ink-2">
          {sentence(n)}
        </p>
      ))}
      {children}
    </div>
  );
}

/** A stopped project (it was moved, or someone stopped it): one line and Start. */
export function StoppedNote({ project, state }: { project: string; state?: ProjectState }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const stopped = state?.resources?.find((r) => r.address === "stopped");
  const start = useMutation({
    mutationFn: () => api.startProject(project),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ["project", project] });
      void qc.invalidateQueries({ queryKey: ["changes"] });
      const id = r.change?.id;
      toast({ title: `Started ${project}.`, detail: "Its apps come back from their live deploys.", action: id ? { label: "Undo", run: () => undoChange(id) } : undefined });
    },
  });
  if (!stopped) return null;
  const reason = (stopped.spec as { reason?: string } | null)?.reason;
  return (
    <div className="mt-6 rounded-[10px] border border-rule-2 bg-paper-sunk px-4 py-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="flex items-center gap-2 text-[0.9375rem] font-[550] text-ink">
            <PilotLight state="off" />
            {project} is stopped
          </p>
          <p className="mt-0.5 text-[0.8125rem] text-ink-2">
            Its apps are down and its data is kept. {reason ? sentence(reason) : ""}
          </p>
        </div>
        {can("apply:reversible") && (
          <Button size="md" disabled={start.isPending} onClick={() => start.mutate()}>
            {start.isPending ? "Starting…" : `Start ${project}`}
          </Button>
        )}
      </div>
      {start.isError && <ProblemNote className="mt-3" error={start.error} />}
    </div>
  );
}

/** Settings › Copy & move: Duplicate here, Export to a file, Move to another box (from the terminal). */
export function CopyAndMove({ project }: { project: string }) {
  const { can } = useMe();
  const full = can("apply:irreversible");
  return (
    <div className="divide-y divide-rule border-y border-rule">
      {full ? (
        <>
          <Duplicate project={project} />
          <Export project={project} />
        </>
      ) : (
        <p className="py-4 text-[0.875rem] text-ink-3">Duplicating or exporting {project} hands out all of its data, so it needs full access to {project}.</p>
      )}
      <Move project={project} />
    </div>
  );
}

function Row({ title, line, children }: { title: string; line: ReactNode; children: ReactNode }) {
  return (
    <div className="py-5">
      <h3 className="text-[0.875rem] font-[550] text-ink">{title}</h3>
      <p className="mt-0.5 mb-3 max-w-[40rem] text-[0.8125rem] text-ink-3">{line}</p>
      {children}
    </div>
  );
}

/** The running duplicate's job id survives moving between pages (this tab only). */
const dupKey = (project: string) => `tiffin.duplicate.${project}`;
const stored = {
  get: (k: string) => {
    try {
      return sessionStorage.getItem(k);
    } catch {
      return null;
    }
  },
  set: (k: string, v: string | null) => {
    try {
      if (v) sessionStorage.setItem(k, v);
      else sessionStorage.removeItem(k);
    } catch {
      /* private window */
    }
  },
};

function Duplicate({ project }: { project: string }) {
  const qc = useQueryClient();
  const projects = useQuery(q.projects);
  const [typed, setTyped] = useState<string | null>(null);
  const [jobId, setJobId] = useState<string | null>(() => stored.get(dupKey(project)));
  const job = useQuery(q.projectJob(jobId ?? ""));
  // A job the box no longer knows (restarted, or someone else's) is simply forgotten.
  const j = jobId && !job.isError ? job.data : undefined;
  const ended = j?.status === "done" || j?.status === "failed" || job.isError;
  useEffect(() => {
    if (!ended) return;
    stored.set(dupKey(project), null);
    void qc.invalidateQueries({ queryKey: ["projects"] });
  }, [ended, project, qc]);

  const names = (projects.data ?? []).map((p) => p.name);
  const suggested = [`${project}-copy`, ...[2, 3, 4, 5, 6, 7, 8, 9].map((n) => `${project}-copy-${n}`)].find((n) => !names.includes(n)) ?? `${project}-copy`;
  const name = typed ?? suggested;
  const check = checkName(name, { projects: names, routes: [] });
  const start = useMutation({
    mutationFn: () => api.duplicate(project, name),
    onSuccess: (r) => {
      qc.setQueryData(["project-job", r.id], r);
      stored.set(dupKey(project), r.id);
      setJobId(r.id);
      setTyped(null);
    },
  });
  const reset = () => {
    stored.set(dupKey(project), null);
    setJobId(null);
    start.reset();
  };

  return (
    <Row title="Duplicate" line={<>A full copy of {project} on this box under a new name: its database, files, secrets, settings and apps, at their own new addresses.</>}>
      {!j ? (
        <form
          className="flex flex-wrap items-start gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (check.ok && !start.isPending) start.mutate();
          }}
        >
          <label className="min-w-0 flex-1 basis-56 sm:max-w-[18rem]">
            <span className="sr-only">Name of the copy</span>
            <input value={name} onChange={(e) => setTyped(slugify(e.target.value))} autoComplete="off" spellCheck={false} aria-invalid={!check.ok} className={field} />
            {!check.ok && <span className="mt-1 block text-[0.8125rem] text-danger">{check.why}</span>}
          </label>
          <Button type="submit" size="lg" className="h-9" disabled={!check.ok || start.isPending}>
            {start.isPending ? "Starting…" : `Duplicate ${project}`}
          </Button>
        </form>
      ) : j.status === "running" ? (
        <JobProgress job={j} />
      ) : (
        <JobOutcome job={j}>
          <div className="flex flex-wrap items-center gap-2">
            {j.created && j.project && (
              <DeleteProject
                project={j.project}
                label={j.status === "done" ? `Undo: delete ${j.project}…` : `Delete ${j.project}…`}
                onDeleted={reset}
              />
            )}
            <Button variant="ghost" size="md" onClick={reset}>
              {j.status === "done" ? "Done" : "Try again"}
            </Button>
          </div>
        </JobOutcome>
      )}
      {start.isError && <ProblemNote className="mt-3" error={start.error} />}
    </Row>
  );
}

function Export({ project }: { project: string }) {
  const [includeSecrets, setIncludeSecrets] = useState(false);
  const [withHistory, setWithHistory] = useState(false);
  const [id, setId] = useState<string | null>(null);
  const exp = useQuery({
    queryKey: ["project-export", project, id],
    queryFn: () => api.projectExport(project, id!),
    enabled: !!id,
    refetchInterval: (qq) => (qq.state.data && ["done", "failed", "expired"].includes(qq.state.data.status) ? false : 1000),
    refetchIntervalInBackground: true,
  });
  const start = useMutation({
    mutationFn: () => api.exportProject(project, { includeSecrets, withHistory }),
    onSuccess: (x) => {
      setId(x.id);
      download(x.download, x.fileName);
    },
  });
  const x = id ? exp.data : undefined;
  const busy = start.isPending || x?.status === "pending" || x?.status === "running";
  return (
    <Row title="Export" line={<>Download {project} as one .tiffin file: its database, files, cache, settings and apps. Import it on any Tiffin box, or run it without Tiffin from the instructions inside.</>}>
      <div className="flex flex-col gap-2.5">
        <label className="flex cursor-pointer items-start gap-3">
          <Checkbox className="mt-0.5" checked={includeSecrets} onCheckedChange={(v) => setIncludeSecrets(v === true)} />
          <span className="text-[0.875rem] text-ink">
            Include secrets in plain text
            <span className={cn("block text-[0.8125rem]", includeSecrets ? "text-danger" : "text-ink-3")}>
              {includeSecrets
                ? "Anyone with the file can read every secret in it. Keep it like a password."
                : "Without this, secrets stay locked to this box. Another box needs this box’s key to read them, or sets them again."}
            </span>
          </span>
        </label>
        <label className="flex cursor-pointer items-start gap-3">
          <Checkbox className="mt-0.5" checked={withHistory} onCheckedChange={(v) => setWithHistory(v === true)} />
          <span className="text-[0.875rem] text-ink">
            Include History
            <span className="block text-[0.8125rem] text-ink-3">Every change made to {project}, who made it and when.</span>
          </span>
        </label>
      </div>
      <div className="mt-4 flex flex-wrap items-center gap-3">
        <Button size="lg" disabled={busy} onClick={() => start.mutate()}>
          {busy ? "Exporting…" : `Download ${project}.tiffin`}
        </Button>
        {x && (x.status === "pending" || x.status === "running") && (
          <span className="flex items-center gap-2 text-[0.8125rem] text-ink-2" aria-live="polite">
            <PilotLight state="busy" label="Exporting" />
            {x.status === "pending" ? (
              <>
                Waiting for the download to start.{" "}
                <a href={x.download} download={x.fileName} className="underline underline-offset-2">
                  Start it
                </a>
              </>
            ) : (
              <>
                {sentence(plainWords(x.phase || "packing"))} {x.contentBytes > 0 && <span className="tnum text-ink-3">{bytes(x.contentBytes)} so far</span>}
              </>
            )}
          </span>
        )}
      </div>
      {x?.status === "done" && (
        <div className="mt-3 text-[0.8125rem]" aria-live="polite">
          <p className="text-ink-2">
            Downloaded <span className="ident">{x.fileName}</span>
            {x.sizeBytes ? `, ${bytes(x.sizeBytes)}` : ""}.
          </p>
          {x.secretsNote && <p className={cn("mt-1", x.includeSecrets ? "text-danger" : "text-ink-3")}>{sentence(x.secretsNote)}</p>}
        </div>
      )}
      {x?.status === "failed" && <ProblemNote className="mt-3" error={new ApiError({ status: 0, code: "internal", title: "", detail: x.error ?? "The export stopped.", hint: x.hint })} />}
      {start.isError && <ProblemNote className="mt-3" error={start.error} />}
    </Row>
  );
}

/** Starts the browser's download of a same-origin path (the session cookie goes with it). */
function download(href: string, name: string) {
  const a = document.createElement("a");
  a.href = href;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
}

function Move({ project }: { project: string }) {
  return (
    <Row title="Move to another box" line="Moving runs from your terminal, on the computer where Tiffin knows both boxes. Put the other box’s name in place of <box>.">
      <Command cmd={`tiffin projects move ${project} --to <box>`} className="max-w-[34rem]" wrap />
      <p className="mt-3 max-w-[40rem] text-[0.8125rem] text-ink-2">
        It copies everything to the other box, where the apps get new addresses and your custom domains follow them. {project} stays here, stopped, until you delete it.
      </p>
    </Row>
  );
}
