import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Send } from "lucide-react";
import { ToggleGroup } from "radix-ui";
import { useState } from "react";
import { request } from "@/api/client";
import type { EmailSummary } from "@/api/modules";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";

// Pieces of the Email pages: what a message's status means, the status
// filter over the log, and the dialog that sends a test message.

type Status = EmailSummary["status"];
type Tone = "ok" | "sent" | "warn" | "bad" | "quiet";

/** The mail services by their API id, as people say them. */
export const PROVIDER_NAME: Record<string, string> = {
  sendgrid: "SendGrid",
  resend: "Resend",
  postmark: "Postmark",
  ses: "Amazon SES",
  mailgun: "Mailgun",
  brevo: "Brevo",
  cloudflare: "Cloudflare Email Service",
  other: "your SMTP relay",
};

/**
 * A status in a word, with the tone of its mark. "Sent" is handed to the
 * relay; Delivered, Bounced and Marked as spam come back from the relay's
 * provider through its webhook, when that is set up.
 */
export const MAIL_STATUS: Record<Status, { word: string; tone: Tone; about: string }> = {
  delivered: { word: "Delivered", tone: "ok", about: "The receiving server took it, the mail service reports." },
  sent: { word: "Sent", tone: "sent", about: "Handed to the relay, which accepted it." },
  queued: { word: "Queued", tone: "warn", about: "Waiting for the relay. The box retries with backoff." },
  bounced: { word: "Bounced", tone: "bad", about: "The receiving server refused it, the mail service reports." },
  complained: { word: "Marked as spam", tone: "bad", about: "The recipient reported it as spam. The address is now suppressed." },
  failed: { word: "Not delivered", tone: "bad", about: "The relay refused it for good, or every retry failed." },
  suppressed: { word: "Held back", tone: "warn", about: "Every recipient is on the won't-write list, so it wasn't sent." },
  captured: { word: "Dev inbox", tone: "quiet", about: "Caught here and not sent: there's no relay, or it came from a preview." },
};

/** A small round mark for a status: filled for a settled outcome, a ring for one that's only handed over or kept here. */
export function MailDot({ status, className }: { status: Status; className?: string }) {
  const t = MAIL_STATUS[status].tone;
  return (
    <span
      aria-hidden
      className={cn(
        "inline-block size-[7px] shrink-0 rounded-full",
        t === "ok"
          ? "bg-ok"
          : t === "sent"
            ? "shadow-[inset_0_0_0_1.5px_var(--ok)]"
            : t === "bad"
              ? "bg-danger"
              : t === "warn"
                ? "bg-warn"
                : "shadow-[inset_0_0_0_1.5px_var(--ink-4)]",
        className,
      )}
    />
  );
}

/** The status as a mark and a word, for a row or a header. */
export function MailStatus({ status, className }: { status: Status; className?: string }) {
  const s = MAIL_STATUS[status];
  return (
    <span className={cn("inline-flex items-center gap-1.5 [:where(&)]:text-xs", s.tone === "bad" ? "text-danger" : s.tone === "warn" ? "text-warn-ink" : "text-ink-3", className)}>
      <MailDot status={status} />
      {s.word}
    </span>
  );
}

export type MailFilter = "all" | Status;
const ORDER: Status[] = ["delivered", "sent", "queued", "bounced", "complained", "failed", "suppressed", "captured"];
export const isMailFilter = (v: unknown): v is MailFilter => v === "all" || (ORDER as unknown[]).includes(v);

/**
 * The log's status filter: All, then each status the loaded messages have,
 * with its count. A status with none drops out unless it is the one chosen.
 */
export function MailFilters({ messages, value, onChange }: { messages: EmailSummary[]; value: MailFilter; onChange: (f: MailFilter) => void }) {
  const counts = new Map<Status, number>();
  for (const m of messages) counts.set(m.status, (counts.get(m.status) ?? 0) + 1);
  const shown = ORDER.filter((s) => counts.get(s) || s === value);
  if (shown.length < 2 && value === "all") return null;
  const opts: Array<{ v: MailFilter; label: string; n: number }> = [
    { v: "all", label: "All", n: messages.length },
    ...shown.map((s) => ({ v: s, label: MAIL_STATUS[s].word, n: counts.get(s) ?? 0 })),
  ];
  return (
    <ToggleGroup.Root
      type="single"
      aria-label="Show messages"
      value={value}
      onValueChange={(v) => v && onChange(v as MailFilter)}
      className="-mx-1 flex gap-0.5 overflow-x-auto px-1 [scrollbar-width:none]"
    >
      {opts.map((o) => (
        <ToggleGroup.Item
          key={o.v}
          value={o.v}
          className="group inline-flex h-7 shrink-0 items-center gap-1.5 rounded-[6px] px-2.5 text-[0.8125rem] text-ink-3 transition-colors duration-[var(--dur-state)] outline-hidden hover:bg-paper-hover hover:text-ink focus-visible:shadow-[inset_0_0_0_2px_var(--focus)] data-[state=on]:bg-paper-select data-[state=on]:font-[550] data-[state=on]:text-ink"
        >
          {o.v !== "all" && <MailDot status={o.v} />}
          {o.label}
          <span className="text-xs text-ink-4 tnum group-data-[state=on]:text-ink-2">{int(o.n)}</span>
        </ToggleGroup.Item>
      ))}
    </ToggleGroup.Root>
  );
}

type SendResult = { id: string; delivery: "inbox" | "relay" | "suppressed"; status: string; reason: string; recipients: string[]; suppressed?: string[] };

/**
 * Send a test message from the project, the way its apps send: through the
 * relay when the box has one, into the dev inbox when it hasn't. Opens the
 * message once it's in the log.
 */
export function SendTest({
  project,
  open,
  onOpenChange,
  relay,
  onSent,
}: {
  project: string;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  relay: boolean;
  onSent: (id: string) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">{open && <SendForm project={project} relay={relay} onDone={onOpenChange} onSent={onSent} />}</DialogContent>
    </Dialog>
  );
}

function SendForm({ project, relay, onDone, onSent }: { project: string; relay: boolean; onDone: (o: boolean) => void; onSent: (id: string) => void }) {
  const qc = useQueryClient();
  const [to, setTo] = useState("");
  const [subject, setSubject] = useState(`A test from ${project}`);
  const [text, setText] = useState(`This is a test message from ${project}. If you can read it, sending works.`);
  const ok = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(to.trim()) && subject.trim() !== "";
  const send = useMutation({
    mutationFn: () => request<SendResult>("POST", `/v1/projects/${encodeURIComponent(project)}/email/send`, { to: [to.trim()], subject: subject.trim(), text }),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ["messages", project] });
      toast({
        title: r.delivery === "relay" ? `Sending to ${to.trim()}.` : r.delivery === "suppressed" ? "Held back: that address is on the won't-write list." : "Caught in the dev inbox.",
        detail: r.delivery === "inbox" ? "Nothing left the box." : undefined,
      });
      onDone(false);
      onSent(r.id);
    },
  });
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (ok && !send.isPending) send.mutate();
      }}
    >
      <DialogHeader>
        <DialogTitle>Send a test</DialogTitle>
        <DialogDescription>
          {relay
            ? "It goes out through the relay, from the project's sender address, like mail from your apps."
            : "There's no relay yet, so it lands in the dev inbox here. Nothing leaves the box."}
        </DialogDescription>
      </DialogHeader>
      <DialogBody className="space-y-4">
        <label className="block">
          <span className="text-sm text-ink-2">To</span>
          <Input autoFocus type="email" value={to} onChange={(e) => setTo(e.target.value)} placeholder="you@example.com" className="mt-1" />
        </label>
        <label className="block">
          <span className="text-sm text-ink-2">Subject</span>
          <Input value={subject} onChange={(e) => setSubject(e.target.value)} className="mt-1" maxLength={200} />
        </label>
        <label className="block">
          <span className="text-sm text-ink-2">Message</span>
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={4}
            className="mt-1 block w-full resize-y rounded-md border border-rule bg-paper px-3 py-2 text-base text-ink transition-[border-color,box-shadow] outline-hidden placeholder:text-ink-4 hover:border-rule-2 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]"
          />
        </label>
        {send.isError && <ProblemNote error={send.error} />}
      </DialogBody>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={() => onDone(false)}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!ok || send.isPending}>
          <Send />
          {send.isPending ? "Sending…" : relay ? "Send for real" : "Send to the dev inbox"}
        </Button>
      </DialogFooter>
    </form>
  );
}
