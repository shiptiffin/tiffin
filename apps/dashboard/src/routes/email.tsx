import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, ArrowUpRight, Download, Inbox, Mail, Paperclip, Search, Send, Settings2, ShieldBan, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod, mq, type EmailDetail, type EmailSummary, type Suppression } from "@/api/modules";
import { Command, CopyButton } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { Empty, Page, PageHeader, Skeleton, Untrusted, NotOnBox } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { Input, Label } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { bytes } from "@/lib/format";
import { useMe } from "@/lib/me";
import { clock, dayKey, full, relative } from "@/lib/time";
import { Confirm } from "@/components/confirm";

// ------------------------------------------------------------------ helpers

/** "Ada <ada@example.com>" → "Ada"; bare addresses stay. */
function displayName(addr: string) {
  const m = addr.match(/^\s*"?([^"<]+?)"?\s*<[^>]+>\s*$/);
  return m ? m[1] : addr;
}

function when(iso: string) {
  return dayKey(iso) === dayKey(new Date().toISOString())
    ? clock(iso)
    : new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(new Date(iso));
}

const statusTone: Record<EmailSummary["status"], string> = {
  captured: "text-ink-3",
  queued: "text-brass-ink",
  sent: "text-rev",
  failed: "text-irr",
  suppressed: "text-out",
};

function StatusLabel({ m }: { m: Pick<EmailSummary, "status" | "delivery"> }) {
  const label = { captured: "In the dev inbox", queued: "Queued for the relay", sent: "Sent", failed: "Failed", suppressed: "Held: suppressed" }[
    m.status
  ];
  return <span className={cn("text-xs", statusTone[m.status])}>{label}</span>;
}

/** Live: new mail arrives over server-sent events while the page is open. */
function useMailStream(project: string, onMessage: (s: EmailSummary) => void) {
  const [live, setLive] = useState(false);
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

function ModeBanner({ project }: { project: string }) {
  const st = useQuery(mq.emailStatus);
  if (!st.data) return null;
  const relay = st.data.mode === "relay";
  return (
    <div
      className={cn(
        "mt-8 flex flex-col gap-3 rounded-xl border px-5 py-4 sm:flex-row sm:items-center",
        relay ? "border-out/40 bg-out-wash" : "border-rule bg-raised/70",
      )}
    >
      <span className={cn("grid size-9 shrink-0 place-items-center rounded-lg", relay ? "bg-out/15 text-out" : "bg-hover text-ink-2")}>
        {relay ? <Send className="size-4" /> : <Inbox className="size-4" />}
      </span>
      <p className="flex-1 text-base text-ink">
        {relay ? (
          <>
            <span className="font-medium">Mail goes out for real</span>{" "}
            <span className="text-ink-2">
              through <code className="font-mono text-sm">{st.data.relay?.host}</code>. Preview deployments still land in the dev inbox.
              {st.data.queued > 0 && ` ${st.data.queued} queued.`}
              {st.data.failedLastDay > 0 && ` ${st.data.failedLastDay} failed in the last day.`}
            </span>
          </>
        ) : (
          <>
            <span className="font-medium">Nothing leaves this box.</span>{" "}
            <span className="text-ink-2">Every message your apps send is caught here, in the dev inbox, until the box owner connects a relay.</span>
          </>
        )}
      </p>
      <Button asChild size="sm" variant="secondary">
        <Link to="/projects/$project/email/settings" params={{ project }}>
          <Settings2 />
          {relay ? "Relay settings" : "Connect a relay"}
        </Link>
      </Button>
    </div>
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

  if (list.isError && notOnBox(list.error)) return <NotOnBox what="The dev inbox and relay" />;
  const msgs = list.data ?? [];

  return (
    <Page full>
      <PageHeader
        eyebrow={<Crumbs project={project} />}
        title="Email"
        actions={
          <Button asChild variant="secondary">
            <Link to="/projects/$project/email/settings" params={{ project }}>
              <Settings2 />
              Settings
            </Link>
          </Button>
        }
      />
      <ModeBanner project={project} />

      <div className="mt-6 grid overflow-hidden rounded-xl border border-rule bg-raised/60 lg:h-[calc(100dvh-18rem)] lg:min-h-[34rem] lg:grid-cols-[minmax(19rem,25rem)_1fr]">
        <section className={cn("flex min-h-0 flex-col border-rule lg:border-r", m && "hidden lg:flex")} aria-label="Messages">
          <div className="flex items-center gap-2 border-b border-rule px-3 py-2.5">
            <Search className="size-4 shrink-0 text-ink-4" />
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search subject, people, text"
              aria-label="Search mail"
              className="h-7 min-w-0 flex-1 bg-transparent text-base text-ink outline-none placeholder:text-ink-4"
            />
            <span
              className={cn("flex shrink-0 items-center gap-1.5 text-xs", live ? "text-rev" : "text-ink-4")}
              title={live ? "New mail shows up as it arrives" : "Not connected; refresh to retry"}
            >
              <span className={cn("size-1.5 rounded-full", live ? "animate-pulse bg-rev" : "bg-ink-4")} />
              {live ? "Live" : "Offline"}
            </span>
          </div>
          <ul className="min-h-0 flex-1 divide-y divide-rule/70 overflow-y-auto">
            {list.isPending &&
              [0, 1, 2, 3].map((i) => (
                <li key={i} className="space-y-2 px-4 py-3">
                  <Skeleton className="h-4 w-1/2" />
                  <Skeleton className="h-3 w-5/6 opacity-60" />
                </li>
              ))}
            {msgs.map((s) => (
              <li key={s.id}>
                <button
                  onClick={() => go({ q, m: s.id })}
                  className={cn(
                    "relative block w-full px-4 py-3 text-left transition-colors hover:bg-hover/60",
                    s.id === m && "bg-hover",
                    fresh.has(s.id) && "animate-rise",
                  )}
                  aria-current={s.id === m}
                >
                  {s.id === m && <span aria-hidden className="absolute inset-y-2 left-0 w-[3px] rounded-r-full bg-brass" />}
                  <span className="flex items-baseline gap-2">
                    <span className="min-w-0 flex-1 truncate text-sm text-ink-2">to {(s.to ?? []).map(displayName).join(", ")}</span>
                    <time dateTime={s.createdAt} className="shrink-0 font-mono text-xs text-ink-3 tnum" title={full(s.createdAt)}>
                      {when(s.createdAt)}
                    </time>
                  </span>
                  <span className="mt-0.5 flex items-center gap-1.5">
                    <span className="min-w-0 flex-1 truncate text-base font-medium text-ink">{s.subject || "(no subject)"}</span>
                    {s.attachments > 0 && <Paperclip className="size-3.5 shrink-0 text-ink-3" aria-label={`${s.attachments} attachments`} />}
                  </span>
                  <span className="mt-0.5 line-clamp-2 text-sm text-ink-3">{s.snippet}</span>
                  {s.status !== "captured" && (
                    <span className="mt-1 block">
                      <StatusLabel m={s} />
                    </span>
                  )}
                </button>
              </li>
            ))}
          </ul>
          {list.isSuccess && msgs.length === 0 && (
            <div className="px-6 py-12 text-center">
              <Mail className="mx-auto size-6 text-ink-4" />
              <p className="mt-3 text-base text-ink">{q ? "No mail matches that." : "No mail yet."}</p>
              {!q && <p className="mt-1 text-sm text-ink-3">When your app sends a sign-up or reset email, it shows up here instantly.</p>}
            </div>
          )}
          {msgs.length > 0 && <p className="border-t border-rule px-4 py-2 text-xs text-ink-4">{msgs.length} messages · the newest 1,000 are kept</p>}
        </section>

        <section className={cn("min-h-0 min-w-0", !m && "hidden lg:block")} aria-label="Message">
          {m ? (
            <Message key={m} project={project} id={m} onBack={() => go({ q })} onDeleted={() => go({ q })} />
          ) : (
            <div className="grid h-full place-items-center p-10 text-center">
              <div>
                <Inbox className="mx-auto size-8 text-ink-4" />
                <p className="display mt-4 text-xl text-ink">Pick a message</p>
                <p className="mt-1 text-base text-ink-3">You can read it, follow its links and check how it looks.</p>
              </div>
            </div>
          )}
        </section>
      </div>
    </Page>
  );
}

function Crumbs({ project }: { project: string }) {
  return (
    <Link to="/projects/$project" params={{ project }} className="font-mono hover:text-ink">
      {project}
    </Link>
  );
}

type Tab = "preview" | "text" | "links" | "headers";

function Message({ project, id, onBack, onDeleted }: { project: string; id: string; onBack: () => void; onDeleted: () => void }) {
  const qc = useQueryClient();
  const d = useQuery(mq.message(project, id));
  const [tab, setTab] = useState<Tab | null>(null);
  const [deleting, setDeleting] = useState(false);
  const { can } = useMe();

  if (d.isPending)
    return (
      <div className="space-y-3 p-6">
        <Skeleton className="h-7 w-2/3" />
        <Skeleton className="h-4 w-1/3 opacity-60" />
        <Skeleton className="mt-6 h-64" />
      </div>
    );
  if (d.isError) return <ProblemNote className="m-6" error={d.error} />;
  const msg = d.data;
  const t: Tab = tab ?? (msg.html ? "preview" : "text");
  const links = msg.links ?? [];

  return (
    <article className="flex h-full min-h-0 flex-col">
      <header className="border-b border-rule px-5 pt-4 pb-4 sm:px-6">
        <div className="flex items-center gap-2">
          <button
            onClick={onBack}
            className="-ml-1 grid size-8 place-items-center rounded-md text-ink-3 hover:bg-hover lg:hidden"
            aria-label="Back to the list"
          >
            <ArrowLeft className="size-4" />
          </button>
          <StatusLabel m={msg} />
          <span className="ml-auto flex items-center gap-1">
            <a
              href={msg.rawUrl}
              download
              className="grid size-8 place-items-center rounded-md text-ink-3 hover:bg-hover hover:text-ink"
              title="Download .eml"
              aria-label="Download the raw message"
            >
              <Download className="size-4" />
            </a>
            {can("apply:reversible") && msg.delivery === "inbox" && (
              <button
                onClick={() => setDeleting(true)}
                className="grid size-8 place-items-center rounded-md text-ink-3 hover:bg-irr-wash hover:text-irr"
                aria-label="Delete this message"
              >
                <Trash2 className="size-4" />
              </button>
            )}
          </span>
        </div>
        <h2 className="display mt-2 text-2xl text-ink">{msg.subject || "(no subject)"}</h2>
        <dl className="mt-3 grid grid-cols-[3rem_1fr] gap-x-3 gap-y-0.5 text-sm">
          <dt className="text-ink-3">From</dt>
          <dd className="truncate text-ink-2">{msg.from}</dd>
          <dt className="text-ink-3">To</dt>
          <dd className="truncate text-ink-2">{(msg.to ?? []).join(", ")}</dd>
          <dt className="text-ink-3">When</dt>
          <dd className="text-ink-2" title={full(msg.createdAt)}>
            {relative(msg.createdAt)} · via {msg.source === "smtp" ? "SMTP" : "the API"} · {bytes(msg.size)}
          </dd>
        </dl>
        {msg.lastError && <p className="mt-2 rounded-md bg-irr-wash px-3 py-1.5 text-sm text-ink">Last attempt failed: {msg.lastError}</p>}
        {(msg.attachmentList ?? []).length > 0 && (
          <ul className="mt-3 flex flex-wrap gap-2">
            {(msg.attachmentList ?? []).map((a) => (
              <li key={a.index}>
                <a
                  href={mod.attachmentUrl(project, id, a.index)}
                  download={a.filename}
                  className="inline-flex items-center gap-2 rounded-lg border border-rule bg-paper px-3 py-1.5 text-sm text-ink-2 transition-colors hover:border-rule-strong hover:text-ink"
                >
                  <Paperclip className="size-3.5 text-ink-3" />
                  {a.filename}
                  <span className="font-mono text-xs text-ink-4">{bytes(a.size)}</span>
                </a>
              </li>
            ))}
          </ul>
        )}
      </header>
      <nav className="flex gap-1 border-b border-rule px-4 sm:px-5" aria-label="View">
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
                "relative h-10 px-2.5 text-sm text-ink-3 transition-colors hover:text-ink",
                t === k && "font-medium text-ink after:absolute after:inset-x-2 after:-bottom-px after:h-0.5 after:rounded-full after:bg-ink",
              )}
            >
              {label}
            </button>
          ))}
      </nav>
      <div className="min-h-0 flex-1 overflow-auto bg-paper-sunk/60 p-4 sm:p-5">
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
    <div className="mx-auto max-w-[44rem]">
      <div className="overflow-hidden rounded-lg border border-rule shadow-pop">
        <iframe
          title="Message preview"
          sandbox="allow-popups allow-popups-to-escape-sandbox"
          srcDoc={doc}
          className="block h-[min(60vh,40rem)] w-full bg-white"
        />
      </div>
      <p className="mt-2 text-xs text-ink-3">
        Shown in a sandbox: scripts and forms are off{remote ? ", and remote images (often trackers) are blocked" : ""}. Links open in a new tab.
      </p>
    </div>
  );
}

function LinksView({ links }: { links: string[] }) {
  if (links.length === 0) return <Empty title="No links in this message" />;
  return (
    <div>
      <p className="mb-3 text-sm text-ink-3">Every http(s) link in the message. Handy for sign-in and verification flows.</p>
      <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised">
        {links.map((l) => (
          <li key={l} className="flex items-center gap-2 px-4 py-2.5">
            <code className="min-w-0 flex-1 truncate font-mono text-[0.8125rem] text-ink-2" title={l}>
              {l}
            </code>
            <CopyButton value={l} label="Copy link" />
            <a
              href={l}
              target="_blank"
              rel="noopener noreferrer"
              className="grid size-7 place-items-center rounded-md text-ink-3 hover:bg-hover hover:text-ink"
              aria-label="Open link in a new tab"
            >
              <ArrowUpRight className="size-3.5" />
            </a>
          </li>
        ))}
      </ul>
    </div>
  );
}

function HeadersView({ msg }: { msg: EmailDetail }) {
  return (
    <Untrusted label="Headers, as received">
      <dl className="divide-y divide-rule/70 font-mono text-[0.75rem]">
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
    <div className="grid grid-cols-[minmax(8rem,12rem)_1fr] gap-3 px-4 py-1.5">
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
          <span>
            <Crumbs project={project} /> /{" "}
            <Link to="/projects/$project/email" params={{ project }} className="hover:text-ink">
              email
            </Link>
          </span>
        }
        title="Email settings"
        lede="How mail leaves the box, the credentials your apps use, and the addresses this project won't write to."
      />
      <div className="mt-10 grid gap-10 lg:grid-cols-2">
        <RelayCard admin={admin} />
        <div className="flex flex-col gap-10">
          <SmtpCard project={project} />
          <Suppressions project={project} />
        </div>
      </div>
    </Page>
  );
}

function RelayCard({ admin }: { admin: boolean }) {
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

  return (
    <section aria-labelledby="relay">
      <h2 id="relay" className="display-italic mb-3 text-xl text-ink">
        Sending for real
      </h2>
      <div className="rounded-xl border border-rule bg-raised/60 p-5">
        {st.data?.mode === "relay" && relay ? (
          <>
            <p className="text-base text-ink">
              Relaying through <code className="font-mono">{relay.host}</code>:{relay.port} ({relay.tls})
              {relay.username ? ` as ${relay.username}` : ""}.
            </p>
            <p className="mt-1 text-sm text-ink-3">
              Password {relay.passwordSet ? "stored encrypted, never shown" : "not set"} · changed {relative(relay.updatedAt)}
            </p>
          </>
        ) : (
          <p className="text-base text-ink-2">
            No relay yet, so mail is captured in each project's dev inbox. Point the box at any SMTP relay (Resend, Postmark, SES, your provider) to
            send for real.
          </p>
        )}
        {admin ? (
          <form
            className="mt-5 grid gap-3 sm:grid-cols-[1fr_6rem]"
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
              <Input id="r-port" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ""))} inputMode="numeric" />
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
            <div className="flex items-center justify-between gap-3 sm:col-span-2">
              <p className="text-sm text-ink-3">Mail then leaves the box for real. Preview deployments keep using the dev inbox.</p>
              <Button type="submit" variant="primary" disabled={!host.trim() || save.isPending}>
                {save.isPending ? "Saving…" : relay ? "Update relay" : "Connect relay"}
              </Button>
            </div>
          </form>
        ) : (
          <p className="mt-4 text-sm text-ink-3">Only the box owner and admins can set the relay.</p>
        )}
        {admin && relay && (
          <div className="mt-6 border-t border-rule pt-5">
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
            {test.data && <p className={cn("mt-2 text-sm", test.data.ok ? "text-rev" : "text-irr")}>{test.data.detail}</p>}
            {test.isError && <ProblemNote className="mt-2" error={test.error} />}
            <Button variant="danger-quiet" size="sm" className="mt-4" onClick={() => setRemoving(true)}>
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
    </section>
  );
}

function SmtpCard({ project }: { project: string }) {
  const [env, setEnv] = useState<Record<string, string> | null>(null);
  const [err, setErr] = useState<unknown>(null);
  return (
    <section aria-labelledby="smtp">
      <h2 id="smtp" className="display-italic mb-3 text-xl text-ink">
        SMTP for your apps
      </h2>
      <div className="rounded-xl border border-rule bg-raised/60 p-5">
        <p className="text-base text-ink-2">
          Apps in <code className="font-mono text-ink">{project}</code> already get <code className="font-mono text-ink">SMTP_URL</code> and friends.
          Any SMTP library works, or <code className="font-mono text-ink">send()</code> from tiffin-sdk/email.
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
            className="mt-4"
            cmd={Object.entries(env)
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([k, v]) => `${k}=${v}`)
              .join(" ")}
          />
        )}
      </div>
    </section>
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
  return (
    <section aria-labelledby="supp">
      <h2 id="supp" className="display-italic mb-1 text-xl text-ink">
        Won't write to
      </h2>
      <p className="mb-3 text-sm text-ink-3">Hard bounces land here on their own. Add unsubscribes and complaints yourself.</p>
      <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
        {(list.data ?? []).map((s) => (
          <li key={s.address} className="flex items-center gap-3 px-4 py-2.5">
            <ShieldBan className="size-4 shrink-0 text-ink-3" />
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
        <li className="px-4 py-3">
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
            <Button size="sm" type="submit" disabled={!address.includes("@") || add.isPending}>
              Suppress
            </Button>
          </form>
          {add.isError && <ProblemNote className="mt-2" error={add.error} />}
        </li>
      </ul>
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
    </section>
  );
}
