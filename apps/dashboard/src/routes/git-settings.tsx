import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowUpRight, Check, TriangleAlert } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { Confirm } from "@/components/confirm";
import { CopyButton } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { GitHubMark } from "@/components/github-mark";
import { NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { connectGitHub, disconnectGitHub, githubQuery, installGitHub, type GitHubStatus } from "@/lib/github";
import { relative } from "@/lib/time";
import { Section } from "@/routes/box-settings";

export type GitSearch = { installed?: boolean; requested?: boolean; error?: string };

/**
 * Settings › Git: connect the box to GitHub (one button: the box makes its own
 * GitHub App, owned by you), pick the repositories it may see, and see what it
 * did because of GitHub. Disconnecting is the one thing that asks first.
 */
export function GitSettingsPage({ search }: { search: GitSearch }) {
  useTitle("Git");
  const st = useQuery({ ...githubQuery, refetchInterval: 20_000 });
  const navigate = useNavigate();
  const flashed = useRef(false);
  const [error, setError] = useState<string | undefined>(search.error);

  // Coming back from GitHub: say what happened once, then tidy the address.
  useEffect(() => {
    if (flashed.current || (!search.installed && !search.requested && !search.error)) return;
    flashed.current = true;
    if (search.installed)
      toast({
        title: "GitHub is connected.",
        detail: "Import a repository from New project; every push to its branch deploys.",
        action: { label: "Import a repository", run: () => void navigate({ to: "/new", search: { starter: "github" } }) },
      });
    if (search.requested) toast({ title: "GitHub asked an organization owner to approve the install.", detail: "It shows up here once they do." });
    void navigate({ to: "/settings/git", search: {}, replace: true });
  }, [search, navigate]);

  if (st.isError && notOnBox(st.error)) return <NotOnBox what="Git" />;
  const s = st.data;

  return (
    <Page>
      <PageHeader
        title="Git"
        lede="Connect GitHub and your code deploys itself: every push to a project’s branch goes live, and every pull request gets its own preview address."
      />
      {error && (
        <div role="alert" className="mt-6 flex items-start gap-2.5 rounded-[10px] border border-danger-rule bg-danger-wash px-4 py-3 text-sm text-ink">
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-danger" />
          <p className="min-w-0 flex-1">{error}</p>
          <button className="text-ink-3 hover:text-ink" onClick={() => setError(undefined)}>
            Dismiss
          </button>
        </div>
      )}
      {st.isError && <ProblemNote className="mt-6" error={st.error} />}
      {!s ? <Skeleton className="mt-9 h-44" /> : s.connected ? <Connected s={s} /> : <NotConnected s={s} />}
    </Page>
  );
}

function NotConnected({ s }: { s: GitHubStatus }) {
  const [org, setOrg] = useState("");
  const [inOrg, setInOrg] = useState(false);
  const go = useMutation({ mutationFn: () => connectGitHub(inOrg ? org.trim() : undefined) });
  const manage = s.canManage;
  return (
    <section className="mt-9 max-w-[44rem] rounded-[14px] border border-rule-2 bg-paper-raised px-6 py-5 shadow-raised" aria-label="Connect GitHub">
      <div className="flex items-start gap-4">
        <span className="grid size-10 shrink-0 place-items-center rounded-[10px] bg-paper-sunk text-ink">
          <GitHubMark className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="text-[1rem] font-[550] text-ink">Not connected yet.</h2>
          <p className="mt-1 text-[0.875rem] text-ink-2">
            Connecting makes a small GitHub App for this box, owned by you. GitHub asks you to confirm it, then you choose which repositories it may see. Its key stays on this box, encrypted.
          </p>
          {s.problem && <p className="mt-3 text-sm text-danger">{s.problem}</p>}
          {!s.reachable ? (
            <p className="mt-4 flex items-start gap-2 rounded-[8px] bg-warn-wash px-3 py-2.5 text-sm text-ink">
              <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn-ink" />
              <span>{s.reachableHint}</span>
            </p>
          ) : !manage ? (
            <p className="mt-4 text-sm text-ink-3">Only the box’s owner or an admin can connect GitHub. Ask them to open Settings › Git.</p>
          ) : (
            <>
              <div className="mt-5 flex flex-wrap items-center gap-3">
                <Button variant="primary" size="lg" onClick={() => go.mutate()} disabled={go.isPending || (inOrg && !org.trim())}>
                  <GitHubMark />
                  {go.isPending ? "Opening GitHub…" : "Connect GitHub"}
                </Button>
                <label className="flex items-center gap-2 text-sm text-ink-2">
                  <input type="checkbox" checked={inOrg} onChange={(e) => setInOrg(e.target.checked)} className="size-4 accent-[var(--brass)]" />
                  In an organization
                </label>
              </div>
              {inOrg && (
                <div className="mt-3 max-w-[20rem]">
                  <label htmlFor="org" className="mb-1 block text-xs text-ink-3">
                    Organization name on GitHub
                  </label>
                  <Input id="org" value={org} onChange={(e) => setOrg(e.target.value)} placeholder="acme" autoComplete="off" spellCheck={false} className="ident" />
                </div>
              )}
              {go.isError && <ProblemNote className="mt-4" error={go.error} />}
            </>
          )}
        </div>
      </div>
      <ol className="mt-5 grid gap-2 border-t border-rule pt-4 text-[0.8125rem] text-ink-3 sm:grid-cols-3">
        {["Confirm the app on GitHub", "Choose repositories", "Import one in New project"].map((t, i) => (
          <li key={t} className="flex items-center gap-2">
            <span className="ident grid size-5 place-items-center rounded-full border border-rule-2 text-[0.6875rem] text-ink-3">{i + 1}</span>
            {t}
          </li>
        ))}
      </ol>
    </section>
  );
}

function Connected({ s }: { s: GitHubStatus }) {
  const qc = useQueryClient();
  const [leaving, setLeaving] = useState(false);
  const install = useMutation({ mutationFn: installGitHub });
  const installs = s.installations ?? [];
  const events = s.events ?? [];
  const app = s.app;
  const manage = s.canManage;
  const own = s.source === "box";

  return (
    <>
      <div className="mt-8 flex max-w-[44rem] flex-wrap items-center gap-x-4 gap-y-3 rounded-[14px] border border-rule-2 bg-paper-raised px-5 py-4 shadow-raised">
        <span className="grid size-10 shrink-0 place-items-center rounded-[10px] bg-paper-sunk text-ink">
          <GitHubMark className="size-5" />
        </span>
        <div className="min-w-0 flex-1 basis-56">
          <p className="flex items-center gap-2 text-[1rem] font-[550] text-ink">
            {installs.length ? <Check className="size-4 text-ok" strokeWidth={2.5} /> : null}
            {installs.length ? "Connected to GitHub." : "Almost there: choose repositories."}
          </p>
          <p className="mt-0.5 text-[0.875rem] text-ink-2">
            {installs.length
              ? `${app?.name ?? app?.slug ?? "The app"} can see ${installs.map((i) => (i.repositories === "all" ? `every repository of ${i.account}` : `some of ${i.account}’s repositories`)).join(" and ")}.`
              : `${app?.name ?? app?.slug ?? "The app"} exists on GitHub. Install it on the repositories you want to deploy.`}
          </p>
        </div>
        <div className="flex flex-wrap gap-2 max-sm:w-full max-sm:pl-14">
          {installs.length > 0 && (
            <Button asChild variant="primary" size="lg">
              <Link to="/new" search={{ starter: "github" }}>
                Import a repository
              </Link>
            </Button>
          )}
          {manage && (
            <Button variant={installs.length ? "secondary" : "primary"} size="lg" onClick={() => install.mutate()} disabled={install.isPending}>
              {install.isPending ? "Opening GitHub…" : installs.length ? "Add repositories" : "Install on repositories"}
            </Button>
          )}
        </div>
        {install.isError && <ProblemNote className="basis-full" error={install.error} />}
      </div>
      {s.problem && <p className="mt-4 max-w-[44rem] rounded-[8px] bg-warn-wash px-3.5 py-2.5 text-sm text-ink">{s.problem}</p>}
      {!s.reachable && s.reachableHint && <p className="mt-4 max-w-[44rem] rounded-[8px] bg-warn-wash px-3.5 py-2.5 text-sm text-ink">{s.reachableHint}</p>}

      {installs.length > 0 && (
        <Section title="Accounts" note="Where the app is installed. Change which repositories it sees on GitHub.">
          <ul className="divide-y divide-rule border-y border-rule">
            {installs.map((i) => (
              <li key={i.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-2.5">
                <span className="min-w-0 flex-1">
                  <span className="block text-[0.875rem] text-ink">{i.account}</span>
                  <span className="block text-xs text-ink-3">
                    {i.accountType === "Organization" ? "Organization" : "Personal account"} · {i.repositories === "all" ? "all repositories" : "selected repositories"}
                    {i.suspended ? " · suspended" : ""}
                  </span>
                </span>
                {i.settingsUrl && (
                  <a href={i.settingsUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-[0.8125rem] text-ink-2 hover:text-brass-ink">
                    Change on GitHub <ArrowUpRight className="size-3.5" />
                  </a>
                )}
              </li>
            ))}
          </ul>
        </Section>
      )}

      <Section title="Recently" note="What the box did because of GitHub.">
        {events.length === 0 ? (
          <p className="border-y border-rule py-3 text-sm text-ink-3">Nothing yet. Pushes and pull requests show up here.</p>
        ) : (
          <ul className="divide-y divide-rule border-y border-rule">
            {events.slice(0, 12).map((e, i) => (
              <li key={i} className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4 py-2">
                <span className="min-w-0 text-[0.8125rem] text-ink">
                  {e.repo && <span className="ident mr-1.5 text-[0.75rem] text-ink-3">{e.repo}</span>}
                  <span className={cn(!e.ok && "text-danger")}>{e.summary}</span>
                </span>
                <span className="text-xs text-ink-3 tnum">{relative(e.at)}</span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title="The app" note={own ? "This box made it, and keeps its key encrypted." : s.source === "file" ? "Set by whoever runs this box." : "An existing app given to the box."}>
        <dl className="grid grid-cols-[8rem_minmax(0,1fr)] text-[0.875rem]">
          {(
            [
              ["Name", app?.htmlUrl ? <Ext href={app.htmlUrl}>{app.name || app.slug}</Ext> : (app?.name ?? app?.slug ?? "…")],
              app?.owner && ["Belongs to", app.owner],
              app?.createdAt && ["Made", relative(app.createdAt)],
              ["Deliveries to", <span className="flex min-w-0 items-start gap-1"><span className="ident min-w-0 text-[0.75rem] break-all">{s.webhookUrl}</span><CopyButton value={s.webhookUrl} label="Copy the webhook address" className="size-6" /></span>],
              s.shared && ["Shared", "Other accounts use this app too; the box only acts for installations made from here."],
            ].filter(Boolean) as Array<[string, ReactNode]>
          ).map(([k, v]) => (
            <div key={k} className="col-span-2 grid grid-cols-subgrid border-b border-rule py-2.5">
              <dt className="text-ink-3">{k}</dt>
              <dd className="min-w-0 text-ink">{v}</dd>
            </div>
          ))}
        </dl>
        {manage && s.source !== "file" && (
          <div className="mt-4 flex flex-wrap items-center gap-3">
            <Button variant="danger-quiet" size="md" onClick={() => setLeaving(true)}>
              Disconnect GitHub
            </Button>
            <span className="text-xs text-ink-3">Pushes stop deploying. What runs keeps running.</span>
          </div>
        )}
      </Section>

      <Confirm
        open={leaving}
        onClose={() => setLeaving(false)}
        title="Disconnect GitHub?"
        body={
          <>
            The box forgets the app’s key, so pushes and pull requests stop deploying. Apps keep running and keep their repository settings. {own ? "The app stays on GitHub until you delete it there; connecting again makes a new one." : ""}
          </>
        }
        action="Disconnect"
        run={disconnectGitHub}
        done={() => {
          void qc.invalidateQueries({ queryKey: ["github"] });
          toast({ title: "GitHub is disconnected.", detail: own && app?.slug ? "You can delete the app on GitHub, under Settings › Developer settings › GitHub Apps." : undefined });
        }}
      />
    </>
  );
}

function Ext({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-brass-ink hover:text-ink">
      {children}
      <ArrowUpRight className="size-3.5" />
    </a>
  );
}
