import { useMutation } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import type { ReactNode } from "react";
import { ProblemNote } from "./problem";
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

/** A small confirm dialog for one-line actions (an alert dialog: a click outside doesn't dismiss it). Destructive by default. */
export function Confirm({
  open,
  onClose,
  title,
  body,
  action,
  run,
  done,
  tone = "danger",
  cancel = "Keep it",
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  body: ReactNode;
  action: string;
  run: () => Promise<unknown>;
  done: () => void;
  tone?: "danger" | "normal";
  /** The button that closes it without doing anything. */
  cancel?: string;
}) {
  return (
    <AlertDialog open={open} onOpenChange={(o) => !o && onClose()}>
      <AlertDialogContent className="max-w-md">
        {open && <ConfirmBody title={title} body={body} action={action} run={run} done={done} onClose={onClose} tone={tone} cancel={cancel} />}
      </AlertDialogContent>
    </AlertDialog>
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
  cancel,
}: {
  title: string;
  body: ReactNode;
  action: string;
  run: () => Promise<unknown>;
  done: () => void;
  onClose: () => void;
  tone: "danger" | "normal";
  cancel: string;
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
      <AlertDialogHeader>
        <AlertDialogTitle>{title}</AlertDialogTitle>
        <AlertDialogDescription>{body}</AlertDialogDescription>
      </AlertDialogHeader>
      {m.isError && (
        <AlertDialogBody>
          <ProblemNote error={m.error} />
        </AlertDialogBody>
      )}
      <AlertDialogFooter>
        <AlertDialogCancel asChild>
          <Button variant="ghost">{cancel}</Button>
        </AlertDialogCancel>
        <Button variant={tone === "danger" ? "danger" : "primary"} disabled={m.isPending} onClick={() => m.mutate()}>
          {tone === "danger" && <Trash2 />}
          {m.isPending ? "Working…" : action}
        </Button>
      </AlertDialogFooter>
    </>
  );
}
