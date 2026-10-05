import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, ArrowUpRight, Download, Paperclip, Search, Send, Settings2, Trash2, X } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod, mq, type EmailDetail, type EmailSummary, type Suppression } from "@/api/modules";
import emptyInbox from "@/assets/illustrations/empty-inbox.webp";
import { Confirm } from "@/components/confirm";
import { Command, CopyButton } from "@/components/copy";
import { Rows, Section } from "@/components/data-parts";
import { useTitle } from "@/components/favicon";
import { Crumbs, Page, PageHeader, Skeleton, Untrusted, NotOnBox } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { Input, Label } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { bytes, count, int, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { clock, dayKey, full, relative } from "@/lib/time";

// ------------------------------------------------------------------ helpers

/** "Ada <ada@example.com>" → "Ada"; bare addresses stay. */
function displayName(addr: string) {
  const m = addr.match(/^\s*"?([^"<]+?)"?\s*<[^>]+>\s*$/);
  return m ? m[1] : addr;
}

const shortDay = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" });
function when(iso: string) {
  return dayKey(iso) === dayKey(new Date().toISOString()) ? clock(iso) : shortDay.format(new Date(iso));
}

/** Words for a status that isn't the calm default (captured in the dev inbox). */
const statusWords: Record<EmailSummary["status"], { text: string; tone: string } | null> = {
  captured: null,
  queued: { text: "Queued for the relay", tone: "text-warn-ink" },
  sent: { text: "Sent", tone: "text-ink-3" },
  failed: { text: "Not delivered", tone: "text-danger" },
  suppressed: { text: "Held back", tone: "text-warn-ink" },
};

function StatusLabel({ m }: { m: Pick<EmailSummary, "status"> }) {
  const w = statusWords[m.status];
  return w ? <span className={cn("text-xs font-[550]", w.tone)}>{w.text}</span> : null;
}

/** Live: new mail arrives over server-sent events while the page is open. */
function useMailStream(project: string, onMessage: (s: EmailSummary) => void) {
  const [live, setLive] = useState<boolean | null>(null);
  useEffect(() => {
    if (typeof EventSource === "undefined") return;
    const es = new EventSource(mod.streamUrl(project));
    es.onopen = () => setLive(true);
    es.onerror = () => setLive(false);
    es.addEventListener("message", (e) => {
      try {
        onMessage(JSON.parse((e as MessageEvent).data));
      } catch {
        /* a ping or a malformed event: ignore */
      }
    });
    return () => es.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [project]);
  return live;
}

/** The one line that says where mail goes, with the way to change it. */
function Truth({ project }: { project: string }) {
  const st = useQuery(mq.emailStatus);
  if (!st.data) return <span className="invisible">Loading where mail goes.</span>;
  if (st.data.mode === "relay")
    return (
      <>
        <span className="text-ink">Mail goes out for real</span> through <code className="ident text-ink">{st.data.relay?.host}</code>. Preview
        deployments still land here.
        {st.data.queued > 0 && <span className="text-warn-ink"> {count(st.data.queued, "message")} queued.</span>}
        {st.data.failedLastDay > 0 && <span className="text-danger"> {int(st.data.failedLastDay)} not delivered in the last day.</span>}
      </>
    );
  return (
    <>
      <span className="text-ink">Nothing leaves this box.</span> Every message {project} sends is caught here until you{" "}
      <Link
        to="/projects/$project/email/settings"
        params={{ project }}
        className="text-brass-ink underline decoration-brass/40 underline-offset-[3px] hover:decoration-brass"
      >
        add a relay
      </Link>
      .
    </>
  );
}

// ------------------------------------------------------------------ inbox

export function InboxPage({ project, q = "", m }: { project: string; q?: string; m?: string }) {
  useTitle(`${project} · Email`);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [query, setQuery] = useState(q);
  const list = useQuery(mq.messages(project, q));
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  const live = useMailStream(project, (s) => {
    setFresh((f) => new Set(f).add(s.id));
    qc.invalidateQueries({ queryKey: ["messages", project] });
  });
  const go = (o: { q?: string; m?: string }) =>
    navigate({ to: "/projects/$project/email", params: { project }, search: { q: o.q || undefined, m: o.m }, replace: !!o.q !== !!q });

  useEffect(() => {
    const t = setTimeout(() => query !== q && go({ q: query, m }), 250);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query]);

  const msgs = list.data ?? [];
  // j / k move through the list, like a mail client.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || (e.target as HTMLElement)?.closest?.("input,textarea,select,[contenteditable]")) return;
      if (e.key !== "j" && e.key !== "k") return;
      const i = msgs.findIndex((x) => x.id === m);
      const next = msgs[e.key === "j" ? Math.min(msgs.length - 1, i + 1) : Math.max(0, i - 1)];
      if (next && next.id !== m) go({ q, m: next.id });
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  });

  if (list.isError && notOnBox(list.error)) return <NotOnBox what="The dev inbox and relay" />;
  const empty = list.isSuccess && msgs.length === 0 && !q;

  return (
    <Page full>
      <PageHeader
        eyebrow={<ProjectCrumb project={project} />}
        title="Email"
        lede={<Truth project={project} />}
        actions={
          <Button asChild variant="secondary">
            <Link to="/projects/$project/email/settings" params={{ project }}>
              <Settings2 />
              Settings
            </Link>
          </Button>
        }
      />

      {empty ? (
        <div className="mt-10 flex flex-col items-center border-y border-rule px-6 py-14 text-center">
          <span className="art-plate block size-40">
            <img src={emptyInbox} alt="" width={160} height={160} className="block size-full select-none" draggable={false} />
          </span>
          <p className="mt-4 text-md text-ink">No mail yet.</p>
          <p className="mt-1 max-w-[30rem] text-base text-ink-3">
            When {project} sends a sign-up link or a receipt, it shows up here the moment it's sent, rendered the way the recipient would see it.
          </p>
        </div>
      ) : (
        <div className="mt-8 grid border-y border-rule lg:h-[calc(100dvh-15.5rem)] lg:min-h-[32rem] lg:grid-cols-[minmax(19rem,24rem)_minmax(0,1fr)]">
          <section className={cn("flex min-h-0 flex-col lg:border-r lg:border-rule", m && "hidden lg:flex")} aria-label="Messages">
            <label className="flex h-11 shrink-0 items-center gap-2.5 border-b border-rule lg:px-3">
              <Search className="size-4 shrink-0 text-ink-3" aria-hidden />
              <input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Search subject, people, text"
                aria-label="Search mail"
                className="h-8 min-w-0 flex-1 bg-transparent text-base text-ink outline-none placeholder:text-ink-4"
              />
              {query && (
                <button
                  onClick={() => setQuery("")}
                  aria-label="Clear search"
                  className="grid size-6 place-items-center rounded-[5px] text-ink-3 hover:bg-paper-sunk"
                >
                  <X className="size-3.5" />
                </button>
              )}
            </label>
            <ul className="min-h-0 flex-1 divide-y divide-rule overflow-y-auto">
              {list.isPending &&
                [0, 1, 2, 3].map((i) => (
                  <li key={i} className="space-y-2 py-3">
                    <Skeleton className="h-4 w-1/2" />
                    <Skeleton className="h-3 w-5/6 opacity-60" />
                  </li>
                ))}
              {msgs.map((s) => {
                const active = s.id === m;
                return (
                  <li key={s.id}>
                    <button
                      onClick={() => go({ q, m: s.id })}
                      className={cn(
                        "relative block w-full py-3 text-left transition-colors duration-[var(--dur-state)] hover:bg-paper-sunk lg:pr-4 lg:pl-3",
                        active && "bg-paper-sunk",
                        fresh.has(s.id) && "animate-rise",
                      )}
                      aria-current={active}
                    >
                      {active && <span aria-hidden className="absolute inset-y-2 left-0 w-[2px] rounded-full bg-brass max-lg:hidden" />}
                      <span className="flex items-baseline gap-3">
                        <span className="min-w-0 flex-1 truncate text-base font-[550] text-ink">{s.subject || "(no subject)"}</span>
                        <time dateTime={s.createdAt} className="shrink-0 text-xs text-ink-3 tnum" title={full(s.createdAt)}>
                          {when(s.createdAt)}
                        </time>
                      </span>
                      <span className="mt-0.5 flex items-center gap-1.5 text-sm text-ink-2">
                        <span className="min-w-0 truncate">to {(s.to ?? []).map(displayName).join(", ")}</span>
                        {s.attachments > 0 && <Paperclip className="size-3.5 shrink-0 text-ink-3" aria-label={count(s.attachments, "attachment")} />}
                        <span className="ml-auto shrink-0">
                          <StatusLabel m={s} />
                        </span>
                      </span>
                      <span className="mt-0.5 line-clamp-1 text-sm text-ink-3">{s.snippet}</span>
                    </button>
                  </li>
                );
              })}
            </ul>
            {list.isSuccess && msgs.length === 0 && (
              <div className="px-6 py-12 text-center">
                <p className="text-base text-ink">Nothing matches “{q}”.</p>
                <p className="mt-1 text-sm text-ink-3">Search looks at subjects, addresses and the text of each message.</p>
              </div>
            )}
            {msgs.length > 0 && (
              <p className="shrink-0 border-t border-rule py-2 text-xs text-ink-3 lg:px-3" title="The newest 1,000 messages are kept">
                {count(msgs.length, "message")}.{" "}
                {live === false ? <span className="text-warn-ink">Not connected: refresh to see new mail.</span> : "New mail shows up as it arrives."}
              </p>
            )}
          </section>

          <section className={cn("min-h-0 min-w-0", !m && "hidden lg:block")} aria-label="Message">
            {m ? (
              <Message key={m} project={project} id={m} onBack={() => go({ q })} onDeleted={() => go({ q })} />
            ) : (
              <div className="grid h-full place-items-center p-10 text-center">
                <div>
                  <p className="text-md text-ink-2">Pick a message to read it.</p>
                  <p className="mt-1 text-sm text-ink-3">
                    Follow its links, check how it renders, read its headers. <kbd className="kbd">j</kbd> <kbd className="kbd">k</kbd> move through
                    the list.
                  </p>
                </div>
              </div>
            )}
          </section>
        </div>
      )}
    </Page>
  );
}

function ProjectCrumb({ project }: { project: string }) {
  return <Crumbs items={[{ label: project, to: "/projects/$project", params: { project }, mono: true }]} />;
}

type Tab = "preview" | "text" | "links" | "headers";

/** What happened to a message, in order, from the fields the box keeps. */
function timeline(msg: EmailDetail): Array<{ at?: string; text: ReactNode; tone?: string }> {
  const out: Array<{ at?: string; text: ReactNode; tone?: string }> = [
    {
      at: msg.createdAt,
      text: (
        <>
          Received through {msg.source === "smtp" ? "SMTP" : "the API"}, {bytes(msg.size)}
        </>
      ),
    },
  ];
  if (msg.delivery === "inbox") out.push({ at: msg.createdAt, text: "Caught in the dev inbox. Not sent." });
  if (msg.status === "suppressed" || (msg.suppressed ?? []).length > 0)
    out.push({
      text: (
        <>
          Held back for {(msg.suppressed ?? []).join(", ") || "a suppressed address"}
          {msg.reason ? `: ${msg.reason}` : ", on the won't-write list"}
        </>
      ),
      tone: "text-warn-ink",
    });
  if (msg.status === "queued")
    out.push({
      at: msg.nextAttempt,
      text: (
        <>
          Waiting for the relay{msg.attempts ? `, tried ${words(msg.attempts)} ${msg.attempts === 1 ? "time" : "times"}` : ""}
          {msg.nextAttempt ? `; next try ${relative(msg.nextAttempt)}` : ""}
        </>
      ),
      tone: "text-warn-ink",
    });
  if (msg.status === "sent") out.push({ at: msg.sentAt, text: "Handed to the relay" });
  if (msg.status === "failed")
    out.push({
      text: <>Not delivered{msg.attempts ? ` after ${words(msg.attempts)} ${msg.attempts === 1 ? "try" : "tries"}` : ""}</>,
      tone: "text-danger",
    });
  return out;
}

function Message({ project, id, onBack, onDeleted }: { project: string; id: string; onBack: () => void; onDeleted: () => void }) {
  const qc = useQueryClient();
  const d = useQuery(mq.message(project, id));
  const [tab, setTab] = useState<Tab | null>(null);
  const [deleting, setDeleting] = useState(false);
  const { can } = useMe();

  if (d.isPending)
    return (
      <div className="space-y-3 py-6 lg:px-6">
        <Skeleton className="h-7 w-2/3" />
        <Skeleton className="h-4 w-1/3 opacity-60" />
        <Skeleton className="mt-6 h-64" />
      </div>
    );
  if (d.isError) return <ProblemNote className="my-6 lg:mx-6" error={d.error} />;
  const msg = d.data;
  const t: Tab = tab ?? (msg.html ? "preview" : "text");
  const links = msg.links ?? [];
  const events = timeline(msg);

  return (
    <article className="flex h-full min-h-0 flex-col">
      <header className="border-b border-rule pt-4 pb-4 lg:px-6">
        <div className="flex items-start gap-2">
          <button
            onClick={onBack}
            className="-ml-1.5 grid size-8 shrink-0 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-sunk lg:hidden"
            aria-label="Back to the list"
          >
            <ArrowLeft className="size-4" />
          </button>
          <h2 className="min-w-0 flex-1 pt-0.5 text-xl font-[550] tracking-[-0.01em] text-ink">{msg.subject || "(no subject)"}</h2>
          <span className="flex shrink-0 items-center gap-0.5">
            <a
              href={msg.rawUrl}
              download
              className="grid size-8 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-sunk hover:text-ink"
              title="Download the raw message (.eml)"
              aria-label="Download the raw message"
            >
              <Download className="size-4" />
            </a>
            {can("apply:reversible") && msg.delivery === "inbox" && (
              <button
                onClick={() => setDeleting(true)}
                className="grid size-8 place-items-center rounded-[6px] text-ink-3 hover:bg-danger-wash hover:text-danger"
                aria-label="Delete this message"
                title="Delete this message"
              >
                <Trash2 className="size-4" />
              </button>
            )}
          </span>
        </div>
        <div className="mt-3 grid gap-x-8 gap-y-4 xl:grid-cols-[minmax(0,1fr)_minmax(14rem,18rem)]">
          <dl className="grid min-w-0 grid-cols-[2.75rem_minmax(0,1fr)] content-start gap-x-3 gap-y-1 text-sm">
            <dt className="text-ink-3">From</dt>
            <dd className="truncate text-ink-2">{msg.from}</dd>
            <dt className="text-ink-3">To</dt>
            <dd className="truncate text-ink-2">{(msg.to ?? []).join(", ")}</dd>
            <dt className="text-ink-3">Date</dt>
            <dd className="text-ink-2" title={full(msg.createdAt)}>
              {full(msg.createdAt)}
            </dd>
          </dl>
          <ol className="relative min-w-0 text-sm" aria-label="What happened to it">
            {events.map((e, i) => (
              <li key={i} className="relative grid grid-cols-[2.75rem_0.75rem_minmax(0,1fr)] gap-x-2 pb-1.5 last:pb-0">
                <time className="text-ink-3 tnum" dateTime={e.at}>
                  {e.at ? clock(e.at) : ""}
                </time>
                <span aria-hidden className="relative flex justify-center pt-[7px]">
                  <i className={cn("block size-[5px] rounded-full", e.tone ? "bg-current " + e.tone : "bg-ink-3")} />
                  {i < events.length - 1 && <i className="absolute top-[14px] -bottom-[6px] w-px bg-rule-2" />}
                </span>
                <span className={cn("min-w-0", e.tone ?? "text-ink-2")}>{e.text}</span>
              </li>
            ))}
          </ol>
        </div>
        {msg.lastError && (
          <p className="mt-3 text-sm text-ink-2">
            <span className="text-danger">Last attempt failed:</span> {msg.lastError}
          </p>
        )}
        {(msg.attachmentList ?? []).length > 0 && (
          <ul className="mt-3 flex flex-wrap gap-2">
            {(msg.attachmentList ?? []).map((a) => (
              <li key={a.index}>
                <a
                  href={mod.attachmentUrl(project, id, a.index)}
                  download={a.filename}
                  className="inline-flex h-7 items-center gap-2 rounded-[6px] border border-rule-2 bg-paper-raised px-2.5 text-sm text-ink-2 transition-colors hover:border-rule-3 hover:text-ink"
                >
                  <Paperclip className="size-3.5 text-ink-3" />
                  <span className="font-mono text-xs">{a.filename}</span>
                  <span className="text-xs text-ink-3 tnum">{bytes(a.size)}</span>
                </a>
              </li>
            ))}
          </ul>
        )}
      </header>
      <nav className="flex gap-1 border-b border-rule lg:px-4" aria-label="View">
        {(
          [
            ["preview", "Preview", !!msg.html],
            ["text", "Text", true],
            ["links", `Links${links.length ? ` ${links.length}` : ""}`, true],
            ["headers", "Headers", true],
          ] as const
        )
          .filter(([, , ok]) => ok)
          .map(([k, label]) => (
            <button
              key={k}
              onClick={() => setTab(k)}
              aria-pressed={t === k}
              className={cn(
                "relative h-10 px-2.5 text-sm text-ink-3 transition-colors hover:text-ink first:max-lg:pl-0",
                t === k &&
                  "font-[550] text-ink after:absolute after:inset-x-2 after:-bottom-px after:h-0.5 after:rounded-full after:bg-ink first:max-lg:after:left-0",
              )}
            >
              {label}
            </button>
          ))}
      </nav>
      <div className="min-h-0 flex-1 overflow-auto py-4 lg:bg-paper-sunk/50 lg:p-5">
        {t === "preview" && <HtmlView html={msg.html} />}
        {t === "text" && (
          <Untrusted label="Message text, as the sender wrote it">
            <pre className="px-4 py-3 font-sans text-base leading-6 whitespace-pre-wrap text-ink-2">{msg.text || "(no text part)"}</pre>
          </Untrusted>
        )}
        {t === "links" && <LinksView links={links} />}
        {t === "headers" && <HeadersView msg={msg} />}
      </div>
      <Confirm
        open={deleting}
        onClose={() => setDeleting(false)}
        title="Delete this message?"
        body="It's removed from the dev inbox for good."
        action="Delete"
        run={() => mod.deleteMessage(project, id)}
        done={() => {
          qc.invalidateQueries({ queryKey: ["messages", project] });
          onDeleted();
        }}
      />
    </article>
  );
}

/** The HTML part in a sandboxed frame: no scripts, no forms, no remote images. */
function HtmlView({ html }: { html: string }) {
  const doc = useMemo(
    () =>
      `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:"><base target="_blank"><style>html,body{margin:0;background:#fff;color:#222;font:15px/1.5 -apple-system,system-ui,sans-serif}body{padding:8px}img{max-width:100%;height:auto}</style></head><body>${html}</body></html>`,
    [html],
  );
  const remote = /<img[^>]+src=["']?https?:/i.test(html);
  return (
    <div className="mx-auto max-w-[42rem]">
      <div className="overflow-hidden rounded-[10px] border border-rule-2 bg-white shadow-raised">
        <iframe
          title="Message preview"
          sandbox="allow-popups allow-popups-to-escape-sandbox"
          srcDoc={doc}
          className="block h-[min(60vh,40rem)] w-full bg-white"
        />
      </div>
      <p className="mt-2.5 text-xs text-ink-3">
        Shown in a sandbox: scripts and forms are off{remote ? ", and remote images (often trackers) are blocked" : ""}. Links open in a new tab.
      </p>
    </div>
  );
}

function LinksView({ links }: { links: string[] }) {
  if (links.length === 0) return <p className="py-6 text-center text-base text-ink-3">No links in this message.</p>;
  return (
    <div className="mx-auto max-w-[42rem]">
      <p className="mb-3 text-sm text-ink-3">Every http(s) link in the message. Handy for sign-in and verification flows.</p>
      <Untrusted label="Links from the message, shown as plain text">
        <ul className="divide-y divide-rule">
          {links.map((l) => (
            <li key={l} className="flex items-center gap-2 py-1.5 pr-1.5 pl-3">
              <code className="min-w-0 flex-1 truncate font-mono text-[0.78125rem] text-ink-2" title={l}>
                {l}
              </code>
              <CopyButton value={l} label="Copy link" />
              <a
                href={l}
                target="_blank"
                rel="noopener noreferrer"
                className="grid size-7 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-press hover:text-ink"
                aria-label="Open link in a new tab"
              >
                <ArrowUpRight className="size-3.5" />
              </a>
            </li>
          ))}
        </ul>
      </Untrusted>
    </div>
  );
}

function HeadersView({ msg }: { msg: EmailDetail }) {
  return (
    <Untrusted label="Headers, as received">
      <dl className="divide-y divide-rule font-mono text-[0.75rem]">
        <Row k="Envelope from" v={msg.envelope.from} />
        <Row k="Envelope to" v={(msg.envelope.to ?? []).join(", ")} />
        {(msg.headers ?? []).map((h, i) => (
          <Row key={i} k={h.name} v={h.value} />
        ))}
      </dl>
    </Untrusted>
  );
}

function Row({ k, v }: { k: string; v: ReactNode }) {
  return (
    <div className="grid grid-cols-[minmax(7rem,11rem)_1fr] gap-3 px-3.5 py-1.5">
      <dt className="text-ink-3">{k}</dt>
      <dd className="break-all text-ink-2">{v}</dd>
    </div>
  );
}

// ------------------------------------------------------------------ settings

export function EmailSettingsPage({ project }: { project: string }) {
  useTitle(`${project} · Email settings`);
  const st = useQuery(mq.emailStatus);
  const { admin } = useMe();
  if (st.isError && notOnBox(st.error)) return <NotOnBox what="The dev inbox and relay" />;
  return (
    <Page wide>
      <PageHeader
        eyebrow={
          <Crumbs
            items={[
              { label: project, to: "/projects/$project", params: { project }, mono: true },
              { label: "Email", to: "/projects/$project/email", params: { project } },
            ]}
          />
        }
        title="Email settings"
        lede="How mail leaves the box, the credentials your apps use, and the addresses this project won't write to."
      />
      <div className="mt-10 grid gap-x-14 gap-y-12 lg:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)]">
        <RelaySection admin={admin} />
        <div className="flex min-w-0 flex-col gap-12">
          <SmtpSection project={project} />
          <Suppressions project={project} />
        </div>
      </div>
    </Page>
  );
}

function RelaySection({ admin }: { admin: boolean }) {
  const qc = useQueryClient();
  const st = useQuery(mq.emailStatus);
  const relay = st.data?.relay;
  const [host, setHost] = useState("");
  const [port, setPort] = useState("587");
  const [tls, setTls] = useState<"starttls" | "tls" | "none">("starttls");
  const [username, setUser] = useState("");
  const [password, setPass] = useState("");
  const [to, setTo] = useState("");
  const [removing, setRemoving] = useState(false);
  const save = useMutation({
    mutationFn: () => mod.setRelay({ host: host.trim(), port: Number(port), tls, username: username || undefined, password: password || undefined }),
    onSuccess: () => {
      setPass("");
      qc.invalidateQueries({ queryKey: ["email-status"] });
    },
  });
  const test = useMutation({ mutationFn: () => mod.testRelay(to.trim()) });
  const isRelay = st.data?.mode === "relay" && !!relay;

  return (
    <Section id="relay" label="Sending for real" aside={isRelay ? "box-wide, for every project" : undefined}>
      <div className="border-t border-rule pt-4">
        {isRelay ? (
          <>
            <p className="text-base text-ink">
              Relaying through <code className="ident">{relay.host}</code>:{relay.port} (
              {relay.tls === "none" ? "no encryption" : relay.tls.toUpperCase()}){relay.username ? ` as ${relay.username}` : ""}.
            </p>
            <p className="mt-1 text-sm text-ink-3">
              Password {relay.passwordSet ? "stored encrypted, never shown" : "not set"}; changed {relative(relay.updatedAt)}.
            </p>
          </>
        ) : (
          <p className="text-base text-ink-2">
            No relay yet, so every project's mail stays in its dev inbox. Point the box at any SMTP relay (Resend, Postmark, SES or your provider) and
            production mail goes out for real; preview deployments keep using the dev inbox.
          </p>
        )}
        {admin ? (
          <form
            className="mt-6 grid gap-x-3 gap-y-4 sm:grid-cols-[minmax(0,1fr)_7.5rem]"
            onSubmit={(e) => {
              e.preventDefault();
              if (host.trim()) save.mutate();
            }}
          >
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="r-host">SMTP host</Label>
              <Input
                id="r-host"
                value={host}
                onChange={(e) => setHost(e.target.value)}
                placeholder={relay?.host ?? "smtp.resend.com"}
                autoComplete="off"
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="r-port">Port</Label>
              <Input id="r-port" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ""))} inputMode="numeric" className="tnum" />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="r-user">Username</Label>
              <Input id="r-user" value={username} onChange={(e) => setUser(e.target.value)} placeholder="resend" autoComplete="off" />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="r-tls">Security</Label>
              <Select
                id="r-tls"
                value={tls}
                onValueChange={(v) => setTls(v as typeof tls)}
                options={[
                  { value: "starttls", label: "STARTTLS" },
                  { value: "tls", label: "TLS" },
                  { value: "none", label: "None" },
                ]}
              />
            </div>
            <div className="flex flex-col gap-1.5 sm:col-span-2">
              <Label htmlFor="r-pass">Password or API key</Label>
              <Input
                id="r-pass"
                type="password"
                value={password}
                onChange={(e) => setPass(e.target.value)}
                autoComplete="new-password"
                placeholder={relay?.passwordSet ? "Leave empty to keep the stored one" : ""}
              />
            </div>
            {save.isError && <ProblemNote className="sm:col-span-2" error={save.error} />}
            <div className="flex flex-col-reverse gap-3 sm:col-span-2 sm:flex-row sm:items-center sm:justify-between">
              <p className="text-sm text-ink-3">Stored encrypted on the box. Changing it applies at once.</p>
              <Button type="submit" variant="primary" disabled={!host.trim() || save.isPending} className="self-start sm:self-auto">
                <Send />
                {save.isPending
                  ? "Saving…"
                  : relay
                    ? `Relay through ${host.trim() || "the new host"}`
                    : `Send through ${host.trim() || "this relay"}`}
              </Button>
            </div>
          </form>
        ) : (
          <p className="mt-4 text-sm text-ink-3">Only the box owner and admins can set the relay.</p>
        )}
        {admin && relay && (
          <div className="mt-8 border-t border-rule pt-5">
            <p className="label mb-2.5">Send a test</p>
            <form
              className="flex flex-col gap-2 sm:flex-row"
              onSubmit={(e) => {
                e.preventDefault();
                if (to.trim()) test.mutate();
              }}
            >
              <Input
                value={to}
                onChange={(e) => setTo(e.target.value)}
                placeholder="you@example.com"
                type="email"
                aria-label="Send a test email to"
              />
              <Button type="submit" disabled={!to.trim() || test.isPending}>
                <Send />
                {test.isPending ? "Sending…" : "Send a test"}
              </Button>
            </form>
            {test.data && <p className={cn("mt-2 text-sm", test.data.ok ? "text-ink-2" : "text-danger")}>{test.data.detail}</p>}
            {test.isError && <ProblemNote className="mt-2" error={test.error} />}
            <Button variant="danger-quiet" size="sm" className="mt-5 -ml-2.5" onClick={() => setRemoving(true)}>
              Remove the relay
            </Button>
          </div>
        )}
      </div>
      <Confirm
        open={removing}
        onClose={() => setRemoving(false)}
        title="Remove the relay?"
        body="The box goes back to catching every message in the dev inbox. Nothing will be sent for real until you connect one again."
        action="Remove relay"
        tone="normal"
        run={() => mod.removeRelay()}
        done={() => qc.invalidateQueries({ queryKey: ["email-status"] })}
      />
    </Section>
  );
}

function SmtpSection({ project }: { project: string }) {
  const [env, setEnv] = useState<Record<string, string> | null>(null);
  const [err, setErr] = useState<unknown>(null);
  return (
    <Section id="smtp" label="SMTP for your apps">
      <div className="border-t border-rule pt-4">
        <p className="text-base text-ink-2">
          Apps in <code className="ident text-ink">{project}</code> already get <code className="ident text-ink">SMTP_URL</code> and friends. Any SMTP
          library works, or <code className="ident text-ink">send()</code> from tiffin-sdk/email.
        </p>
        {!env && (
          <Button
            size="sm"
            className="mt-4"
            onClick={async () => {
              setErr(null);
              try {
                setEnv(await mod.smtp(project));
              } catch (e) {
                setErr(e);
              }
            }}
          >
            Show the SMTP env
          </Button>
        )}
        {err ? <ProblemNote className="mt-3" error={err} /> : null}
        {env && (
          <Command
            wrap
            className="mt-4"
            cmd={Object.entries(env)
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([k, v]) => `${k}=${v}`)
              .join(" ")}
          />
        )}
      </div>
    </Section>
  );
}

function Suppressions({ project }: { project: string }) {
  const qc = useQueryClient();
  const list = useQuery(mq.suppressions(project));
  const [address, setAddress] = useState("");
  const add = useMutation({
    mutationFn: () => mod.suppress(project, address.trim(), "manual"),
    onSuccess: () => {
      setAddress("");
      qc.invalidateQueries({ queryKey: ["suppressions", project] });
    },
  });
  const [removing, setRemoving] = useState<Suppression | null>(null);
  const reason = { bounce: "Bounced", complaint: "Marked as spam", unsubscribe: "Unsubscribed", manual: "Added by hand" };
  const items = list.data ?? [];
  return (
    <Section id="supp" label="Won't write to" aside={items.length ? count(items.length, "address", "addresses") : undefined}>
      <p className="mb-3 text-sm text-ink-3">Hard bounces land here on their own. Add unsubscribes and complaints yourself.</p>
      <Rows>
        {items.map((s) => (
          <li key={s.address} className="flex items-center gap-3 py-2.5">
            <span className="min-w-0 flex-1">
              <span className="block truncate font-mono text-[0.8125rem] text-ink">{s.address}</span>
              <span className="block truncate text-xs text-ink-3">
                {reason[s.reason]} {relative(s.createdAt)}
                {s.detail ? ` · ${s.detail}` : ""}
              </span>
            </span>
            <Button variant="ghost" size="sm" onClick={() => setRemoving(s)}>
              Allow again
            </Button>
          </li>
        ))}
        <li className="py-3">
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (address.includes("@")) add.mutate();
            }}
          >
            <Input
              value={address}
              onChange={(e) => setAddress(e.target.value)}
              placeholder="someone@example.com"
              type="email"
              aria-label="Address to suppress"
              className="h-8"
            />
            <Button size="md" type="submit" disabled={!address.includes("@") || add.isPending}>
              Suppress
            </Button>
          </form>
          {add.isError && <ProblemNote className="mt-2" error={add.error} />}
        </li>
      </Rows>
      <Confirm
        open={!!removing}
        onClose={() => setRemoving(null)}
        title={`Write to ${removing?.address ?? "them"} again?`}
        body={
          removing?.reason === "bounce"
            ? "This address bounced before. If it still doesn't exist, it will bounce again."
            : "The project can send to this address again."
        }
        action="Allow again"
        tone="normal"
        run={() => mod.unsuppress(project, removing!.address)}
        done={() => qc.invalidateQueries({ queryKey: ["suppressions", project] })}
      />
    </Section>
  );
}
