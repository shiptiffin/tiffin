import { useMutation } from "@tanstack/react-query";
import { CircleAlert, MailCheck, Inbox } from "lucide-react";
import { useState } from "react";
import { boxMail, type BoxMailResult } from "@/api/modules";
import { isProblem, type Person } from "@/api/client";
import { ConfirmItsYou } from "@/components/confirm-its-you";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { cn } from "@/lib/cn";

// People and their email: the address the box sends invites, sign-in links
// and new sign-in notices to, and what happened when it tried.

/** One line on what became of an emailed link. */
export function MailOutcome({ result, className }: { result: BoxMailResult; className?: string }) {
  const sent = result.delivery === "relay";
  const Icon = sent ? MailCheck : result.delivery === "inbox" ? Inbox : CircleAlert;
  const words =
    result.delivery === "relay"
      ? `Emailed to ${result.to}.`
      : result.delivery === "inbox"
        ? "No mail service is connected, so the email waits in the box’s dev inbox. Send them the link yourself."
        : result.delivery === "suppressed"
          ? `${result.to} bounced before, so the box didn’t email it. Send them the link yourself.`
          : `Couldn’t email ${result.to}${result.detail ? `: ${result.detail.replace(/\.$/, "")}` : ""}. Send them the link yourself.`;
  return (
    <p className={cn("flex items-start gap-2 text-[0.875rem]", sent ? "text-ink" : "text-ink-2", className)} role="status">
      <Icon className={cn("mt-[3px] size-4 shrink-0", sent ? "text-ok" : result.delivery === "failed" ? "text-warn-ink" : "text-ink-3")} aria-hidden />
      <span>{words}</span>
    </p>
  );
}

/** Set or clear someone's address. */
export function EmailDialog({ person, self, onClose, onDone }: { person: Person | null; self: boolean; onClose: () => void; onDone: () => void }) {
  return (
    <Dialog open={!!person} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>{person && <EmailForm key={person.id} person={person} self={self} onClose={onClose} onDone={onDone} />}</DialogContent>
    </Dialog>
  );
}

function EmailForm({ person, self, onClose, onDone }: { person: Person; self: boolean; onClose: () => void; onDone: () => void }) {
  const [v, setV] = useState(person.email ?? "");
  const t = v.trim();
  const bad = t !== "" && !/^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/.test(t);
  // Sudo mode: emailed sign-in links go to this address, so changing it
  // needs a sign-in from the last 10 minutes; the box says reauth_required.
  const [confirming, setConfirming] = useState(false);
  const m = useMutation({
    mutationFn: () => boxMail.setEmail(person.id, t),
    onSuccess: () => {
      onDone();
      onClose();
    },
    onError: (e) => isProblem(e, "reauth_required") && setConfirming(true),
  });
  if (confirming)
    return (
      <ConfirmItsYou
        why="Emailed sign-in links go to this address, so Tiffin checks it’s really you first: changing it needs a sign-in from the last 10 minutes."
        back="/settings/people"
        working={m.isPending}
        workingLabel="Saving…"
        onBack={() => (setConfirming(false), m.reset())}
        onConfirmed={() => m.mutate()}
      />
    );
  return (
    <form
      className="flex min-h-0 flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        if (!bad) m.mutate();
      }}
    >
      <DialogHeader>
        <DialogTitle>{self ? "Your email" : `${person.name.split(" ")[0]}’s email`}</DialogTitle>
        <DialogDescription>
          {self
            ? "The box sends your sign-in links here, and tells you when you sign in from a new browser. You can also ask for a link on the sign-in page."
            : "The box sends their invites and sign-in links here, and tells them when they sign in from a new browser."}
        </DialogDescription>
      </DialogHeader>
      <DialogBody className="flex flex-col gap-1.5">
        <Label htmlFor="person-email">Email</Label>
        <Input
          id="person-email"
          type="email"
          autoFocus
          value={v}
          onChange={(e) => setV(e.target.value)}
          placeholder="you@example.com"
          maxLength={254}
          aria-invalid={bad || undefined}
          autoComplete={self ? "email" : "off"}
        />
        <p className={bad ? "text-sm text-danger" : "text-xs text-ink-3"}>{bad ? "That isn’t an email address." : "Leave it empty to remove it."}</p>
        {m.isError && !isProblem(m.error, "reauth_required") && <ProblemNote className="mt-2" error={m.error} />}
      </DialogBody>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={bad || m.isPending || t === (person.email ?? "")}>
          {m.isPending ? "Saving…" : "Save"}
        </Button>
      </DialogFooter>
    </form>
  );
}
