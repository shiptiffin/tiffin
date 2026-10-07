import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ChevronLeft, ChevronRight, Search, Settings2 } from "lucide-react";
import { useEffect, useEffectEvent, useState, type ReactNode } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { mod3, type AuthOverview, type AuthUser } from "@/api/modules";
import { AuthSettings, methodName } from "@/components/auth-settings";
import { Confirm } from "@/components/confirm";
import { CopyButton } from "@/components/copy";
import { Reading, Readings } from "@/components/data-parts";
import { useTitle } from "@/components/favicon";
import { StateSentence } from "@/components/jobs-words";
import { Crumbs, NotOnBox, Page, PageHeader, Skeleton, Tabs } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { countWords, int, pct } from "@/lib/format";
import { useMe } from "@/lib/me";
import { useShortcut } from "@/lib/shortcuts";
import { clock, dayKey, full, relative } from "@/lib/time";

// ------------------------------------------------------------------ shared

function Header({ project, title, lede, actions, crumbs, tabs = true }: { project: string; title: ReactNode; lede?: ReactNode; actions?: ReactNode; crumbs?: Array<{ label: ReactNode; to?: string; params?: Record<string, string> }>; tabs?: boolean }) {
  return (
    <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, ...(crumbs ?? [])]} />} title={title} lede={lede} actions={actions}>
      {tabs && (
        <Tabs
          items={[
            { to: "/projects/$project/users", params: { project }, label: "Accounts" },
            { to: "/projects/$project/orgs", params: { project }, label: "Organizations" },
          ]}
        />
      )}
    </PageHeader>
  );
}

function Label({ children, id, action }: { children: ReactNode; id?: string; action?: ReactNode }) {
  return (
    <div className="mb-2.5 flex min-h-7 items-end justify-between gap-3">
      <h2 id={id} className="label">
        {children}
      </h2>
      {action}
    </div>
  );
}

/** Initials on a quiet disc: people round, organizations square. No colours: names aren't status. */
function Avatar({ name, square, big }: { name: string; square?: boolean; big?: boolean }) {
  const initials = name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0])
    .join("")
    .toUpperCase();
  return (
    <span
      aria-hidden
      className={cn(
        "grid shrink-0 place-items-center border border-rule bg-paper-sunk font-[550] tracking-[0.02em] text-ink-2",
        big ? "size-9 text-[0.8125rem]" : "size-7 text-[0.6875rem]",
        square ? (big ? "rounded-[8px]" : "rounded-[6px]") : "rounded-full",
      )}
    >
      {initials || "?"}
    </span>
  );
}

function SearchBox({ value, onChange, label, placeholder }: { value: string; onChange: (v: string) => void; label: string; placeholder: string }) {
  return (
    <label className="flex h-9 w-full max-w-[24rem] items-center gap-2 rounded-[8px] border border-rule-2 bg-paper-raised px-3 transition-colors focus-within:border-brass">
      <Search className="size-4 shrink-0 text-ink-3" aria-hidden />
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        aria-label={label}
        type="search"
        className="h-full min-w-0 flex-1 bg-transparent text-[0.875rem] text-ink outline-hidden placeholder:text-ink-3"
      />
    </label>
  );
}

function useSearchBox(initial: string, onSearch: (q: string) => void) {
  const [q, setQ] = useState(initial);
  // Only typing searches: a new initial value or callback alone doesn't.
  const search = useEffectEvent((v: string) => v !== initial && onSearch(v));
  useEffect(() => {
    const t = setTimeout(() => search(q), 250);
    return () => clearTimeout(t);
  }, [q]);
  return [q, setQ] as const;
}

const PAGE = 25;
const dateFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short" });
const dateYearFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", year: "numeric" });
const longFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long", year: "numeric", hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const longDate = (iso: string) => longFmt.format(new Date(iso)).replace(",", " at");
const shortDate = (iso: string) => (new Date(iso).getFullYear() === new Date().getFullYear() ? dateFmt : dateYearFmt).format(new Date(iso));

/** "today at 17:50", "yesterday at 09:12", "3 days ago", "12 Aug". People read last-seen as a time of day when it's recent. */
function seen(iso: string) {
  const d = new Date(iso);
  const now = new Date();
  const ageS = (now.getTime() - d.getTime()) / 1000;
  if (ageS < 120) return "just now";
  if (ageS < 3600) return relative(iso);
  if (dayKey(iso) === dayKey(now.toISOString())) return `today at ${clock(iso)}`;
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (dayKey(iso) === dayKey(y.toISOString())) return `yesterday at ${clock(iso)}`;
  if (ageS < 86400 * 14) return relative(iso);
  return shortDate(iso);
}

const err = (e: unknown) => (e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e));

// ------------------------------------------------------------------ users

export function UsersPage({ project, search = "", page = 1 }: { project: string; search?: string; page?: number }) {
  useTitle(`${project} · Users`);
  const navigate = useNavigate();
  const [settings, setSettings] = useState(false);
  const overview = useQuery({ queryKey: ["auth", project], queryFn: () => mod3.auth(project) });
  const list = useQuery({
    queryKey: ["auth-users", project, search, page],
    queryFn: () => mod3.users(project, search, (page - 1) * PAGE),
    placeholderData: (d) => d,
  });
  const go = (o: { search?: string; page?: number }) =>
    navigate({ to: "/projects/$project/users", params: { project }, search: { search: o.search || undefined, page: o.page && o.page > 1 ? o.page : undefined } });
  const [q, setQ] = useSearchBox(search, (v) => go({ search: v }));
  useShortcut("/", "Search users", () => document.getElementById("user-search")?.focus());
  if (overview.isError && notOnBox(overview.error)) return <NotOnBox what="Sign-in for your apps" />;
  const o = overview.data;
  const st = o?.stats;
  const total = list.data?.total ?? 0;
  const users = list.data?.users ?? [];

  return (
    <Page wide>
      <Header
        project={project}
        title="Auth"
        lede={
          <>
            The people who sign in to {project}’s apps. The box’s own team is under{" "}
            <Link to="/settings/people" className="text-brass-ink underline decoration-brass/40 underline-offset-[3px] hover:decoration-brass">
              Access
            </Link>
            .
          </>
        }
        actions={
          o && (
            <Button onClick={() => setSettings(true)}>
              <Settings2 />
              Sign-in settings
            </Button>
          )
        }
      />
      {overview.isError && <ProblemNote className="mt-8" error={overview.error} title="Couldn’t load sign-in for this project" />}
      {overview.isPending && (
        <div className="mt-8 grid grid-cols-2 gap-6 border-b border-rule pb-7 lg:grid-cols-4" aria-busy>
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-14" />
          ))}
        </div>
      )}
      {st && (
        <Readings className="grid-cols-2 lg:grid-cols-4">
          <Reading label="People" value={int(st.users)} sub={st.bannedUsers ? `${int(st.bannedUsers)} suspended` : st.users ? "None suspended" : "Nobody yet"} />
          <Reading label="New this week" value={int(st.signups7d)} sub={st.users ? `${pct(st.signups7d / st.users)} of everyone` : undefined} />
          <Reading
            label="Verified email"
            value={st.users ? pct(st.verifiedUsers / st.users) : "—"}
            sub={st.users ? `${int(st.verifiedUsers)} of ${int(st.users)}` : undefined}
          />
          <Reading
            label="Signed in now"
            value={int(st.activeSessions)}
            unit={st.activeSessions === 1 ? "session" : "sessions"}
            sub={o.organizations ? countWords(st.organizations, "organization", "organizations", true) : "Organizations are off"}
          />
        </Readings>
      )}
      {o && <MailBanner project={project} o={o} />}
      {o && <MethodsLine o={o} onOpen={() => setSettings(true)} />}

      <div className="mt-8 flex items-center justify-between gap-4">
        <SearchBox value={q} onChange={setQ} label="Search users" placeholder="Search by name or email" />
        <span className="shrink-0 text-[0.8125rem] text-ink-3 tnum max-sm:hidden">{list.isSuccess && (search ? `${int(total)} found` : `${int(total)} in all`)}</span>
      </div>
      {list.isError && <ProblemNote className="mt-4" error={list.error} />}
      <div className="mt-4">
        <div aria-hidden className="label hidden grid-cols-[1.75rem_minmax(0,1fr)_9rem_10rem_6.5rem] gap-x-4 pb-2 sm:grid">
          <span />
          <span>Person</span>
          <span>Account</span>
          <span>Last seen</span>
          <span className="text-right">Joined</span>
        </div>
        <ul className={cn("divide-y divide-rule border-y border-rule-2 transition-opacity", list.isPlaceholderData && "opacity-60")}>
          {list.isPending &&
            [0, 1, 2, 3, 4].map((i) => (
              <li key={i} className="flex items-center gap-4 py-3">
                <Skeleton className="size-7 rounded-full" />
                <Skeleton className="h-4 w-1/3" />
              </li>
            ))}
          {users.map((u) => (
            <UserRow key={u.id} project={project} u={u} />
          ))}
          {list.isSuccess && users.length === 0 && (
            <li className="py-12 text-center">
              <p className="text-md text-ink">{search ? "Nobody matches that." : "No users yet."}</p>
              <p className="mx-auto mt-1 max-w-[32rem] text-[0.875rem] text-ink-3">
                {search ? (
                  "Search looks at names and email addresses."
                ) : o ? (
                  <>
                    When people sign up in your app, they show up here. Your apps reach sign-in at <code className="ident text-ink-2">{o.endpoint}</code>.
                  </>
                ) : (
                  "When people sign up in your app, they show up here."
                )}
              </p>
            </li>
          )}
        </ul>
      </div>
      <Pager page={page} total={total} onPage={(p) => go({ search, page: p })} />
      {o && <AuthSettings project={project} o={o} open={settings} onOpenChange={setSettings} />}
    </Page>
  );
}

/** Email sign-in is on but the box can't send email: production refuses it until a mail service is connected. */
function MailBanner({ project, o }: { project: string; o: AuthOverview }) {
  const on = (o.methods ?? []).filter((m) => m === "email" || m === "magic-link" || m === "otp");
  if (!o.emailBlocked || on.length === 0) return null;
  return (
    <div role="status" className="mt-6 flex flex-col gap-x-4 gap-y-2 rounded-[10px] border border-warn/40 bg-warn-wash px-4 py-3 sm:flex-row sm:items-center">
      <div className="min-w-0 flex-1">
        <p className="text-[0.875rem] font-[550] text-warn-ink">Email sign-in is off in production: this box can’t send email yet.</p>
        <p className="mt-0.5 text-[0.8125rem] text-ink-2">
          Sign-up, sign-in links, codes and password resets answer “This app can’t send email yet” until you connect your mail service. Passkeys and sign-in
          providers work meanwhile; previews keep using the dev inbox.
        </p>
      </div>
      <Link
        to="/projects/$project/email/settings"
        params={{ project }}
        className="shrink-0 text-[0.8125rem] font-[550] text-warn-ink underline decoration-current/40 underline-offset-2 hover:decoration-current"
      >
        Connect a mail service
      </Link>
    </div>
  );
}

/** The sign-in methods that are on, in a line, with what still needs setting up. */
function MethodsLine({ o, onOpen }: { o: AuthOverview; onOpen: () => void }) {
  const methods = o.methods ?? [];
  // Sign-in providers that are on with no keys yet (box-wide or the project's own).
  const missing = methods.filter((m) => m in (o.social ?? {}) && !o.social?.[m]);
  return (
    <div className="mt-6 flex flex-wrap items-center gap-x-2 gap-y-2 text-[0.8125rem]">
      <span className="mr-1 text-ink-3">Sign in with</span>
      {methods.map((m) => {
        const broken = missing.includes(m);
        return (
          <button
            key={m}
            type="button"
            onClick={onOpen}
            title={broken ? `${methodName(m)} needs keys: box-wide ones in Box settings, or this project’s own` : undefined}
            className={cn(
              "inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 transition-colors hover:bg-paper-hover",
              broken ? "border-warn/50 text-warn-ink" : "border-rule-2 text-ink-2",
            )}
          >
            {broken && <span aria-hidden className="size-1.5 rounded-full bg-warn" />}
            {methodName(m)}
            {broken && <span className="sr-only">: needs setting up</span>}
          </button>
        );
      })}
      {o.emailVerification.required && <span className="ml-1 text-ink-3">Email verification is on.</span>}
      {missing.length > 0 && (
        <button type="button" onClick={onOpen} className="ml-1 text-warn-ink underline decoration-current/40 underline-offset-2 hover:decoration-current">
          {missing.length === 1 ? `${methodName(missing[0]!)} needs keys` : `${missing.length} providers need keys`}
        </button>
      )}
    </div>
  );
}

function UserRow({ project, u }: { project: string; u: AuthUser }) {
  return (
    <li>
      <Link
        to="/projects/$project/users/$id"
        params={{ project, id: u.id }}
        className="grid grid-cols-[1.75rem_minmax(0,1fr)_auto] items-center gap-x-4 py-2.5 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover/60 sm:grid-cols-[1.75rem_minmax(0,1fr)_9rem_10rem_6.5rem]"
      >
        <Avatar name={u.name} />
        <span className="min-w-0">
          <span className="flex items-baseline gap-2">
            <span className="truncate text-[0.9375rem] text-ink">{u.name}</span>
            {u.banned && <span className="shrink-0 text-[0.8125rem] font-[550] text-danger sm:hidden">suspended</span>}
          </span>
          <span className="block truncate text-[0.8125rem] text-ink-3">
            {u.email}
            {!u.emailVerified && <span className="sm:hidden"> · not verified</span>}
          </span>
        </span>
        <span className="hidden min-w-0 items-center gap-2 text-[0.8125rem] sm:flex">
          {u.banned ? (
            <span className="font-[550] text-danger">Suspended</span>
          ) : u.emailVerified ? (
            <span className="text-ink-2">Verified</span>
          ) : (
            <span className="text-ink-3">Not verified</span>
          )}
          {u.twoFactorEnabled && (
            <span className="rounded-[4px] border border-rule-2 px-1 text-[0.6875rem] font-[550] tracking-[0.02em] text-ink-2" title="Two-factor codes are on">
              2FA
            </span>
          )}
        </span>
        <span className={cn("text-right text-[0.8125rem] sm:text-left", u.lastSeenAt ? "text-ink-2" : "text-ink-3")}>
          {u.lastSeenAt ? seen(u.lastSeenAt) : "never signed in"}
        </span>
        <time className="hidden text-right text-[0.8125rem] text-ink-3 tnum sm:block" dateTime={u.createdAt} title={full(u.createdAt)}>
          {shortDate(u.createdAt)}
        </time>
      </Link>
    </li>
  );
}

function Pager({ page, total, onPage }: { page: number; total: number; onPage: (p: number) => void }) {
  const pages = Math.max(1, Math.ceil(total / PAGE));
  if (total <= PAGE) return null;
  return (
    <div className="mt-3 flex items-center justify-between text-[0.8125rem] text-ink-3">
      <span className="tnum">
        {int((page - 1) * PAGE + 1)}–{int(Math.min(total, page * PAGE))} of {int(total)}
      </span>
      <span className="flex items-center gap-1">
        <Button size="icon-sm" variant="ghost" disabled={page <= 1} onClick={() => onPage(page - 1)} aria-label="Previous page">
          <ChevronLeft />
        </Button>
        <span className="px-1 tnum">
          {page} / {pages}
        </span>
        <Button size="icon-sm" variant="ghost" disabled={page >= pages} onClick={() => onPage(page + 1)} aria-label="Next page">
          <ChevronRight />
        </Button>
      </span>
    </div>
  );
}

function device(ua: string | null) {
  if (!ua) return "An unknown device";
  const os = /iPhone/.test(ua) ? "iPhone" : /iPad/.test(ua) ? "iPad" : /Android/.test(ua) ? "Android" : /Mac OS X/.test(ua) ? "Mac" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "a device";
  const br = /Edg\//.test(ua) ? "Edge" : /Firefox/.test(ua) ? "Firefox" : /Chrome/.test(ua) ? "Chrome" : /Safari/.test(ua) ? "Safari" : /curl|bun|node/i.test(ua) ? "A script" : "A browser";
  return `${br} on ${os}`;
}

// ------------------------------------------------------------------ one user

export function UserPage({ project, id }: { project: string; id: string }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const d = useQuery({ queryKey: ["auth-user", project, id], queryFn: () => mod3.user(project, id) });
  useTitle(d.data ? d.data.user.name : "User");
  const [banning, setBanning] = useState(false);
  const [reason, setReason] = useState("");
  const [revoking, setRevoking] = useState(false);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["auth-user", project, id] });
    void qc.invalidateQueries({ queryKey: ["auth-users", project] });
    void qc.invalidateQueries({ queryKey: ["auth", project] });
  };
  const unbanNow = async () => {
    await mod3.unban(project, id);
    refresh();
  };
  const ban = useMutation({
    mutationFn: () => mod3.ban(project, id, reason.trim()),
    onSuccess: (r) => {
      setBanning(false);
      setReason("");
      refresh();
      toast({
        title: `Suspended ${d.data?.user.name ?? "them"}.`,
        detail: r.sessionsRevoked ? `${countWords(r.sessionsRevoked, "session")} ended. Undo lets them sign in again.` : "Undo lets them sign in again.",
        action: { label: "Undo", run: unbanNow },
      });
    },
  });
  const unban = useMutation({
    mutationFn: () => mod3.unban(project, id),
    onSuccess: () => {
      refresh();
      toast({ title: `${d.data?.user.name ?? "They"} can sign in again.` });
    },
    onError: (e) => toast({ title: "Couldn’t lift the suspension.", detail: err(e), tone: "danger" }),
  });
  if (d.isPending)
    return (
      <Page wide>
        <Skeleton className="h-12 w-80" />
      </Page>
    );
  if (d.isError)
    return (
      <Page wide>
        <ProblemNote error={d.error} title={d.error instanceof ApiError && d.error.status === 404 ? "There’s no user with that ID" : undefined} />
      </Page>
    );
  const { user: u, sessions, accounts, memberships, apiKeys, passkeys } = d.data;
  const writer = can("apply:reversible");
  const live = [...(sessions ?? [])].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
  const latest = live[0];
  let said: ReactNode;
  if (u.banned)
    said = (
      <>
        Suspended{u.banReason ? `: “${u.banReason}”` : ""}. {u.banExpires ? `Until ${full(u.banExpires)}.` : "They can’t sign in until you lift it."}
      </>
    );
  else
    said = latest ? (
      <>
        Joined {relative(u.createdAt)}; last seen {seen(latest.updatedAt)}, in {device(latest.userAgent).replace(/^A /, "a ")}.
      </>
    ) : (
      <>Joined {relative(u.createdAt)}. Not signed in anywhere right now.</>
    );
  const ways: Array<[string, ReactNode]> = [
    ...(accounts ?? []).map((a): [string, ReactNode] => [
      a.providerId + a.accountId,
      a.providerId === "credential" ? "Email and password" : a.providerId.charAt(0).toUpperCase() + a.providerId.slice(1),
    ]),
    ...(passkeys > 0 ? ([["passkeys", countWords(passkeys, "passkey", "passkeys", true)]] as Array<[string, ReactNode]>) : []),
    ...(u.twoFactorEnabled ? ([["2fa", "Two-factor codes, as a second step"]] as Array<[string, ReactNode]>) : []),
    ...((apiKeys ?? []).length ? ([["keys", `${countWords((apiKeys ?? []).length, "API key", "API keys", true)} for scripts`]] as Array<[string, ReactNode]>) : []),
  ];

  return (
    <Page wide>
      <Header
        project={project}
        tabs={false}
        crumbs={[{ label: "Auth", to: "/projects/$project/users", params: { project } }]}
        title={
          <span className="flex items-center gap-3">
            <Avatar name={u.name} big />
            {u.name}
          </span>
        }
        lede={
          <>
            {u.email}
            <span className="text-ink-3">{u.emailVerified ? " · verified" : " · not verified yet"}</span>
          </>
        }
        actions={
          writer ? (
            <>
              {live.length > 0 && (
                <Button variant="secondary" onClick={() => setRevoking(true)}>
                  Sign out everywhere…
                </Button>
              )}
              {u.banned ? (
                <Button variant="secondary" onClick={() => unban.mutate()} disabled={unban.isPending}>
                  Lift the suspension
                </Button>
              ) : (
                !banning && (
                  <Button variant="danger-quiet" onClick={() => setBanning(true)}>
                    Suspend…
                  </Button>
                )
              )}
            </>
          ) : undefined
        }
      />
      <StateSentence className="mt-6">{said}</StateSentence>
      {banning && (
        <form
          className="mt-5 max-w-[40rem] animate-pop rounded-[10px] border border-danger-rule bg-paper-raised px-4 py-3.5 shadow-raised"
          onSubmit={(e) => {
            e.preventDefault();
            ban.mutate();
          }}
        >
          <p className="text-[0.9375rem] text-ink">Suspend {u.name}?</p>
          <p className="mt-0.5 text-[0.84375rem] text-ink-2">
            They’re signed out of {countWords(live.length, "session")} now and can’t sign in until you lift it. Nothing they made is deleted.
          </p>
          <Input className="mt-3" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Why (only you and your team see this)" autoFocus aria-label="Reason" />
          <div className="mt-3 flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={() => setBanning(false)}>
              Keep them
            </Button>
            <Button type="submit" variant="danger" disabled={ban.isPending}>
              Suspend and sign out
            </Button>
          </div>
          {ban.isError && <ProblemNote className="mt-3" error={ban.error} />}
        </form>
      )}

      <div className="mt-10 grid gap-x-12 gap-y-10 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]">
        <section aria-labelledby="sess">
          <Label id="sess">Signed in on</Label>
          {live.length === 0 ? (
            <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">Nowhere right now.</p>
          ) : (
            <ul className="divide-y divide-rule border-y border-rule">
              {live.map((s, i) => (
                <li key={s.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4 py-2.5">
                  <span className="min-w-0">
                    <span className="block text-[0.9375rem] text-ink">
                      {device(s.userAgent)}
                      {i === 0 && <span className="ml-2 text-[0.8125rem] text-ink-3">most recent</span>}
                    </span>
                    <span className="block truncate text-[0.8125rem] text-ink-3">
                      <span className="font-mono text-[0.75rem]">{s.ipAddress ?? "no address"}</span> · signed in {seen(s.createdAt)} · active {seen(s.updatedAt)}
                    </span>
                  </span>
                  <span className="text-[0.8125rem] text-ink-3" title={full(s.expiresAt)}>
                    ends {relative(s.expiresAt)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>
        <div className="flex flex-col gap-10">
          <section aria-labelledby="how">
            <Label id="how">Signs in with</Label>
            <ul className="divide-y divide-rule border-y border-rule">
              {ways.map(([k, v]) => (
                <li key={k} className="py-2.5 text-[0.9375rem] text-ink">
                  {v}
                </li>
              ))}
              {ways.length === 0 && <li className="py-2.5 text-[0.875rem] text-ink-3">No sign-in method on record.</li>}
            </ul>
          </section>
          <section aria-labelledby="orgs">
            <Label id="orgs">Organizations</Label>
            {(memberships ?? []).length === 0 ? (
              <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">Not in any.</p>
            ) : (
              <ul className="divide-y divide-rule border-y border-rule">
                {(memberships ?? []).map((m) => (
                  <li key={m.organizationId}>
                    <Link
                      to="/projects/$project/orgs/$id"
                      params={{ project, id: m.organizationId }}
                      className="grid grid-cols-[1.75rem_minmax(0,1fr)_auto] items-center gap-x-3 py-2.5 transition-colors hover:bg-paper-hover/60"
                    >
                      <Avatar name={m.name} square />
                      <span className="truncate text-[0.9375rem] text-ink">{m.name}</span>
                      <span className={cn("text-[0.8125rem]", m.role === "owner" ? "text-ink" : "text-ink-3")}>{m.role}</span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>
          <section aria-labelledby="about">
            <Label id="about">Account</Label>
            <dl className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-x-4 border-y border-rule py-1 text-[0.84375rem] [&>dd]:py-1.5 [&>dt]:py-1.5">
              <dt className="text-ink-3">User ID</dt>
              <dd className="flex min-w-0 items-center gap-0.5">
                <code className="truncate font-mono text-[0.75rem] text-ink-2">{u.id}</code>
                <CopyButton value={u.id} label="Copy the user ID" className="-my-1 size-6" />
              </dd>
              <dt className="text-ink-3">Email</dt>
              <dd className="flex min-w-0 items-center gap-0.5">
                <span className="truncate text-ink-2">{u.email}</span>
                <CopyButton value={u.email} label="Copy the email address" className="-my-1 size-6" />
              </dd>
              <dt className="text-ink-3">Verified</dt>
              <dd className={u.emailVerified ? "text-ink-2" : "text-ink-3"}>{u.emailVerified ? "Yes" : "Not yet"}</dd>
              <dt className="text-ink-3">Two-factor</dt>
              <dd className={u.twoFactorEnabled ? "text-ink-2" : "text-ink-3"}>{u.twoFactorEnabled ? "On" : "Off"}</dd>
              <dt className="text-ink-3">Joined</dt>
              <dd className="text-ink-2">{longDate(u.createdAt)}</dd>
              <dt className="text-ink-3">Last change</dt>
              <dd className="text-ink-2" title={full(u.updatedAt)}>
                {relative(u.updatedAt)}
              </dd>
            </dl>
          </section>
        </div>
      </div>
      <Confirm
        open={revoking}
        onClose={() => setRevoking(false)}
        title={`Sign ${u.name} out everywhere?`}
        body={`${countWords(live.length, "session")} end${live.length === 1 ? "s" : ""} now. They can sign in again straight away unless you suspend them.`}
        action="Sign out everywhere"
        tone="normal"
        run={() => mod3.revokeSessions(project, id)}
        done={() => {
          refresh();
          toast({ title: `Signed ${u.name} out everywhere.` });
        }}
      />
    </Page>
  );
}

// ------------------------------------------------------------------ organizations

export function OrgsPage({ project, search = "" }: { project: string; search?: string }) {
  useTitle(`${project} · Organizations`);
  const navigate = useNavigate();
  const list = useQuery({ queryKey: ["auth-orgs", project, search], queryFn: () => mod3.orgs(project, search, 0), placeholderData: (d) => d });
  const [q, setQ] = useSearchBox(search, (v) => navigate({ to: "/projects/$project/orgs", params: { project }, search: { search: v || undefined } }));
  if (list.isError && notOnBox(list.error)) return <NotOnBox what="Sign-in for your apps" />;
  const orgs = list.data?.organizations ?? [];
  return (
    <Page wide>
      <Header project={project} title="Auth" lede="Teams your users create in your app, with their members, roles and open invitations." />
      <div className="mt-8 flex items-center justify-between gap-4">
        <SearchBox value={q} onChange={setQ} label="Search organizations" placeholder="Search by name or slug" />
        <span className="shrink-0 text-[0.8125rem] text-ink-3 tnum max-sm:hidden">{list.isSuccess && `${int(list.data?.total ?? orgs.length)} in all`}</span>
      </div>
      {list.isError && <ProblemNote className="mt-4" error={list.error} />}
      <div className="mt-4">
        <div aria-hidden className="label hidden grid-cols-[1.75rem_minmax(0,1fr)_7rem_7rem_6.5rem] gap-x-4 pb-2 sm:grid">
          <span />
          <span>Organization</span>
          <span className="text-right">Members</span>
          <span className="text-right">Invited</span>
          <span className="text-right">Created</span>
        </div>
        <ul className="divide-y divide-rule border-y border-rule-2">
          {orgs.map((o) => (
            <li key={o.id}>
              <Link
                to="/projects/$project/orgs/$id"
                params={{ project, id: o.id }}
                className="grid grid-cols-[1.75rem_minmax(0,1fr)_auto] items-center gap-x-4 py-2.5 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover/60 sm:grid-cols-[1.75rem_minmax(0,1fr)_7rem_7rem_6.5rem]"
              >
                <Avatar name={o.name} square />
                <span className="min-w-0">
                  <span className="block truncate text-[0.9375rem] text-ink">{o.name}</span>
                  <span className="block truncate font-mono text-[0.75rem] text-ink-3">{o.slug}</span>
                </span>
                <span className="text-right text-[0.875rem] text-ink tnum">
                  {int(o.memberCount ?? 0)}
                  <span className="text-[0.8125rem] text-ink-3 sm:hidden"> {(o.memberCount ?? 0) === 1 ? "member" : "members"}</span>
                </span>
                <span className={cn("hidden text-right text-[0.875rem] tnum sm:block", o.pendingInvitations ? "text-ink" : "text-ink-4")}>{int(o.pendingInvitations ?? 0)}</span>
                <time className="hidden text-right text-[0.8125rem] text-ink-3 tnum sm:block" title={full(o.createdAt)}>
                  {shortDate(o.createdAt)}
                </time>
              </Link>
            </li>
          ))}
          {list.isSuccess && orgs.length === 0 && (
            <li className="py-10 text-center">
              <p className="text-md text-ink">{search ? "Nothing matches that." : "No organizations yet."}</p>
              {!search && <p className="mt-1 text-[0.875rem] text-ink-3">When users create a team in your app, it shows up here.</p>}
            </li>
          )}
        </ul>
      </div>
    </Page>
  );
}

const roleOrder = ["owner", "admin", "member", "viewer"];

export function OrgPage({ project, id }: { project: string; id: string }) {
  const d = useQuery({ queryKey: ["auth-org", project, id], queryFn: () => mod3.org(project, id) });
  useTitle(d.data ? d.data.organization.name : "Organization");
  if (d.isPending)
    return (
      <Page wide>
        <Skeleton className="h-12 w-80" />
      </Page>
    );
  if (d.isError)
    return (
      <Page wide>
        <ProblemNote error={d.error} />
      </Page>
    );
  const { organization: o, members, invitations, inviteLinks } = d.data;
  const sorted = [...(members ?? [])].sort((a, b) => roleOrder.indexOf(a.role) - roleOrder.indexOf(b.role) || a.createdAt.localeCompare(b.createdAt));
  const pending = (invitations ?? []).filter((i) => i.status === "pending");
  const links = (inviteLinks ?? []).filter((l) => !l.revokedAt);
  const owners = sorted.filter((m) => m.role === "owner").map((m) => m.name);
  return (
    <Page wide>
      <Header
        project={project}
        tabs={false}
        crumbs={[{ label: "Organizations", to: "/projects/$project/orgs", params: { project } }]}
        title={
          <span className="flex items-center gap-3">
            <Avatar name={o.name} square big />
            {o.name}
          </span>
        }
        lede={
          <>
            <span className="font-mono text-[0.8125rem]">{o.slug}</span>
            <span className="text-ink-3"> · created {shortDate(o.createdAt)}</span>
          </>
        }
      />
      <StateSentence className="mt-6">
        {countWords(sorted.length, "member", "members", true)}
        {owners.length ? `, owned by ${owners.join(" and ")}` : ""}
        {pending.length ? `; ${countWords(pending.length, "invitation")} waiting for an answer.` : "."}
      </StateSentence>
      <section className="mt-10" aria-labelledby="mem">
        <Label id="mem">Members</Label>
        <ul className="divide-y divide-rule border-y border-rule-2">
          {sorted.map((m) => (
            <li key={m.id}>
              <Link
                to="/projects/$project/users/$id"
                params={{ project, id: m.userId }}
                className="grid grid-cols-[1.75rem_minmax(0,1fr)_auto] items-center gap-x-4 py-2.5 transition-colors hover:bg-paper-hover/60 sm:grid-cols-[1.75rem_minmax(0,1fr)_6rem_8rem]"
              >
                <Avatar name={m.name} />
                <span className="min-w-0">
                  <span className="block truncate text-[0.9375rem] text-ink">{m.name}</span>
                  <span className="block truncate text-[0.8125rem] text-ink-3">{m.email}</span>
                </span>
                <span className={cn("text-right text-[0.8125rem] sm:text-left", m.role === "owner" ? "font-[550] text-ink" : "text-ink-2")}>{m.role}</span>
                <span className="hidden text-right text-[0.8125rem] text-ink-3 sm:block">since {shortDate(m.createdAt)}</span>
              </Link>
            </li>
          ))}
        </ul>
      </section>
      {((invitations ?? []).length > 0 || links.length > 0) && (
        <section className="mt-10" aria-labelledby="inv">
          <Label id="inv">Invited</Label>
          <ul className="divide-y divide-rule border-y border-rule">
            {(invitations ?? []).map((i) => (
              <li key={i.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4 py-2.5">
                <span className="min-w-0">
                  <span className="block truncate text-[0.9375rem] text-ink">{i.email}</span>
                  <span className="block text-[0.8125rem] text-ink-3">
                    as {i.role} · sent {seen(i.createdAt)}
                  </span>
                </span>
                <span className={cn("text-[0.8125rem]", i.status === "pending" ? "text-ink-2" : "text-ink-3")} title={full(i.expiresAt)}>
                  {i.status === "pending" ? `expires ${relative(i.expiresAt)}` : i.status}
                </span>
              </li>
            ))}
            {links.map((l) => (
              <li key={l.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4 py-2.5">
                <span className="text-[0.9375rem] text-ink">
                  An invite link for {l.role}s <span className="text-[0.8125rem] text-ink-3">· used {int(l.uses)} of {l.maxUses ? int(l.maxUses) : "unlimited"}</span>
                </span>
                <span className="text-[0.8125rem] text-ink-2">expires {relative(l.expiresAt)}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </Page>
  );
}
