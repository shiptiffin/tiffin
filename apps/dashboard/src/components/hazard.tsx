import { useEffect, useEffectEvent, useState, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { ProblemNote } from "./problem";
import { RiskMark } from "./risk";
import { Button } from "./ui/button";
import {
  AlertDialog,
  AlertDialogBody,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "./ui/alert-dialog";
import { Input } from "./ui/input";

type Phase<P> =
  { k: "loading" } | { k: "review"; confirm: string; preview: P } | { k: "running"; confirm: string; preview: P } | { k: "error"; error: unknown };

/**
 * The confirm flow for operations that overwrite data: ask once without a
 * confirm value (the API answers 428 with a preview and the value), show the
 * preview with the hazard treatment, make the person type a word, then run it
 * with the value. Same feel as undoing an irreversible change.
 */
export function HazardDialog<P, R>({
  open,
  onOpenChange,
  title,
  word,
  run,
  renderPreview,
  action,
  onDone,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: string;
  word: string;
  run: (confirm?: string) => Promise<R>;
  renderPreview: (p: P) => ReactNode;
  action: string;
  onDone: (r: R) => void;
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      {open && (
        <AlertDialogContent tone="danger" onOpenAutoFocus={(e) => e.preventDefault()}>
          <Body
            title={title}
            word={word}
            run={run}
            renderPreview={renderPreview}
            action={action}
            onDone={onDone}
            onClose={() => onOpenChange(false)}
          />
        </AlertDialogContent>
      )}
    </AlertDialog>
  );
}

function Body<P, R>({
  title,
  word,
  run,
  renderPreview,
  action,
  onDone,
  onClose,
}: {
  title: string;
  word: string;
  run: (confirm?: string) => Promise<R>;
  renderPreview: (p: P) => ReactNode;
  action: string;
  onDone: (r: R) => void;
  onClose: () => void;
}) {
  const [phase, setPhase] = useState<Phase<P>>({ k: "loading" });
  const [typed, setTyped] = useState("");
  // Ask once, on open; the callbacks are read when the answer comes, not reacted to.
  const ask = useEffectEvent(() => run());
  const finish = useEffectEvent((r: R) => {
    onDone(r);
    onClose();
  });
  useEffect(() => {
    let live = true;
    ask()
      .then((r) => {
        // Nothing to confirm: it already ran.
        if (!live) return;
        finish(r);
      })
      .catch((e) => {
        if (!live) return;
        if (e instanceof ApiError && e.status === 428 && e.problem.confirm)
          setPhase({ k: "review", confirm: e.problem.confirm, preview: e.problem.preview as P });
        else setPhase({ k: "error", error: e });
      });
    return () => {
      live = false;
    };
  }, []);

  const go = async () => {
    if (phase.k !== "review") return;
    setPhase({ ...phase, k: "running" });
    try {
      const r = await run(phase.confirm);
      onDone(r);
      onClose();
    } catch (e) {
      setPhase({ k: "error", error: e });
    }
  };

  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle className="flex items-center gap-2 text-danger">
          <RiskMark tier="irreversible" className="size-4" />
          {title}
        </AlertDialogTitle>
        <AlertDialogDescription>Nothing has changed yet. Read what this overwrites, then confirm.</AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogBody>
        {phase.k === "loading" && <div className="h-24 animate-pulse rounded-lg bg-paper-sunk" />}
        {phase.k === "error" && <ProblemNote error={phase.error} />}
        {(phase.k === "review" || phase.k === "running") && (
          <>
            {renderPreview(phase.preview)}
            <label className="mt-5 block">
              <span className="text-base text-ink">
                Type <code className="rounded-xs bg-danger-wash px-1 font-mono text-danger">{word}</code> to confirm.
              </span>
              <Input
                className="mt-2 font-mono"
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                aria-label={`Type ${word} to confirm`}
              />
            </label>
          </>
        )}
      </AlertDialogBody>
      <AlertDialogFooter>
        <AlertDialogCancel asChild>
          <Button variant="ghost">{phase.k === "error" ? "Close" : "Keep everything as it is"}</Button>
        </AlertDialogCancel>
        {(phase.k === "review" || phase.k === "running") && (
          <Button variant="danger" disabled={typed.trim() !== word || phase.k === "running"} onClick={go}>
            {phase.k === "running" ? "Working…" : action}
          </Button>
        )}
      </AlertDialogFooter>
    </>
  );
}
