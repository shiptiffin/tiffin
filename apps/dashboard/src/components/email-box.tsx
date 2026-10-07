import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight } from "lucide-react";
import { useState } from "react";
import { ApiError } from "@/api/client";
import { boxMail, boxMailQ, type BoxMessage, type BoxSender } from "@/api/modules";
import { CopyButton } from "@/components/copy";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { relative } from "@/lib/time";

// Settings › Email › Mail from the box: who the dashboard's own mail
// (invites, sign-in links, new sign-in notices) comes from, and the last
// few messages, which is where they wait when there is no mail service.

const ADDR = /^([^<>]*<)?[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+>?$/;

export function BoxMailSection({ admin }: { admin: boolean }) {
  const sender = useQuery(boxMailQ.sender);
  // An older box without box mail: say nothing.
  if (sender.isError && sender.error instanceof ApiError && [404, 405, 501].includes(sender.error.status)) return null;
  return (
    <section className="mt-8 border-t border-rule pt-5" aria-labelledby="box-mail">
      <h3 id="box-mail" className="text-[0.9375rem] font-[550] text-ink">
        Mail from the box
      </h3>
      <p className="mt-1 max-w-[40rem] text-[0.8125rem] text-ink-2">
        Invites, sign-in links people ask for, and a note when someone signs in from a new browser. No tracking, no images.
      </p>
      {sender.isError ? (
        <ProblemNote className="mt-3" error={sender.error} />
      ) : !sender.data ? (
        <Skeleton className="mt-4 h-14" />
      ) : (
        <Sender s={sender.data} admin={admin} />
      )}
      {admin && sender.data && <Recent relay={sender.data.mode === "relay"} />}
    </section>
  );
}

function Sender({ s, admin }: { s: BoxSender; admin: boolean }) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [from, setFrom] = useState("");
  const [reply, setReply] = useState("");
  const save = useMutation({
    mutationFn: () => boxMail.setSender({ from: from.trim(), replyTo: reply.trim() }),
    onSuccess: (d) => {
      qc.setQueryData(boxMailQ.sender.queryKey, d);
      setEditing(false);
    },
  });
  const badFrom = from.trim() !== "" && !ADDR.test(from.trim());
  const badReply = reply.trim() !== "" && !ADDR.test(reply.trim());

  if (editing)
    return (
      <form
        className="mt-4 grid max-w-[40rem] gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!badFrom && !badReply) save.mutate();
        }}
      >
        <div className="grid gap-1.5">
          <Label htmlFor="box-from">From</Label>
          <Input id="box-from" autoFocus value={from} onChange={(e) => setFrom(e.target.value)} placeholder={s.defaultFrom} spellCheck={false} aria-invalid={badFrom || undefined} />
          <p className={badFrom ? "text-sm text-danger" : "text-xs text-ink-3"}>
            {badFrom ? "That isn’t an email address." : `A name and an address on a domain your mail service accepts. Empty means ${s.defaultFrom}.`}
          </p>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="box-reply">
            Reply-To <span className="font-normal text-ink-3">(optional)</span>
          </Label>
          <Input id="box-reply" value={reply} onChange={(e) => setReply(e.target.value)} placeholder="help@example.com" spellCheck={false} aria-invalid={badReply || undefined} />
          {badReply && <p className="text-sm text-danger">That isn’t an email address.</p>}
        </div>
        {save.isError && <ProblemNote error={save.error} />}
        <div className="flex gap-2">
          <Button type="submit" variant="primary" disabled={badFrom || badReply || save.isPending}>
            {save.isPending ? "Saving…" : "Save"}
          </Button>
          <Button type="button" variant="ghost" onClick={() => setEditing(false)}>
            Cancel
          </Button>
        </div>
      </form>
    );

  return (
    <div className="mt-4 flex flex-wrap items-start justify-between gap-x-6 gap-y-2 border-y border-rule py-3">
      <dl className="grid min-w-0 grid-cols-[5.5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[0.875rem]">
        <dt className="text-ink-3">From</dt>
        <dd className="min-w-0">
          <span className="ident [overflow-wrap:anywhere] text-ink">{s.from}</span>
          {s.default && <span className="ml-2 text-[0.8125rem] text-ink-3">the default</span>}
        </dd>
        {s.replyTo && (
          <>
            <dt className="text-ink-3">Reply-To</dt>
            <dd className="ident min-w-0 [overflow-wrap:anywhere] text-ink">{s.replyTo}</dd>
          </>
        )}
        <dt className="text-ink-3">Goes</dt>
        <dd className="text-ink-2">{s.mode === "relay" ? `Out through ${s.relay ?? "the mail service"}` : "Into the box’s dev inbox, below: no mail service yet"}</dd>
      </dl>
      {admin && (
        <Button
          size="sm"
          onClick={() => {
            setFrom(s.default ? "" : s.from);
            setReply(s.replyTo ?? "");
            setEditing(true);
          }}
        >
          Change
        </Button>
      )}
    </div>
  );
}

const STATUS: Record<BoxMessage["status"], { word: string; tone: string }> = {
  captured: { word: "In the dev inbox", tone: "text-ink-3" },
  queued: { word: "Sending", tone: "text-ink-2" },
  sent: { word: "Sent", tone: "text-ink-2" },
  delivered: { word: "Delivered", tone: "text-ok" },
  bounced: { word: "Bounced", tone: "text-danger" },
  complained: { word: "Marked as spam", tone: "text-danger" },
  failed: { word: "Failed", tone: "text-danger" },
  suppressed: { word: "Not sent", tone: "text-warn-ink" },
};

function Recent({ relay }: { relay: boolean }) {
  const list = useQuery(boxMailQ.messages);
  const [open, setOpen] = useState<string | null>(null);
  const rows = (list.data ?? []).slice(0, 8);
  return (
    <div className="mt-6">
      <p className="label mb-2">{relay ? "Recently sent" : "Dev inbox"}</p>
      {list.isPending && <Skeleton className="h-16" />}
      {list.isError && <ProblemNote error={list.error} />}
      {list.data && rows.length === 0 && (
        <p className="text-[0.8125rem] text-ink-3">Nothing yet. Invite someone with an email address and their invite shows up here.</p>
      )}
      {rows.length > 0 && (
        <ul className="divide-y divide-rule border-y border-rule">
          {rows.map((m) => (
            <li key={m.id}>
              <button
                type="button"
                aria-expanded={open === m.id}
                onClick={() => setOpen(open === m.id ? null : m.id)}
                className="grid w-full grid-cols-[1rem_minmax(0,1fr)] items-baseline sm:grid-cols-[1rem_minmax(0,1fr)_auto] gap-x-2 py-2.5 text-left hover:bg-paper-hover"
              >
                <ChevronRight className={cn("size-3.5 self-center text-ink-3 transition-transform", open === m.id && "rotate-90")} aria-hidden />
                <span className="min-w-0">
                  <span className="block truncate text-[0.875rem] text-ink">{m.subject}</span>
                  <span className="block truncate text-[0.8125rem] text-ink-3">
                    <span className={cn("sm:hidden", STATUS[m.status]?.tone)}>{STATUS[m.status]?.word ?? m.status} · </span>
                    to {(m.to ?? []).join(", ")} · {relative(m.createdAt)}
                  </span>
                </span>
                <span className={cn("text-[0.8125rem] whitespace-nowrap max-sm:hidden", STATUS[m.status]?.tone ?? "text-ink-3")}>{STATUS[m.status]?.word ?? m.status}</span>
              </button>
              {open === m.id && <Body id={m.id} />}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Body({ id }: { id: string }) {
  const d = useQuery({ queryKey: ["box-mail-message", id], queryFn: () => boxMail.message(id) });
  if (d.isPending) return <Skeleton className="mb-3 ml-6 h-24" />;
  if (d.isError) return <ProblemNote className="mb-3 ml-6" error={d.error} />;
  const link = (d.data.links ?? [])[0];
  return (
    <div className="mb-3 ml-6 rounded-[8px] bg-paper-sunk px-4 py-3">
      {link && (
        <p className="mb-2 flex min-w-0 items-center gap-1 text-[0.8125rem]">
          <span className="shrink-0 text-ink-3">Link</span>
          <code className="ident min-w-0 truncate text-ink" title={link}>
            {link}
          </code>
          <CopyButton value={link} label="Copy the link" className="size-6 shrink-0" />
        </p>
      )}
      <pre className="max-h-64 overflow-auto font-sans text-[0.8125rem] leading-5 whitespace-pre-wrap text-ink-2 [overflow-wrap:anywhere]">{d.data.text}</pre>
    </div>
  );
}
