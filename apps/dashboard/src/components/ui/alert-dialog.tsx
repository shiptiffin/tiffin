import { AlertDialog as A } from "radix-ui";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/lib/cn";
import { DialogBody, DialogFooter, dialogOverlay, dialogPanel } from "./dialog";

/**
 * A confirmation that interrupts: role="alertdialog", focus starts on Cancel,
 * and a click outside does not dismiss it (Escape and Cancel do). Use it for
 * destructive or irreversible confirms; everything else is a Dialog.
 *
 *   <AlertDialog open={open} onOpenChange={setOpen}>
 *     <AlertDialogContent tone="danger" className="max-w-md">
 *       <AlertDialogHeader><AlertDialogTitle>…</AlertDialogTitle><AlertDialogDescription>…</AlertDialogDescription></AlertDialogHeader>
 *       <AlertDialogFooter>
 *         <AlertDialogCancel asChild><Button variant="ghost">Keep it</Button></AlertDialogCancel>
 *         <Button variant="danger" onClick={run}>Delete</Button>
 *       </AlertDialogFooter>
 *     </AlertDialogContent>
 *   </AlertDialog>
 *
 * The confirm button is a plain Button, not AlertDialog.Action, so the dialog
 * stays open while the request runs and can show its error.
 */
export const AlertDialog = A.Root;

export function AlertDialogTrigger(props: ComponentProps<typeof A.Trigger>) {
  return <A.Trigger data-slot="alert-dialog-trigger" {...props} />;
}

export function AlertDialogCancel(props: ComponentProps<typeof A.Cancel>) {
  return <A.Cancel data-slot="alert-dialog-cancel" {...props} />;
}

export function AlertDialogAction(props: ComponentProps<typeof A.Action>) {
  return <A.Action data-slot="alert-dialog-action" {...props} />;
}
export const AlertDialogBody = DialogBody;
export const AlertDialogFooter = DialogFooter;

export function AlertDialogContent({ className, tone = "default", ...props }: ComponentProps<typeof A.Content> & { tone?: "default" | "danger" }) {
  return (
    <A.Portal>
      <A.Overlay data-slot="alert-dialog-overlay" className={dialogOverlay} />
      <A.Content
        data-slot="alert-dialog-content"
        className={cn(dialogPanel, tone === "danger" ? "border-danger" : "border-rule-2", className)}
        {...props}
      />
    </A.Portal>
  );
}

/** Like DialogHeader, without the room on the right for a close button (an alert dialog has none). */
export function AlertDialogHeader({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div data-slot="alert-dialog-header" className={cn("[:where(&)]:px-6 [:where(&)]:pt-6 [:where(&)]:pb-4", className)}>
      {children}
    </div>
  );
}

export function AlertDialogTitle({ className, ...props }: ComponentProps<typeof A.Title>) {
  return <A.Title data-slot="alert-dialog-title" className={cn("[:where(&)]:text-lg font-[550] tracking-[-0.01em] [:where(&)]:text-ink", className)} {...props} />;
}

export function AlertDialogDescription({ className, ...props }: ComponentProps<typeof A.Description>) {
  return (
    <A.Description
      data-slot="alert-dialog-description"
      className={cn("[:where(&)]:mt-1.5 [:where(&)]:text-base [:where(&)]:text-ink-2", className)}
      {...props}
    />
  );
}
