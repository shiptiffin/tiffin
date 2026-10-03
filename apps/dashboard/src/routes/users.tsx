import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  ArrowLeft,
  BadgeCheck,
  Building2,
  ChevronLeft,
  ChevronRight,
  KeyRound,
  LogOut,
  Search,
  ShieldCheck,
  ShieldOff,
  UserRound,
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod3 } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { useTitle } from "@/components/favicon";
import { Empty, NotOnBox, Page, PageHeader, Skeleton, Tabs } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { num } from "@/lib/format";
import { useMe } from "@/lib/me";
import { full, relative } from "@/lib/time";

function Header({ project, title, lede }: { project: string; title: ReactNode; lede?: ReactNode }) {
  return (
    <PageHeader
      eyebrow={
        <Link to="/projects/$project" params={{ project }} className="font-mono hover:text-ink">
          {project}
        </Link>
      }
      title={title}
      lede={lede}
    >
      <Tabs
        items={[
          { to: "/projects/$project/users", params: { project }, label: "Users" },
          { to: "/projects/$project/orgs", params: { project }, label: "Organizations" },
        ]}
      />
    </PageHeader>
  );
}

function Avatar({ name, round = true, className }: { name: string; round?: boolean; className?: string }) {
  const initials = name
    .split(/\s+/)
    .slice(0, 2)
    .map((w) => w[0])
    .join("")
    .toUpperCase();
  return (
    <span
      aria-hidden
      className={cn(
        "grid size-8 shrink-0 place-items-center bg-hover text-xs font-medium text-ink-2",
        round ? "rounded-full" : "rounded-lg",
        className,
      )}
    >
      {initials || "?"}
    </span>
  );
}

function useSearchBox(initial: string, onSearch: (q: string) => void) {
  const [q, setQ] = useState(initial);
  useEffect(() => {
    const t = setTimeout(() => q !== initial && onSearch(q), 250);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q]);
  return [q, setQ] as const;
}

const PAGE = 25;

// ------------------------------------------------------------------ users

export function UsersPage({ project, search = "", page = 1 }: { project: string; search?: string; page?: number }) {
  useTitle(`${project} · Users`);
  const navigate = useNavigate();
  const overview = useQuery({ queryKey: ["auth", project], queryFn: () => mod3.auth(project) });
  const list = useQuery({
    queryKey: ["auth-users", project, search, page],
    queryFn: () => mod3.users(project, search, (page - 1) * PAGE),
    placeholderData: (d) => d,
  });
  const go = (o: { search?: string; page?: number }) =>
    navigate({
      to: "/projects/$project/users",
      params: { project },
      search: { search: o.search || undefined, page: o.page && o.page > 1 ? o.page : undefined },
    });
  const [q, setQ] = useSearchBox(search, (v) => go({ search: v }));
  if (overview.isError && notOnBox(overview.error)) return <NotOnBox what="Sign-in for your apps" />;
  const st = overview.data?.stats;
  const total = list.data?.total ?? 0;
  const users = list.data?.users ?? [];

  return (
    <Page wide>
      <Header
        project={project}
        title="Users"
        lede={
          <>
            The people who sign in to <span className="font-mono text-ink">{project}</span>'s apps. Not to be confused with the box's own{" "}
            <Link to="/settings/people" className="text-brass-ink underline underline-offset-4 hover:text-ink">
              People
            </Link>
            .
          </>
        }
      />
      {st && (
        <dl className="mt-8 grid grid-cols-2 max-sm:[&>*:last-child:nth-child(odd)]:col-span-2 gap-px overflow-hidden rounded-xl border border-rule bg-rule sm:grid-cols-5">
          <Stat label="Users" value={num(st.users)} />
          <Stat label="Signed up, 7 days" value={num(st.signups7d)} />
          <Stat label="Verified" value={st.users ? `${Math.round((st.verifiedUsers / st.users) * 100)}%` : "–"} />
          <Stat label="Active sessions" value={num(st.activeSessions)} />
          <Stat label="Suspended" value={num(st.bannedUsers)} />
        </dl>
      )}
      {overview.data && (
        <p className="mt-3 text-sm text-ink-3">
          Sign in with {(overview.data.methods ?? []).join(", ")} · endpoint <code className="font-mono text-xs">{overview.data.endpoint}</code>
        </p>
      )}

      <div className="mt-8 flex items-center gap-2 rounded-lg border border-rule bg-paper px-3 focus-within:border-brass">
        <Search className="size-4 text-ink-4" />
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search by name or email"
          aria-label="Search users"
          className="h-10 flex-1 bg-transparent text-base text-ink outline-none placeholder:text-ink-4"
        />
      </div>
      <div className="mt-3 overflow-hidden rounded-xl border border-rule bg-raised/60">
        {list.isPending && <Skeleton className="m-4 h-40" />}
        {list.isError && <ProblemNote className="m-4" error={list.error} />}
        <ul className="divide-y divide-rule/70">
          {users.map((u) => (
            <li key={u.id}>
              <Link
                to="/projects/$project/users/$id"
                params={{ project, id: u.id }}
                className="grid grid-cols-[2rem_minmax(0,1fr)_auto] items-center gap-x-3 px-4 py-3 hover:bg-hover/50 sm:grid-cols-[2rem_minmax(0,1fr)_9rem_8rem]"
              >
                <Avatar name={u.name} />
                <span className="min-w-0">
                  <span className="flex items-center gap-1.5">
                    <span className="truncate text-base font-medium text-ink">{u.name}</span>
                    {u.emailVerified && <BadgeCheck className="size-3.5 shrink-0 text-ink-3" aria-label="email verified" />}
                    {u.banned && <span className="rounded-full bg-irr-wash px-1.5 py-px text-xs text-irr">suspended</span>}
                  </span>
                  <span className="block truncate text-sm text-ink-3">{u.email}</span>
                </span>
                <span className="hidden text-sm text-ink-3 sm:block">{u.lastSeenAt ? `seen ${relative(u.lastSeenAt)}` : "never signed in"}</span>
                <span className="text-right text-sm text-ink-3" title={full(u.createdAt)}>
                  joined {relative(u.createdAt)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
        {list.isSuccess && users.length === 0 && (
          <Empty className="m-4 border-0" icon={<UserRound />} title={search ? "Nobody matches that" : "No users yet"}>
            {!search && "When people sign up in your app, they show up here."}
          </Empty>
        )}
      </div>
      <Pager page={page} total={total} onPage={(p) => go({ search, page: p })} />
    </Page>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="bg-raised px-4 py-3.5">
      <dt className="text-2xs font-medium tracking-wider text-ink-3 uppercase">{label}</dt>
      <dd className="display mt-1 text-2xl text-ink tnum">{value}</dd>
    </div>
  );
}

function Pager({ page, total, onPage }: { page: number; total: number; onPage: (p: number) => void }) {
  const pages = Math.max(1, Math.ceil(total / PAGE));
  if (total <= PAGE) return <p className="mt-3 text-sm text-ink-3">{num(total)} in all</p>;
  return (
    <div className="mt-3 flex items-center justify-between text-sm text-ink-3">
      <span>{num(total)} in all</span>
      <span className="flex items-center gap-1">
        <Button size="icon-sm" variant="ghost" disabled={page <= 1} onClick={() => onPage(page - 1)} aria-label="Previous page">
          <ChevronLeft />
        </Button>
        <span className="font-mono text-xs tnum">
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
  if (!ua) return "Unknown device";
  const os = /iPhone/.test(ua)
    ? "iPhone"
    : /Android/.test(ua)
      ? "Android"
      : /Mac OS X/.test(ua)
        ? "Mac"
        : /Windows/.test(ua)
          ? "Windows"
          : /Linux/.test(ua)
            ? "Linux"
            : "Device";
  const br = /Firefox/.test(ua) ? "Firefox" : /Chrome/.test(ua) ? "Chrome" : /Safari/.test(ua) ? "Safari" : "browser";
  return `${br} on ${os}`;
}

export function UserPage({ project, id }: { project: string; id: string }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const d = useQuery({ queryKey: ["auth-user", project, id], queryFn: () => mod3.user(project, id) });
  useTitle(d.data ? d.data.user.name : "User");
  const [banning, setBanning] = useState(false);
  const [reason, setReason] = useState("");
  const [revoking, setRevoking] = useState(false);
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["auth-user", project, id] });
    qc.invalidateQueries({ queryKey: ["auth-users", project] });
    qc.invalidateQueries({ queryKey: ["auth", project] });
  };
  const ban = useMutation({ mutationFn: () => mod3.ban(project, id, reason.trim()), onSuccess: () => (setBanning(false), refresh()) });
  const unban = useMutation({ mutationFn: () => mod3.unban(project, id), onSuccess: refresh });
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
  const { user: u, sessions, accounts, memberships, apiKeys, passkeys } = d.data;
  const writer = can("apply:reversible");
  return (
    <Page wide>
      <Link to="/projects/$project/users" params={{ project }} className="inline-flex items-center gap-1.5 text-sm text-ink-3 hover:text-ink">
        <ArrowLeft className="size-3.5" /> Users
      </Link>
      <header className="mt-5 flex flex-col gap-5 sm:flex-row sm:items-center">
        <Avatar name={u.name} className="size-14 text-lg" />
        <div className="min-w-0 flex-1">
          <h1 className="display text-3xl text-ink">{u.name}</h1>
          <p className="mt-1 flex flex-wrap items-center gap-x-2 text-sm text-ink-3">
            {u.email}
            {u.emailVerified ? (
              <span className="inline-flex items-center gap-1 text-ink-2">
                <BadgeCheck className="size-3.5" /> verified
              </span>
            ) : (
              <span>not verified</span>
            )}
            {u.twoFactorEnabled && (
              <span className="inline-flex items-center gap-1 text-ink-2">
                <ShieldCheck className="size-3.5" /> two-factor
              </span>
            )}
            <span>· joined {relative(u.createdAt)}</span>
          </p>
        </div>
        {writer && (
          <div className="flex flex-wrap gap-2">
            {(sessions ?? []).length > 0 && (
              <Button variant="ghost" onClick={() => setRevoking(true)}>
                <LogOut />
                Sign out everywhere
              </Button>
            )}
            {u.banned ? (
              <Button onClick={() => unban.mutate()} disabled={unban.isPending}>
                <ShieldCheck />
                Lift suspension
              </Button>
            ) : (
              <Button variant="danger-quiet" onClick={() => setBanning(true)}>
                <ShieldOff />
                Suspend…
              </Button>
            )}
          </div>
        )}
      </header>
      {u.banned && (
        <div className="mt-6 rounded-xl border border-irr-rule bg-irr-wash px-5 py-3.5 text-base text-ink">
          <span className="font-medium">Suspended.</span> {u.banReason ? `“${u.banReason}”` : "No reason given."}
          {u.banExpires ? ` Until ${full(u.banExpires)}.` : " Until lifted."} They can't sign in, and their sessions were ended.
        </div>
      )}
      {banning && (
        <form
          className="mt-6 flex animate-pop flex-col gap-2 rounded-xl border border-irr-rule bg-raised p-4 sm:flex-row"
          onSubmit={(e) => {
            e.preventDefault();
            ban.mutate();
          }}
        >
          <Input
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="Why (only you and your team see this)"
            autoFocus
            aria-label="Reason"
          />
          <Button type="submit" variant="danger" disabled={ban.isPending}>
            Suspend and sign out
          </Button>
          <Button type="button" variant="ghost" onClick={() => setBanning(false)}>
            Cancel
          </Button>
        </form>
      )}
      {(ban.isError || unban.isError) && <ProblemNote className="mt-4" error={ban.error ?? unban.error} />}

      <div className="mt-10 grid gap-8 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]">
        <section aria-labelledby="sess">
          <h2 id="sess" className="display-italic mb-3 text-xl text-ink">
            Signed in on
          </h2>
          {(sessions ?? []).length === 0 ? (
            <p className="text-base text-ink-3">No active sessions.</p>
          ) : (
            <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
              {(sessions ?? []).map((s) => (
                <li key={s.id} className="flex items-center gap-3 px-4 py-3">
                  <span className="size-2 rounded-full bg-rev" />
                  <span className="min-w-0 flex-1">
                    <span className="block text-base text-ink">{device(s.userAgent)}</span>
                    <span className="block text-xs text-ink-3">
                      {s.ipAddress ?? "unknown IP"} · signed in {relative(s.createdAt)} · active {relative(s.updatedAt)}
                    </span>
                  </span>
                  <span className="text-xs text-ink-3">ends {relative(s.expiresAt)}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
        <div className="flex flex-col gap-8">
          <section aria-labelledby="how">
            <h2 id="how" className="display-italic mb-3 text-xl text-ink">
              Signs in with
            </h2>
            <ul className="flex flex-wrap gap-2">
              {(accounts ?? []).map((a) => (
                <li key={a.providerId + a.accountId} className="rounded-lg border border-rule bg-raised/60 px-3 py-1.5 text-sm text-ink">
                  {a.providerId === "credential" ? "Email and password" : a.providerId}
                </li>
              ))}
              {passkeys > 0 && (
                <li className="inline-flex items-center gap-1.5 rounded-lg border border-rule bg-raised/60 px-3 py-1.5 text-sm text-ink">
                  <KeyRound className="size-3.5" /> {passkeys} passkey{passkeys === 1 ? "" : "s"}
                </li>
              )}
              {(apiKeys ?? []).length > 0 && (
                <li className="rounded-lg border border-rule bg-raised/60 px-3 py-1.5 text-sm text-ink">{(apiKeys ?? []).length} API keys</li>
              )}
            </ul>
          </section>
          <section aria-labelledby="orgs">
            <h2 id="orgs" className="display-italic mb-3 text-xl text-ink">
              Organizations
            </h2>
            {(memberships ?? []).length === 0 ? (
              <p className="text-base text-ink-3">None.</p>
            ) : (
              <ul className="flex flex-col gap-2">
                {(memberships ?? []).map((m) => (
                  <li key={m.organizationId}>
                    <Link
                      to="/projects/$project/orgs/$id"
                      params={{ project, id: m.organizationId }}
                      className="flex items-center gap-3 rounded-lg border border-rule bg-raised/60 px-3 py-2 hover:border-rule-strong"
                    >
                      <Avatar name={m.name} round={false} className="size-7" />
                      <span className="flex-1 text-base text-ink">{m.name}</span>
                      <span className="text-sm text-ink-3">{m.role}</span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </div>
      </div>
      <Confirm
        open={revoking}
        onClose={() => setRevoking(false)}
        title={`Sign ${u.name} out everywhere?`}
        body="Every session ends now. They can sign in again unless suspended."
        action="Sign out everywhere"
        tone="normal"
        run={() => mod3.revokeSessions(project, id)}
        done={refresh}
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
      <Header project={project} title="Organizations" lede="Teams your users create in your app, with their members, roles and open invitations." />
      <div className="mt-8 flex items-center gap-2 rounded-lg border border-rule bg-paper px-3 focus-within:border-brass">
        <Search className="size-4 text-ink-4" />
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search by name or slug"
          aria-label="Search organizations"
          className="h-10 flex-1 bg-transparent text-base text-ink outline-none placeholder:text-ink-4"
        />
      </div>
      {list.isError && <ProblemNote className="mt-4" error={list.error} />}
      <ul className="mt-3 grid gap-3 sm:grid-cols-2">
        {orgs.map((o) => (
          <li key={o.id}>
            <Link
              to="/projects/$project/orgs/$id"
              params={{ project, id: o.id }}
              className="flex items-center gap-3 rounded-xl border border-rule bg-raised/60 px-4 py-3.5 transition-colors hover:border-rule-strong hover:bg-raised"
            >
              <Avatar name={o.name} round={false} className="size-10" />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-md font-medium text-ink">{o.name}</span>
                <span className="block truncate text-sm text-ink-3">
                  <span className="font-mono text-xs">{o.slug}</span> · {num(o.memberCount ?? 0)} members
                  {o.pendingInvitations ? ` · ${o.pendingInvitations} invited` : ""}
                </span>
              </span>
              <ChevronRight className="size-4 text-ink-4" />
            </Link>
          </li>
        ))}
      </ul>
      {list.isSuccess && orgs.length === 0 && (
        <Empty className="mt-3" icon={<Building2 />} title={search ? "Nothing matches that" : "No organizations yet"}>
          {!search && "When users create a team in your app, it shows up here."}
        </Empty>
      )}
    </Page>
  );
}

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
  const roleOrder = ["owner", "admin", "member", "viewer"];
  const sorted = [...(members ?? [])].sort((a, b) => roleOrder.indexOf(a.role) - roleOrder.indexOf(b.role));
  return (
    <Page wide>
      <Link to="/projects/$project/orgs" params={{ project }} className="inline-flex items-center gap-1.5 text-sm text-ink-3 hover:text-ink">
        <ArrowLeft className="size-3.5" /> Organizations
      </Link>
      <header className="mt-5 flex items-center gap-4">
        <Avatar name={o.name} round={false} className="size-14 text-lg" />
        <div>
          <h1 className="display text-3xl text-ink">{o.name}</h1>
          <p className="mt-1 text-sm text-ink-3">
            <span className="font-mono">{o.slug}</span> · created {relative(o.createdAt)}
          </p>
        </div>
      </header>
      <section className="mt-10" aria-labelledby="mem">
        <h2 id="mem" className="display-italic mb-3 text-xl text-ink">
          Members
        </h2>
        <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
          {sorted.map((m) => (
            <li key={m.id}>
              <Link
                to="/projects/$project/users/$id"
                params={{ project, id: m.userId }}
                className="flex items-center gap-3 px-4 py-3 hover:bg-hover/50"
              >
                <Avatar name={m.name} />
                <span className="min-w-0 flex-1">
                  <span className="block text-base text-ink">{m.name}</span>
                  <span className="block truncate text-sm text-ink-3">{m.email}</span>
                </span>
                <span
                  className={cn("rounded-full px-2.5 py-0.5 text-xs", m.role === "owner" ? "bg-brass-wash text-brass-ink" : "bg-hover text-ink-2")}
                >
                  {m.role}
                </span>
                <span className="hidden w-28 text-right text-xs text-ink-3 sm:block">since {relative(m.createdAt)}</span>
              </Link>
            </li>
          ))}
        </ul>
      </section>
      {((invitations ?? []).length > 0 || (inviteLinks ?? []).length > 0) && (
        <section className="mt-10" aria-labelledby="inv">
          <h2 id="inv" className="display-italic mb-3 text-xl text-ink">
            Invited
          </h2>
          <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
            {(invitations ?? []).map((i) => (
              <li key={i.id} className="flex items-center gap-3 px-4 py-3">
                <span className="min-w-0 flex-1">
                  <span className="block text-base text-ink">{i.email}</span>
                  <span className="block text-xs text-ink-3">
                    as {i.role} · {i.status} · sent {relative(i.createdAt)} · expires {relative(i.expiresAt)}
                  </span>
                </span>
              </li>
            ))}
            {(inviteLinks ?? []).map((l) => (
              <li key={l.id} className="flex items-center gap-3 px-4 py-3 text-sm text-ink-2">
                Invite link for {l.role}s · used {l.uses} of {l.maxUses || "∞"} · {l.revokedAt ? "revoked" : `expires ${relative(l.expiresAt)}`}
              </li>
            ))}
          </ul>
        </section>
      )}
    </Page>
  );
}
