import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, Check, FileKey, RadioTower, Search, TriangleAlert } from "lucide-react";
import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { Confirm } from "@/components/confirm";
import { useTitle } from "@/components/favicon";
import { accessCrumbs } from "@/components/health-kit";
import { NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { StatusDot } from "@/components/project-domains";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { connectGoogle, disconnectGoogle, GOOGLE_CONSOLE, googleQuery, indexNowQuery, type GoogleConnection } from "@/lib/search";
import { useWho } from "@/lib/me";
import { relative } from "@/lib/time";

/**
 * Settings › Search engines: every engine the box works with. IndexNow is
 * on with nothing to set up (it shows which addresses it covers and when
 * it last told the engines); Google Search Console takes a service account
 * key, made once in the owner's own Google Cloud project. Then the box
 * verifies every app on a domain of its own, submits sitemaps and reads
 * searches and index status back. What it finds shows in each project's
 * Analytics › Search.
 */
export function SearchSettingsPage() {
  useTitle("Search engines");
  const g = useQuery(googleQuery);
  if (g.isError && g.error instanceof ApiError && [404, 412, 501].includes(g.error.status)) return <NotOnBox what="Search engine settings" />;
  const forbidden = g.isError && g.error instanceof ApiError && g.error.status === 403;
  return (
    <Page>
      <PageHeader
        eyebrow={accessCrumbs}
        title="Search engines"
        lede="Apps on a domain of their own are findable. What people search for and which bots read the pages shows in each project’s Analytics › Search."
      />
      {!forbidden && <IndexNowCard />}
      {forbidden ? (
        <p className="mt-8 max-w-[40rem] border-y border-rule py-4 text-[0.875rem] text-ink-2">IndexNow is on for every app on a domain of its own. Only the box’s owner or an admin can connect Google. Ask them to open Settings › Search engines.</p>
      ) : g.isError ? (
        <ProblemNote className="mt-8" error={g.error} />
      ) : !g.data ? (
        <Skeleton className="mt-5 h-72 max-w-[44rem]" />
      ) : (
        <GoogleCard conn={g.data} />
      )}
    </Page>
  );
}

/** IndexNow: on by itself. Which addresses it covers and when each was last told. */
function IndexNowCard() {
  const q = useQuery(indexNowQuery);
  const v = q.data;
  const list = v?.addresses ?? [];
  return (
    <section className="mt-9 max-w-[44rem] rounded-[14px] border border-rule-2 bg-paper-raised shadow-raised" aria-label="IndexNow">
      <div className="flex items-start gap-4 px-5 py-5 sm:px-6">
        <span className="grid size-10 shrink-0 place-items-center rounded-[10px] bg-paper-sunk text-ink">
          <RadioTower className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="flex items-center gap-2 text-[1rem] font-[550] text-ink">
            {v?.on !== false && <Check className="size-4 text-ok" strokeWidth={2.5} />}
            {v?.on === false ? "IndexNow is off on a local box." : "IndexNow is on. Nothing to set up."}
          </h2>
          <p className="mt-0.5 text-[0.875rem] text-ink-2">
            {v?.on === false
              ? "Nothing outside this box can reach its apps, so there is nothing to tell search engines."
              : "Bing, Yandex, Seznam, Naver and Yep hear about new, changed and removed pages after each deploy and every 6 hours. Bing Webmaster Tools isn’t needed for that."}
          </p>
          {q.isError && <ProblemNote className="mt-3" error={q.error} />}
          {v?.on !== false && q.isSuccess && (
            <ul className="mt-3 divide-y divide-rule border-y border-rule">
              {list.length === 0 && <li className="py-2.5 text-[0.8125rem] text-ink-3">No app has a domain of its own yet. Each one is covered as soon as it goes live.</li>}
              {list.map((a) => (
                <li key={a.host} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 py-2.5">
                  <span className="ident min-w-0 truncate text-[0.8125rem] text-ink">{a.host}</span>
                  <span className={cn("text-xs", a.last && !a.last.ok ? "text-warn-ink" : "text-ink-3")}>
                    {!a.last
                      ? "Not told yet"
                      : a.last.ok
                        ? `Told ${relative(a.last.at)}${a.last.pages ? ` · ${a.last.pages === 1 ? "1 page" : `${a.last.pages} pages`}` : " · nothing new"}`
                        : `Refused ${relative(a.last.at)}`}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </section>
  );
}

function GoogleCard({ conn }: { conn: GoogleConnection }) {
  const qc = useQueryClient();
  const who = useWho();
  const [replacing, setReplacing] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const [key, setKey] = useState<{ name: string; text: string; email?: string } | null>(null);
  const [owner, setOwner] = useState(conn.owner ?? "");
  const [fileError, setFileError] = useState("");
  const file = useRef<HTMLInputElement>(null);
  const connect = useMutation({
    mutationFn: () => connectGoogle(key?.text ?? "", owner.trim()),
    onSuccess: (r) => {
      qc.setQueryData(googleQuery.queryKey, r);
      void qc.invalidateQueries({ queryKey: ["search"] });
      setKey(null);
      setReplacing(false);
      toast({ title: "Google Search Console is connected.", detail: "The box verifies your domains now; it takes a minute or two." });
    },
  });
  const showForm = !conn.connected || replacing;
  const props = conn.properties ?? [];

  const pick = async (f: File | undefined) => {
    setFileError("");
    if (!f) return;
    const text = await f.text();
    try {
      const j = JSON.parse(text) as { type?: string; client_email?: string };
      if (j.type !== "service_account") throw new Error();
      setKey({ name: f.name, text, email: j.client_email });
    } catch {
      setKey(null);
      setFileError("That isn’t a service account key. It’s the JSON file from Keys › Add key › Create new key.");
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (key) connect.mutate();
  };

  return (
    <section className="mt-5 max-w-[44rem] rounded-[14px] border border-rule-2 bg-paper-raised shadow-raised" aria-label="Google Search Console">
      <div className="flex items-start gap-4 px-5 pt-5 sm:px-6">
        <span className="grid size-10 shrink-0 place-items-center rounded-[10px] bg-paper-sunk text-ink">
          <Search className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="flex items-center gap-2 text-[1rem] font-[550] text-ink">
            {conn.connected && <Check className="size-4 text-ok" strokeWidth={2.5} />}
            {conn.connected ? "Connected to Google Search Console." : "Google Search Console"}
          </h2>
          <p className="mt-0.5 text-[0.875rem] text-ink-2">
            {conn.connected ? (
              <>
                The box signs in as <span className="ident text-[0.8125rem] text-ink">{conn.clientEmail}</span>
                {conn.owner ? (
                  <>
                    {" "}
                    and adds <span className="text-ink">{conn.owner}</span> as an owner of each property.
                  </>
                ) : (
                  "."
                )}
              </>
            ) : (
              "Optional. Google reads your sitemaps without it. With it, you see what people search for and which pages Google indexed, beside your analytics:"
            )}
          </p>
        </div>
      </div>

      {!conn.connected && (
        <ul className="mt-4 grid gap-2 px-5 text-[0.875rem] text-ink sm:grid-cols-3 sm:px-6">
          {[
            ["Verified for you", "Every app on a domain of its own, through DNS. No TXT records to copy."],
            ["Searches beside analytics", "Clicks, impressions and searches for each domain, 16 months of them, in Analytics › Search."],
            ["Which pages are indexed", "Google’s answer for each page, and why one isn’t."],
          ].map(([t, d]) => (
            <li key={t} className="rounded-[10px] bg-paper-sunk px-3.5 py-3">
              <p className="font-[550]">{t}</p>
              <p className="mt-0.5 text-[0.8125rem] text-ink-2">{d}</p>
            </li>
          ))}
        </ul>
      )}

      {conn.connected && props.length > 0 && (
        <ul className="mx-5 mt-4 divide-y divide-rule border-y border-rule sm:mx-6">
          {props.map((p) => (
            <li key={p.property} className="flex items-start gap-2.5 py-2.5">
              <StatusDot tone={p.state === "verified" ? "ok" : p.state === "failed" ? "bad" : "busy"} className="mt-1.5" />
              <span className="min-w-0">
                <span className="ident block text-[0.8125rem] text-ink">{p.property}</span>
                <span className="block text-xs text-ink-3">
                  {p.state === "verified" ? `Verified ${p.verifiedAt ? relative(p.verifiedAt) : ""}${p.method === "DNS_TXT" ? ", through DNS" : ", through Google’s file on the host"}` : p.detail}
                </span>
                {p.state === "verified" && p.detail?.includes("would not add") && <span className="block text-xs text-warn-ink">{p.detail.replace(/^Verified, but /, "")}</span>}
              </span>
            </li>
          ))}
        </ul>
      )}
      {conn.connected && conn.error && (
        <div className="mx-5 mt-4 flex items-start gap-2 rounded-[8px] bg-warn-wash px-3 py-2.5 text-sm text-ink sm:mx-6">
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn-ink" />
          <div className="min-w-0">
            <p>Google refuses the key{conn.errorAt ? ` since ${relative(conn.errorAt)}` : ""}, so search data isn’t updating.</p>
            <p className="mt-0.5 text-[0.8125rem] text-ink-2">{conn.error}</p>
            <p className="mt-1 text-[0.8125rem] text-ink-2">
              If the key was deleted, make a new one for the same service account and choose <b className="font-[550]">Replace key</b>: every domain stays verified. If an API was turned off, turn it back on.
            </p>
          </div>
        </div>
      )}
      {conn.connected && !showForm && <OwnerForm current={conn.owner ?? ""} />}

      {conn.connected && props.length === 0 && (
        <p className="mx-5 mt-4 border-y border-rule py-2.5 text-[0.8125rem] text-ink-3 sm:mx-6">No app has a domain of its own yet. Each one is verified as soon as it goes live.</p>
      )}

      {showForm && (
        <form onSubmit={submit} className="mt-5 border-t border-rule px-5 py-5 sm:px-6">
          <p className="mb-4 text-[0.8125rem] text-ink-3">Once, about 5 minutes, in your own Google Cloud account. Nothing is billed.</p>
          <ol className="flex flex-col gap-4 text-[0.875rem] text-ink">
            <Step n={1}>
              <span>Create a Google Cloud project, named tiffin for example.</span>
              <Link href={GOOGLE_CONSOLE.project}>New project</Link>
            </Step>
            <Step n={2}>
              <span>In that project, turn on two APIs: choose Enable on each.</span>
              <span className="mt-2 flex flex-wrap gap-2">
                <Link href={GOOGLE_CONSOLE.searchApi}>Search Console API</Link>
                <Link href={GOOGLE_CONSOLE.verifyApi}>Site Verification API</Link>
              </span>
            </Step>
            <Step n={3}>
              <span>
                Create a service account named <b className="font-[550]">tiffin</b>. Skip the roles: it needs none. Then open it, choose <b className="font-[550]">Keys › Add key › Create new key › JSON</b>
                , and a file downloads.
              </span>
              <Link href={GOOGLE_CONSOLE.accounts}>Create service account</Link>
            </Step>
            <Step n={4}>
              <span>Choose that file, and the Google account you use for Search Console.</span>
              <div className="mt-2 flex w-full flex-col gap-2">
                <input ref={file} type="file" accept="application/json,.json" className="sr-only" onChange={(e) => void pick(e.target.files?.[0])} aria-label="Service account key file" />
                <button
                  type="button"
                  onClick={() => file.current?.click()}
                  className="flex h-9 items-center gap-2 rounded-[7px] border border-dashed border-rule-3 px-3 text-left text-[0.8125rem] text-ink-2 hover:border-ink-4 hover:text-ink"
                >
                  <FileKey className="size-4 shrink-0 text-ink-3" />
                  <span className="min-w-0 truncate">{key ? <span className="ident text-ink">{key.email ?? key.name}</span> : "Choose the key file…"}</span>
                </button>
                <div className="flex w-full flex-col gap-2 sm:flex-row">
                  <Input id="g-owner-new" name="email" type="email" value={owner} onChange={(e) => setOwner(e.target.value)} onInput={(e) => setOwner(e.currentTarget.value)} placeholder="you@gmail.com (optional)" autoComplete="email" className="sm:flex-1" aria-label="Your Google account" />
                  <Button type="submit" variant="primary" size="lg" className="h-9" disabled={!key || connect.isPending}>
                    {connect.isPending ? "Checking the key…" : "Connect"}
                  </Button>
                </div>
              </div>
              {fileError && <p className="mt-1.5 text-xs text-danger">{fileError}</p>}
              <p className="mt-1.5 text-xs text-ink-3">
                The box signs in with it to check it, then keeps it encrypted. It never leaves the box. Your account is added as an owner, so the domains show in your Search Console too.
              </p>
            </Step>
          </ol>
          {connect.isError && <ProblemNote className="mt-4" error={connect.error} />}
          {replacing && (
            <button type="button" onClick={() => setReplacing(false)} className="mt-3 text-[0.8125rem] text-ink-3 hover:text-ink">
              Keep the current key
            </button>
          )}
        </form>
      )}

      {conn.connected && (
        <div className="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-rule px-5 py-3.5 sm:px-6">
          <span className="min-w-0 flex-1 basis-56 text-xs text-ink-3">
            Connected {conn.connectedAt ? relative(conn.connectedAt) : ""} by {who(conn.by ?? "")} · project {conn.projectId}
          </span>
          {!showForm && (
            <Button variant="ghost" size="sm" onClick={() => setReplacing(true)}>
              Replace key
            </Button>
          )}
          <Button variant="danger-quiet" size="sm" onClick={() => setLeaving(true)}>
            Disconnect…
          </Button>
        </div>
      )}

      <Confirm
        open={leaving}
        onClose={() => setLeaving(false)}
        title="Disconnect Google?"
        body="The box forgets the key and the search data it read. Your properties stay in Search Console, and the verification records stay in DNS."
        action="Disconnect"
        run={() => disconnectGoogle()}
        done={() => {
          void qc.invalidateQueries({ queryKey: ["search-google"] });
          void qc.invalidateQueries({ queryKey: ["search"] });
          toast({ title: "Google is disconnected.", detail: "You can delete the key in Google Cloud, under IAM › Service accounts › Keys." });
        }}
      />
    </section>
  );
}

function Link({ href, children }: { href: string; children: ReactNode }) {
  return (
    <Button asChild size="sm" className="mt-2">
      <a href={href} target="_blank" rel="noopener noreferrer">
        {children}
        <ArrowUpRight className="text-ink-3" />
      </a>
    </Button>
  );
}

function Step({ n, children }: { n: number; children: ReactNode }) {
  return (
    <li className="grid grid-cols-[1.25rem_minmax(0,1fr)] gap-x-3">
      <span className="ident mt-px grid size-5 place-items-center rounded-full border border-rule-2 text-[0.6875rem] text-ink-3">{n}</span>
      <div className="flex min-w-0 flex-col items-start">{children}</div>
    </li>
  );
}

/** The Google account added as an owner of every property, changed without the key. */
function OwnerForm({ current }: { current: string }) {
  const qc = useQueryClient();
  const [owner, setOwner] = useState(current);
  const save = useMutation({
    mutationFn: () => connectGoogle("", owner.trim()),
    onSuccess: (r) => {
      qc.setQueryData(googleQuery.queryKey, r);
      toast({ title: owner.trim() ? `${owner.trim()} is added as an owner.` : "No account is added as an owner.", detail: owner.trim() ? "Your domains show in that account’s Search Console within a minute." : undefined });
    },
  });
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
      className="mx-5 mt-4 sm:mx-6"
    >
      <label htmlFor="g-owner" className="text-[0.8125rem] text-ink-2">
        Your Google account <span className="text-ink-3">· added as an owner of each property, so they show in your Search Console</span>
      </label>
      <div className="mt-1.5 flex flex-col gap-2 sm:flex-row">
        <Input id="g-owner" name="email" type="email" value={owner} onChange={(e) => setOwner(e.target.value)} onInput={(e) => setOwner(e.currentTarget.value)} placeholder="you@gmail.com" autoComplete="email" className="sm:flex-1" />
        <Button type="submit" size="lg" className="h-9" disabled={save.isPending || owner.trim() === current}>
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      </div>
      {save.isError && <ProblemNote className="mt-2" error={save.error} />}
    </form>
  );
}
