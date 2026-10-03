import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, CornerUpLeft, Undo2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { ApiError, api, type Change, type Plan } from "@/api/client";
import { q } from "@/api/queries";
import { ActorMark, actorKindLabel } from "@/components/actor";
import { CopyValue } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { OpCounts, OpView } from "@/components/op";
import { Code, ProblemNote, sentence } from "@/components/problem";
import { RiskBadge, RiskMark } from "@/components/risk";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { asTier, opCounts, tierCopy, tierRank } from "@/lib/changes";
import { full, relative } from "@/lib/time";
import { Page } from "./activity";

export function ChangePage({ id }: { id: string }) {
  const { data: c, isPending, error } = useQuery(q.change(id));
  useTitle(c ? c.intent || "Change" : "Change");
  const [undoOpen, setUndoOpen] = useState(false);
  const [justUndone, setJustUndone] = useState<string | null>(null);

  if (isPending)
    return (
      <Page>
        <div className="h-10 w-2/3 animate-pulse rounded-md bg-hover" />
      </Page>
    );
  if (error || !c)
    return (
      <Page>
        <Back />
        <ProblemNote
          className="mt-6"
          error={error}
          title={
            error instanceof ApiError && error.status === 404
              ? "That change doesn't exist, or it belongs to a project you can't see"
              : "Couldn't load this change"
          }
        />
      </Page>
    );

  const tier = asTier(c.plan.risk);
  const ops = c.plan.ops ?? [];
  const undoneBy = justUndone ?? c.undoneBy;

  return (
    <Page>
      <Back />
      <header className="relative mt-6 animate-rise">
        <div className="flex flex-wrap items-center gap-3">
          <RiskBadge tier={tier} />
          <span className="text-sm text-ink-3">
            <code className="font-mono text-ink-2">{c.project}</code> · version {c.version}
          </span>
        </div>
        <h1 className={cn("display mt-3 text-2xl text-ink sm:text-[2.125rem] sm:leading-[2.6rem]", undoneBy && "text-ink-2")}>
          {c.undoOf && <CornerUpLeft className="mr-2 mb-1 inline size-6 text-ink-3" aria-label="Undo:" />}
          {c.intent || <em className="text-ink-3">No intent given</em>}
        </h1>
        <p className="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 text-base text-ink-2">
          <ActorMark actor={c.actor} />
          <span className="font-medium text-ink">{c.actor.name || c.actor.id}</span>
          <span className="text-ink-3">({actorKindLabel(c.actor.kind)})</span>
          {c.actor.session && (
            <>
              <span className="text-ink-3">in session</span>
              <code className="font-mono text-sm">{c.actor.session}</code>
            </>
          )}
          <span className="text-ink-4">·</span>
          <time dateTime={c.at} title={full(c.at)} className="text-ink-3">
            {relative(c.at)}
          </time>
        </p>
        {undoneBy && <UndoneStamp fresh={!!justUndone} />}
      </header>

      {tier === "irreversible" && (
        <div className="mt-6 flex gap-3 rounded-lg border border-irr-rule bg-irr-wash px-4 py-3 text-base">
          <RiskMark tier="irreversible" className="mt-0.5 size-4" />
          <p className="text-ink">
            <span className="font-medium">This change destroyed data.</span>{" "}
            <span className="text-ink-2">Undo can bring back the settings, but not what was deleted.</span>
          </p>
        </div>
      )}

      <section className="mt-8 grid gap-6 border-y border-rule py-5 sm:grid-cols-[1fr_auto] sm:items-center">
        <dl className="grid grid-cols-[7.5rem_1fr] gap-x-4 gap-y-2 text-base">
          <Meta label="Plan hash">
            <CopyValue value={c.plan.hash} display={`${c.plan.hash.slice(0, 12)}…${c.plan.hash.slice(-4)}`} />
          </Meta>
          <Meta label="Change">
            <CopyValue value={c.id} />
          </Meta>
          <Meta label="Applied">
            <span className="text-ink-2">{full(c.at)}</span>
          </Meta>
          <Meta label="Steps">
            <span className="flex items-center gap-3 text-ink-2">
              {stepsSentence(ops)}
              <OpCounts ops={ops} className="hidden sm:inline-flex" />
            </span>
          </Meta>
          {c.undoOf && (
            <Meta label="Undoes">
              <Link
                to="/changes/$id"
                params={{ id: c.undoOf }}
                className="font-mono text-sm text-brass-ink underline underline-offset-4 hover:text-ink"
              >
                {c.undoOf}
              </Link>
            </Meta>
          )}
          {undoneBy && (
            <Meta label="Undone by">
              <Link
                to="/changes/$id"
                params={{ id: undoneBy }}
                className="font-mono text-sm text-brass-ink underline underline-offset-4 hover:text-ink"
              >
                {undoneBy}
              </Link>
            </Meta>
          )}
        </dl>
        {!undoneBy && (
          <div className="flex flex-col items-start gap-1.5 sm:items-end">
            <Button onClick={() => setUndoOpen(true)} size="lg" variant="secondary">
              <Undo2 />
              Undo this change
            </Button>
            <span className="text-xs text-ink-3">You'll review the undo plan first.</span>
          </div>
        )}
      </section>

      <section className="mt-10" aria-labelledby="steps">
        <h2 id="steps" className="display-italic mb-4 text-xl text-ink">
          What changed
        </h2>
        <div className="flex flex-col gap-3">
          {ops.map((op, k) => (
            <div key={op.address + k} className="animate-rise" style={{ animationDelay: `${80 + k * 40}ms` }}>
              <OpView op={op} />
            </div>
          ))}
        </div>
      </section>

      <UndoDialog change={c} open={undoOpen} onOpenChange={setUndoOpen} onUndone={(nid) => setJustUndone(nid)} />
    </Page>
  );
}

function Back() {
  return (
    <Link to="/" search={{}} className="inline-flex items-center gap-1.5 rounded-md text-sm text-ink-3 transition-colors hover:text-ink">
      <ArrowLeft className="size-3.5" />
      Activity
    </Link>
  );
}

function Meta({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="pt-1 text-sm text-ink-3">{label}</dt>
      <dd className="flex min-h-7 min-w-0 items-center">{children}</dd>
    </>
  );
}

function UndoneStamp({ fresh }: { fresh: boolean }) {
  return (
    <div
      aria-label="This change was undone"
      className={cn(
        "pointer-events-none absolute -top-2 right-0 hidden rotate-[-6deg] rounded-md border-2 border-ink-3/70 px-3 py-1 font-mono text-sm font-medium tracking-[0.2em] text-ink-3/80 uppercase sm:block",
        fresh && "animate-stamp",
      )}
    >
      Undone
    </div>
  );
}

type Phase =
  { k: "loading" } | { k: "review"; plan: Plan; changed?: boolean } | { k: "applying"; plan: Plan } | { k: "error"; error: unknown; plan?: Plan };

function UndoDialog({
  change,
  open,
  onOpenChange,
  onUndone,
}: {
  change: Change;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onUndone: (id: string) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open && <UndoBody change={change} onOpenChange={onOpenChange} onUndone={onUndone} />}
    </Dialog>
  );
}

function UndoBody({ change, onOpenChange, onUndone }: { change: Change; onOpenChange: (o: boolean) => void; onUndone: (id: string) => void }) {
  const qc = useQueryClient();
  const [phase, setPhase] = useState<Phase>({ k: "loading" });
  const [typed, setTyped] = useState("");
  const [attempt, setAttempt] = useState(0);

  const done = (newId?: string) => {
    qc.invalidateQueries({ queryKey: ["changes"] });
    qc.invalidateQueries({ queryKey: ["change"] });
    qc.invalidateQueries({ queryKey: ["projects"] });
    onOpenChange(false);
    if (newId) onUndone(newId);
  };

  // Ask for the undo plan: without a confirm hash the API answers 428 with it.
  useEffect(() => {
    let live = true;
    api
      .undo(change.id)
      .then(() => live && done())
      .catch((e) => {
        if (!live) return;
        if (e instanceof ApiError && e.status === 428 && e.problem.plan) setPhase({ k: "review", plan: e.problem.plan });
        else setPhase({ k: "error", error: e });
      });
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [change.id, attempt]);

  const fetchPlan = () => {
    setPhase({ k: "loading" });
    setTyped("");
    setAttempt((n) => n + 1);
  };

  const confirm = async (plan: Plan) => {
    setPhase({ k: "applying", plan });
    try {
      const r = await api.undo(change.id, plan.hash);
      done(r.change?.id);
    } catch (e) {
      if (e instanceof ApiError && e.status === 428 && e.problem.plan) setPhase({ k: "review", plan: e.problem.plan, changed: true });
      else setPhase({ k: "error", error: e, plan });
    }
  };

  const plan = phase.k === "review" || phase.k === "applying" ? phase.plan : phase.k === "error" ? phase.plan : undefined;
  const tier = plan ? asTier(plan.risk) : "reversible";
  const serious = tier === "irreversible";
  const needsTyping = serious;
  const typedOk = !needsTyping || typed.trim() === change.project;

  return (
    <DialogContent tone={serious ? "danger" : "default"} className="max-w-2xl" onOpenAutoFocus={(e) => e.preventDefault()}>
      <DialogHeader>
        <DialogTitle className={cn(serious && "text-irr")}>{serious ? "This undo destroys data" : "Review the undo"}</DialogTitle>
        <DialogDescription>
          {phase.k === "loading" ? (
            "Working out what undo would do. Nothing changes until you confirm."
          ) : plan ? (
            <>
              Undoing <span className="text-ink">“{change.intent}”</span> takes {plan.ops?.length ?? 0} {plan.ops?.length === 1 ? "step" : "steps"} in{" "}
              <code className="font-mono text-sm text-ink">{plan.project}</code>. Nothing changes until you confirm.
            </>
          ) : (
            "Undo couldn't be planned."
          )}
        </DialogDescription>
      </DialogHeader>
      <DialogBody>
        {phase.k === "loading" && (
          <div className="space-y-2">
            <div className="h-16 animate-pulse rounded-lg bg-hover" />
            <div className="h-16 animate-pulse rounded-lg bg-hover/60" />
          </div>
        )}
        {phase.k === "review" && phase.changed && (
          <p className="mb-3 rounded-lg border border-out/40 bg-out-wash px-3 py-2 text-base text-ink">
            The project moved while you were reviewing, so this is a fresh plan. Check it again.
          </p>
        )}
        {phase.k === "error" && <UndoProblem error={phase.error} />}
        {plan && (
          <>
            <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
              <RiskBadge tier={tier} size="sm" />
              <span className="text-ink-3">{tierCopy[tier].blurb}</span>
            </div>
            <div className="flex flex-col gap-2.5">
              {(plan.ops ?? [])
                .slice()
                .sort((a, b) => (tierRank[b.risk] ?? 4) - (tierRank[a.risk] ?? 4))
                .map((op, k) => (
                  <OpView key={op.address + k} op={op} compact />
                ))}
            </div>
            {asTier(change.plan.risk) === "irreversible" && !serious && (
              <p className="mt-3 text-sm text-ink-3">The original change deleted data. Undo restores the settings; the data itself is gone.</p>
            )}
            <p className="mt-4 flex items-center gap-2 text-sm text-ink-3">
              Undo plan
              <CopyValue value={plan.hash} display={plan.hash.slice(0, 12)} />
            </p>
            {needsTyping && phase.k !== "error" && (
              <label className="mt-4 block">
                <span className="text-base text-ink">
                  Type <code className="rounded-xs bg-irr-wash px-1 font-mono text-irr">{change.project}</code> to confirm.
                </span>
                <Input
                  className="mt-2 font-mono"
                  value={typed}
                  onChange={(e) => setTyped(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  aria-label={`Type ${change.project} to confirm`}
                />
              </label>
            )}
          </>
        )}
      </DialogBody>
      <DialogFooter>
        <Button variant="ghost" onClick={() => onOpenChange(false)}>
          {phase.k === "error" ? "Close" : "Keep it"}
        </Button>
        {phase.k === "error" && !plan && (
          <Button variant="secondary" onClick={fetchPlan}>
            Plan again
          </Button>
        )}
        {(phase.k === "review" || phase.k === "applying") && (
          <Button
            variant={serious ? "danger" : "primary"}
            disabled={!typedOk || phase.k === "applying"}
            onClick={() => confirm(phase.plan)}
            autoFocus={!needsTyping}
          >
            <Undo2 />
            {phase.k === "applying" ? "Undoing…" : serious ? "Destroy and undo" : "Confirm undo"}
          </Button>
        )}
      </DialogFooter>
    </DialogContent>
  );
}

function UndoProblem({ error }: { error: unknown }) {
  if (error instanceof ApiError) {
    const p = error.problem;
    if (p.code === "precondition" || (error.status === 409 && p.code !== "conflict")) {
      return (
        <Callout title="Something changed since, so undo would overwrite newer work">
          <p>{sentence(p.detail ?? "")}</p>
          <p className="mt-1 text-ink-3">
            Look at the newer changes in the activity log. Undo them first, or change the project by hand and apply a new plan.
          </p>
        </Callout>
      );
    }
    if (error.status === 409) {
      return (
        <Callout title="The project moved on while you were deciding">
          <p className="text-ink-3">Close this and press Undo again to get a fresh plan.</p>
        </Callout>
      );
    }
    if (error.status === 403) {
      return (
        <Callout title="Your session isn't allowed to apply this undo">
          <p>{sentence(p.detail ?? "")}</p>
          {p.hint && (
            <p className="mt-1 text-ink-3">
              <Code text={sentence(p.hint)} />
            </p>
          )}
        </Callout>
      );
    }
  }
  return <ProblemNote error={error} />;
}

function Callout({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div role="alert" className="mb-4 rounded-lg border border-out/40 bg-out-wash px-4 py-3 text-base text-ink-2">
      <p className="font-medium text-ink">{title}</p>
      <div className="mt-1">{children}</div>
    </div>
  );
}

function stepsSentence(ops: Change["plan"]["ops"]) {
  const c = opCounts(ops);
  const parts = [c.create && `${c.create} added`, c.update && `${c.update} updated`, c.delete && `${c.delete} removed`].filter(Boolean);
  return parts.length ? parts.join(", ") : "Nothing";
}
