"use client";
// A box being deleted, on /account: the setup screen's pieces, run the other
// way. One stage at a time with a small scene, a bar, the four stages, and
// the job's own steps behind "Show details". It polls the box until the
// worker says it's deleted, then rests on a calm "Deleted".
import { useEffect, useState } from "react";
import { DELETE_STAGES, readDeleteSteps, type Step } from "@/lib/cloud/progress";
import { ZONE } from "@/lib/cloud/names";
import { Scene, type DeleteSceneId } from "../start/scenes";
import { RetryDelete } from "./account-actions";
import { gone } from "./words";

export type DeleteJob = { status: string; steps: Step[]; error: string | null } | null;

type Props = {
  box: { id: string; name: string | null };
  status: string;
  job: DeleteJob;
  deletedAt: string | null;
  dataDeleted: boolean | null;
  /** A fixed state, for a preview: no polling. */
  still?: boolean;
};

const SCENES: DeleteSceneId[] = ["unaddress", "unbuild", "sweep", "gone"];

export function Deleting(p: Props) {
  const [status, setStatus] = useState(p.status);
  const [job, setJob] = useState(p.job);
  const [deletedAt, setDeletedAt] = useState(p.deletedAt);
  const [dataDeleted, setDataDeleted] = useState(p.dataDeleted);
  const failed = job?.status === "failed";

  useEffect(() => {
    if (p.still || status !== "deleting" || failed) return;
    let stop = false;
    let t: ReturnType<typeof setTimeout>;
    const tick = async () => {
      try {
        const res = await fetch(`/api/cloud/boxes/${p.box.id}?job=delete_server`, { cache: "no-store" });
        const j = (await res.json()) as { box?: { status: string; deletedAt: string | null; dataDeleted: boolean | null }; job?: DeleteJob };
        if (!stop && j.box) {
          setStatus(j.box.status);
          setDeletedAt(j.box.deletedAt);
          setDataDeleted(j.box.dataDeleted);
          setJob(j.job ?? null);
        }
      } catch {}
      if (!stop) t = setTimeout(tick, 2000);
    };
    t = setTimeout(tick, 1500);
    return () => {
      stop = true;
      clearTimeout(t);
    };
  }, [p.still, p.box.id, status, failed]);

  const steps = job?.steps ?? [];
  const r = readDeleteSteps(steps, status);
  const stage = DELETE_STAGES[r.stage];
  const domain = p.box.name ? `${p.box.name}.${ZONE}` : null;
  const name = p.box.name ?? "This box";

  let title: string = stage.title;
  let say: React.ReactNode = stage.blurb;
  if (r.done) {
    title = `${name} is deleted`;
    say = gone({ name: p.box.name, deletedAt: deletedAt ? new Date(deletedAt) : null, dataDeleted: Boolean(dataDeleted) });
  } else if (failed) {
    title = "Deleting stopped";
    say = r.stage > 0 ? `${domain ?? "The address"} is already removed. The server is still there.` : "Nothing was deleted yet.";
  } else if (job?.error) {
    say = "A step didn't work; we try again in a minute.";
  }
  const pct = r.done ? 100 : Math.round(((r.stage + (failed ? 0 : 0.5)) / (DELETE_STAGES.length - 1)) * 100);

  return (
    <article className="cp-card pg pg-del" id={p.box.id} data-state={r.done ? "done" : failed ? "stopped" : "running"} aria-labelledby={`${p.box.id}-name`}>
      <Scene id={r.done ? "gone" : failed ? "stopped" : SCENES[r.stage]} name={p.box.name ?? ""} className="pg-scene" key={r.done ? "gone" : failed ? "stopped" : r.stage} />
      <div className="pg-del-body">
        {domain && <p className="pg-eyebrow">{domain}</p>}
        <h2 className="pg-title" id={`${p.box.id}-name`} aria-live="polite">
          {title}
        </h2>
        <p className="pg-say">{say}</p>
        {failed && job?.error && (
          <p className="pg-why">
            <span>What went wrong</span>
            {job.error}
          </p>
        )}
        {!r.done && !failed && (
          <div
            className="pg-meter"
            role="progressbar"
            aria-label="Deleting"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={pct}
            aria-valuetext={`${stage.title}, step ${r.stage + 1} of ${DELETE_STAGES.length}`}
          >
            <span style={{ width: `${pct}%` }} />
          </div>
        )}
        <ol className="pg-stages pg-del-stages" aria-label="Stages">
          {DELETE_STAGES.map((s, i) => {
            const state = r.done || i < r.stage ? "done" : i === r.stage && !failed ? "now" : "next";
            return (
              <li key={s.id} data-state={state} aria-current={state === "now" ? "step" : undefined}>
                <span className="pg-mark" aria-hidden="true" />
                <span>{s.title}</span>
                <span className="sr-only">{state === "done" ? " (done)" : state === "now" ? " (now)" : ""}</span>
              </li>
            );
          })}
        </ol>
        {failed && p.box.name && <RetryDelete id={p.box.id} name={p.box.name} domain={domain ?? p.box.name} />}
        {steps.length > 0 && (
          <details className="pg-details">
            <summary>Show details</summary>
            <ol className="pg-log">
              {steps.map((s, i) => {
                const off = Math.max(0, Math.round((Date.parse(s.at) - Date.parse(steps[0].at)) / 1000)) || 0;
                return (
                  <li key={i}>
                    <time dateTime={s.at} title={s.at}>{`${Math.floor(off / 60)}:${String(off % 60).padStart(2, "0")}`}</time>
                    <span>{s.text}</span>
                  </li>
                );
              })}
            </ol>
          </details>
        )}
      </div>
    </article>
  );
}
