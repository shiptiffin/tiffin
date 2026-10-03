import { useMutation } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import type { ReactNode } from "react";
import { ProblemNote } from "./problem";
import { Button } from "./ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";

/** A small confirm dialog for one-line actions. Destructive by default. */
export function Confirm({
  open,
  onClose,
  title,
  body,
  action,
  run,
  done,
  tone = "danger",
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  body: ReactNode;
  action: string;
  run: () => Promise<unknown>;
  done: () => void;
  tone?: "danger" | "normal";
}) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-md">
        {open && <ConfirmBody title={title} body={body} action={action} run={run} done={done} onClose={onClose} tone={tone} />}
      </DialogContent>
    </Dialog>
  );
}

function ConfirmBody({
  title,
  body,
  action,
  run,
  done,
  onClose,
  tone,
}: {
  title: string;
  body: ReactNode;
  action: string;
  run: () => Promise<unknown>;
  done: () => void;
  onClose: () => void;
  tone: "danger" | "normal";
}) {
  const m = useMutation({
    mutationFn: run,
    onSuccess: () => {
      done();
      onClose();
    },
  });
  return (
    <>
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{body}</DialogDescription>
      </DialogHeader>
      {m.isError && (
        <DialogBody>
          <ProblemNote error={m.error} />
        </DialogBody>
      )}
      <DialogFooter>
        <Button variant="ghost" onClick={onClose} autoFocus>
          Keep it
        </Button>
        <Button variant={tone === "danger" ? "danger" : "primary"} disabled={m.isPending} onClick={() => m.mutate()}>
          {tone === "danger" && <Trash2 />}
          {m.isPending ? "Working…" : action}
        </Button>
      </DialogFooter>
    </>
  );
}
