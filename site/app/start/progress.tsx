"use client";
// The box being made: one stage at a time with a little scene, an honest
// estimate, and the provisioner's own steps behind "Show details".
import { useEffect, useState } from "react";
import { ZONE } from "@/lib/cloud/names";
import { duration, estimate, fraction, readSteps, STAGES, type Reading, type Step } from "@/lib/cloud/progress";
import { EMAIL } from "../chrome";
import { Scene } from "./scenes";

export type Job = { status: string; steps: Step[]; error: string | null } | null;
type Box = { id: string; name: string | null };

/** The clock, ticking every few seconds (or fixed, for a preview). */
function useNow(fixed?: number, every = 5000) {
  const [now, setNow] = useState(() => fixed ?? Date.now());
  useEffect(() => {
    if (fixed != null) return;
    const t = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(t);
  }, [fixed, every]);
  return fixed ?? now;
}

function Details({ steps, open }: { steps: Step[]; open?: boolean }) {
  if (steps.length === 0) return null;
  const t0 = Date.parse(steps[0].at);
  return (
    <details className="pg-details" open={open}>
      <summary>Show details</summary>
      <p className="cp-hint">Every step the setup took, as it logged it. Handy if you write to us.</p>
      <ol className="pg-log">
        {steps.map((s, i) => {
          const t = Date.parse(s.at);
          const off = Number.isFinite(t) && Number.isFinite(t0) ? Math.max(0, Math.round((t - t0) / 1000)) : null;
          return (
            <li key={i}>
              <time dateTime={s.at} title={s.at}>
                {off == null ? "" : `${Math.floor(off / 60)}:${String(off % 60).padStart(2, "0")}`}
              </time>
              <span>{s.text}</span>
            </li>
          );
        })}
      </ol>
    </details>
  );
}

function Stages({ r }: { r: Reading }) {
  return (
    <ol className="pg-stages" aria-label="Stages">
      {STAGES.map((s, i) => {
        const state = r.ready || i < r.stage ? "done" : i === r.stage ? "now" : "next";
        return (
          <li key={s.id} data-state={state} aria-current={state === "now" ? "step" : undefined}>
            <span className="pg-mark" aria-hidden="true" />
            <span>{s.title}</span>
            <span className="sr-only">{state === "done" ? " (done)" : state === "now" ? " (now)" : ""}</span>
          </li>
        );
      })}
    </ol>
  );
}

export function Progress({ box, status, job, now: fixedNow }: { box: Box; status: string; job: Job; now?: number }) {
  const steps = job?.steps ?? [];
  const r = readSteps(steps, status);
  const now = useNow(fixedNow);
  const domain = `${box.name}.${ZONE}`;

  if (r.ready || status === "active") {
    const took = r.firstAt != null && r.lastAt != null && r.lastAt > r.firstAt ? duration(r.lastAt - r.firstAt) : null;
    return (
      <div className="cp-card pg pg-ready">
        <Scene id="ready" name={box.name ?? ""} className="pg-scene" />
        <div aria-live="polite">
          <p className="pg-eyebrow">{domain}</p>
          <h2 className="pg-title">Up and running</h2>
          <p className="pg-say">{took ? `Made in ${took}. ` : ""}It runs on your own server, and our setup key is gone from it. We hold only the sign-in link below, until you use it.</p>
        </div>
        <div className="cp-row">
          <a className="btn btn-primary pg-open" href={`/api/cloud/boxes/${box.id}/open`}>
            Open your dashboard
          </a>
          <a className="btn btn-quiet" href="/account">
            Your account
          </a>
        </div>
        <p className="cp-hint">
          This first time signs you in with a one-time link your box made (it works once, within 24 hours). Add a passkey in the dashboard
          then: after that, you sign in on the box itself.
        </p>
        <Details steps={steps} />
      </div>
    );
  }

  if (r.attention) {
    return (
      <div className="cp-card pg">
        <Scene id="stopped" name={box.name ?? ""} className="pg-scene" />
        <div aria-live="polite">
          <p className="pg-eyebrow">{domain}</p>
          <h2 className="pg-title">Nearly there, but it needs a hand</h2>
          <p className="pg-say">
            Tiffin is installed, but the last setup steps didn&rsquo;t finish. Your server and its data are kept. We look at it and email
            you; there&rsquo;s nothing you need to do.
          </p>
        </div>
        <Details steps={steps} />
      </div>
    );
  }

  const stage = STAGES[r.stage];
  const pct = Math.round(fraction(r, now) * 100);
  const waiting = r.certWaiting;
  const title = waiting ? "Waiting for its certificate" : stage.title;
  const say = waiting
    ? "Tiffin is installed and running. Its HTTPS certificate from Let's Encrypt hasn't arrived yet, which sometimes takes a while."
    : r.say
      ? `${r.say}.`
      : stage.blurb;
  const pastLock = r.stage > STAGES.findIndex((s) => s.id === "lock");
  return (
    <div className="cp-card pg">
      <Scene id={waiting ? "waiting" : stage.id} name={box.name ?? ""} className="pg-scene" key={waiting ? "waiting" : stage.id} />
      <div>
        <p className="pg-eyebrow">{domain}</p>
        {/* Announced when the stage changes, not on every step. */}
        <h2 className="pg-title" aria-live="polite">
          {title}
        </h2>
        <p className="pg-say">{say}</p>
      </div>
      <div className="pg-meter-wrap">
        <div
          className="pg-meter"
          role="progressbar"
          aria-label="Setup progress"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={pct}
          aria-valuetext={`Stage ${r.stage + 1} of ${STAGES.length}: ${title}`}
        >
          <span style={{ width: `${pct}%` }} />
        </div>
        <p className="pg-meter-text">
          <span>
            Stage {r.stage + 1} of {STAGES.length}
          </span>
          <span>{estimate(r, now)}</span>
        </p>
      </div>
      <Stages r={r} />
      <p className="pg-trust">
        {pastLock
          ? "Our temporary access to your server is gone, and your Hetzner API token is forgotten."
          : "At the end we remove our access to your server and forget your Hetzner API token."}{" "}
        You can close this page; we email you when it&rsquo;s ready.
      </p>
      <Details steps={steps} />
    </div>
  );
}

/** The setup stopped before Tiffin was installed: what happened, what we cleaned up, what to do. */
export function Stopped({ box, job }: { box: Box; job: Job }) {
  const steps = job?.steps ?? [];
  const r = readSteps(steps, "failed");
  const at = steps.length > 0 ? STAGES[r.stage].title.toLowerCase() : null;
  // What the clean-up said, so we never claim more than it did.
  const retrying = steps.some((s) => /; we try again$/.test(s.text));
  const leftover = steps.find((s) => /so what the setup made stays/.test(s.text));
  return (
    <div className="cp-card pg" role="alert">
      <Scene id="stopped" name={box.name ?? ""} className="pg-scene" />
      <div>
        {box.name && <p className="pg-eyebrow">{`${box.name}.${ZONE}`}</p>}
        <h2 className="pg-title">Setup stopped</h2>
        <p className="pg-say">{at ? `It stopped while ${at}. ` : ""}You can try again right away.</p>
      </div>
      {job?.error && (
        <p className="pg-why">
          <span>What went wrong</span>
          {job.error}
        </p>
      )}
      <ul className="pg-cleanup">
        {!retrying && <li>We removed the box&rsquo;s address.</li>}
        {leftover ? (
          <li>
            What this try made is still in your Hetzner project: delete what carries the label shiptiffin-box={box.id} in the Hetzner
            console, or just try again (it cleans up first).
          </li>
        ) : retrying ? (
          <li>
            We&rsquo;re still removing the box&rsquo;s address and what this try made in your Hetzner project, and keep at it until
            they&rsquo;re gone.
          </li>
        ) : (
          <li>
            We deleted what this try made in your Hetzner project: only what carries this box&rsquo;s shiptiffin-box label, nothing else in
            your account.
          </li>
        )}
        <li>We forgot your Hetzner API token.</li>
      </ul>
      <p className="cp-hint">
        To try again, paste a token below. If it stops again, write to <a href={`mailto:${EMAIL}`}>{EMAIL}</a> and include the details.
      </p>
      <Details steps={steps} />
    </div>
  );
}
