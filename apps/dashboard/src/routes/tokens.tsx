import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { api, type Change, type CreatedToken, type Tier, type Token } from "@/api/client";
import { q } from "@/api/queries";
import { ActorMark } from "@/components/actor";
import { Confirm } from "@/components/confirm";
import { Command, CopyButton } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { accessCrumbs, Group, Rows } from "@/components/health-kit";
import { Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote, sentence } from "@/components/problem";
import { RiskDots } from "@/components/risk-dots";
import { SignedEntry } from "@/components/signed-entry";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Checkbox, Radio, RadioGroup, Select } from "@/components/ui/choice";
import { Input, Label } from "@/components/ui/input";
import { actorName } from "@/lib/actors";
import { asTier, opCounts } from "@/lib/changes";
import { cn } from "@/lib/cn";
import { countWords, int } from "@/lib/format";
import { clock, dayLabel, expiry, relative, within } from "@/lib/time";

const ladder = [
  { scope: "read", title: "Look", body: "See projects, changes and status. Can't change anything.", tier: "read" as Tier },
  { scope: "plan", title: "Look and plan", body: "Also work out what a change would do, without doing it.", tier: "read" as Tier },
  {
    scope: "apply:reversible",
    title: "Make reversible changes",
    body: "Deploys, env vars, scaling: anything undo can put back.",
    tier: "reversible" as Tier,
  },
  {
    scope: "apply:outbound",
    title: "Also reach outside the box",
    body: "Make a bucket public, send real email. Others will see it.",
    tier: "outbound" as Tier,
  },
  {
    scope: "apply:irreversible",
    title: "Also destroy data",
    body: "Drop buckets and services. Only for something you'd trust with the delete key.",
    tier: "irreversible" as Tier,
  },
];

const ttlOptions = [
  { h: 24, label: "1 day" },
  { h: 24 * 7, label: "7 days" },
  { h: 24 * 30, label: "30 days" },
  { h: 24 * 90, label: "90 days" },
  { h: 24 * 365, label: "1 year" },
  { h: 0, label: "Never (people only)" },
];

/** The most it can do, as a risk tier, so tokens speak the same visual language as changes. */
function power(t: Pick<Token, "scopes">): { label: string; tier: Tier; tokens: boolean } {
  const s = t.scopes ?? [];
  if (s.includes("*")) return { label: "Everything", tier: "irreversible", tokens: true };
  let best = 0;
  ladder.forEach((l, i) => s.includes(l.scope) && (best = Math.max(best, i)));
  return { label: ladder[best].title, tier: ladder[best].tier, tokens: s.includes("tokens") };
}

export function TokensPage({ create }: { create?: boolean }) {
  useTitle("Agents and tokens");
  const tokens = useQuery(q.tokens);
  const changes = useQuery(q.changes());
  const { data: me } = useQuery(q.whoami);
  const navigate = useNavigate();
  const [revoke, setRevoke] = useState<Token | null>(null);
  const setCreate = (o: boolean) => navigate({ to: "/tokens", search: o ? { create: true } : {}, replace: true });

  const all = tokens.data ?? [];
  const names = new Map(all.map((t) => [t.id, t.name]));
  // Dashboard logins are tokens too; they read better as "browsers signed in".
  const isSession = (t: Token) => !!t.person || (t.kind === "human" && t.name === "dashboard session");
  const agents = all.filter((t) => !isSession(t) && t.kind === "agent");
  const people = all.filter((t) => !isSession(t) && t.kind !== "agent");
  const sessions = all.filter(isSession);
  const byToken = new Map<string, Change[]>();
  for (const c of changes.data ?? []) {
    const l = byToken.get(c.actor.id) ?? [];
    l.push(c);
    byToken.set(c.actor.id, l);
  }

  return (
    <Page wide>
      <PageHeader
        eyebrow={accessCrumbs}
        title="Agents and tokens"
        lede="Give every agent its own token with only the power it needs. Its name signs every change it makes, and you can cut it off at any time."
        actions={
          <Button variant="primary" size="lg" onClick={() => setCreate(true)}>
            Create a token
          </Button>
        }
      />
      {tokens.isError && <ProblemNote className="mt-8" error={tokens.error} title="Couldn't load tokens" />}

      <Group label="Agents" id="agents" aside={tokens.data ? (agents.length ? countWords(agents.length, "agent") : undefined) : undefined}>
        {tokens.isPending && <Skeleton className="h-32" />}
        {tokens.isSuccess && agents.length === 0 && (
          <div className="border-y border-rule py-5 text-[0.875rem] text-ink-2">
            No agents yet. Create a token for Claude Code, Codex or any MCP client; it shows up here with everything it has done.
          </div>
        )}
        {agents.length > 0 && (
          <Rows>
            {agents.map((t) => (
              <AgentRow key={t.id} t={t} sponsor={t.sponsor ? names.get(t.sponsor) : undefined} entries={byToken.get(t.id) ?? []} onRevoke={() => setRevoke(t)} />
            ))}
          </Rows>
        )}
      </Group>

      {people.length > 0 && (
        <Group label="People’s tokens" id="people-tokens" aside="for the CLI and scripts">
          <Rows>
            {people.map((t) => {
              const p = power(t);
              const owner = t.kind === "owner";
              const mine = me?.tokenId === t.id;
              return (
                <li key={t.id} className="grid grid-cols-[1.75rem_minmax(0,1fr)_auto] items-center gap-x-3 py-3 sm:grid-cols-[1.75rem_13rem_minmax(0,1fr)_auto] sm:gap-x-4">
                  <ActorMark actor={{ kind: owner ? "owner" : "human", name: t.name, id: t.id }} className="size-6 text-[0.6875rem]" />
                  <span className="min-w-0">
                    <span className="block truncate text-[0.875rem] text-ink">{owner ? "The owner’s key" : actorName(t).name}</span>
                    <span className="block truncate text-xs text-ink-3">{owner ? "made with the box; can do anything" : t.sponsor && names.get(t.sponsor) ? `made by ${actorName({ name: names.get(t.sponsor) }).name}` : "a person’s token"}</span>
                  </span>
                  <span className="col-span-2 col-start-2 row-start-2 mt-1 text-[0.84375rem] text-ink-2 sm:col-span-1 sm:col-start-auto sm:row-start-auto sm:mt-0">
                    <RiskDots tier={p.tier} label={false} className="mr-2 inline-flex align-[1px]" />
                    {p.label}
                    <span className="text-ink-3">
                      {" "}
                      · {t.lastUsedAt ? `used ${relative(t.lastUsedAt)}` : "never used"} · {t.expiresAt ? `expires ${expiry(t.expiresAt)}` : "never expires"}
                      {mine ? " · this is you" : ""}
                    </span>
                  </span>
                  <span className="col-start-3 row-start-1 sm:col-start-auto sm:row-start-auto">
                    {!owner && !mine && (
                      <Button variant="ghost" size="sm" onClick={() => setRevoke(t)} className="hover:text-danger">
                        Revoke…
                      </Button>
                    )}
                  </span>
                </li>
              );
            })}
          </Rows>
        </Group>
      )}

      <p className="mt-4 text-[0.8125rem] text-ink-3">The box keeps only a hash of each secret. If one is lost, revoke it and make another.</p>

      {sessions.length > 0 && <Browsers sessions={sessions} myId={me?.tokenId} onRevoke={setRevoke} />}

      <CreateDialog open={!!create} onOpenChange={setCreate} />
      <RevokeDialog token={revoke} entries={revoke ? (byToken.get(revoke.id)?.length ?? 0) : 0} onClose={() => setRevoke(null)} />
    </Page>
  );
}

/** An agent as a named member: what it may do, where, until when, when it was last seen, and what it signed lately. */
function AgentRow({ t, sponsor, entries, onRevoke }: { t: Token; sponsor?: string; entries: Change[]; onRevoke: () => void }) {
  const p = power(t);
  const who = actorName(t);
  const projects = (t.projects ?? []).includes("*") ? "every project" : (t.projects ?? []).join(", ");
  const soon = t.expiresAt && within(t.expiresAt, 3 * 86400_000);
  return (
    <li className="py-4">
      <div className="grid grid-cols-[1.75rem_minmax(0,1fr)_auto] items-start gap-x-3 sm:gap-x-4">
        <ActorMark actor={{ kind: "agent", name: t.name, id: t.id }} className="mt-0.5 size-6 text-[0.6875rem]" />
        <div className="min-w-0">
          <p className="flex flex-wrap items-baseline gap-x-2">
            <span className="text-[0.9375rem] font-[550] text-graphite">{who.name}</span>
            {who.tag && <span className="ident text-[0.71875rem] text-ink-3">{who.tag}</span>}
            {t.name.toLowerCase() !== who.name.toLowerCase().replace(/ /g, "-") && !who.tag && <span className="ident text-[0.71875rem] text-ink-3">{t.name}</span>}
          </p>
          <p className="mt-1 text-[0.84375rem] text-ink-2">
            <RiskDots tier={p.tier} label={false} className="mr-2 inline-flex align-[1px]" />
            <span>
              {p.label} in {projects}
              {p.tokens ? ", and can make tokens no stronger than its own" : ""}.
            </span>
          </p>
          <p className="mt-0.5 text-[0.8125rem] text-ink-3">
            {t.lastUsedAt ? `Last used ${relative(t.lastUsedAt)}` : "Never used"} ·{" "}
            <span className={soon ? "text-warn-ink" : undefined}>{t.expiresAt ? `expires ${expiry(t.expiresAt)}` : "never expires"}</span>
            {sponsor ? ` · made by ${actorName({ name: sponsor }).name}` : ""}
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={onRevoke} className="hover:text-danger" aria-label={`Revoke ${t.name}`}>
          Revoke…
        </Button>
      </div>
      <div className="mt-2 ml-[2.5rem] sm:ml-[2.75rem]">
        {entries.length === 0 ? (
          <p className="text-[0.8125rem] text-ink-3">Nothing signed yet. Its first change will show here and in the Ledger.</p>
        ) : (
          <div className="divide-y divide-rule border-l border-rule pl-3">
            {entries.slice(0, 2).map((c) => (
              <SignedEntry
                key={c.id}
                className="py-2"
                time={clock(c.at)}
                timeNote={dayLabel(c.at) === "Today" ? undefined : dayLabel(c.at).replace(/,.*$/, "").slice(0, 3)}
                actor={{ kind: "agent", name: c.actor.name ?? t.name, session: c.actor.session }}
                intent={sentence(c.intent)}
                counts={opCounts(c.plan.ops)}
                tier={asTier(c.plan.risk)}
                muted={!!c.undoneBy}
                extra={<span>{c.project}</span>}
                to="/changes/$id"
                params={{ id: c.id }}
              />
            ))}
            {entries.length > 2 && <p className="py-2 text-[0.8125rem] text-ink-3">and {countWords(entries.length - 2, "earlier change", "earlier changes")} in the Ledger.</p>}
          </div>
        )}
      </div>
    </li>
  );
}

/**
 * This browser and the three most recent others; everything older is one
 * line with one guarded action. Sign-ins pile up (every login link is one),
 * and a list of identical rows says nothing (critique #9).
 */
function Browsers({ sessions, myId, onRevoke }: { sessions: Token[]; myId?: string; onRevoke: (t: Token) => void }) {
  const qc = useQueryClient();
  const [asking, setAsking] = useState(false);
  const mine = sessions.find((t) => t.id === myId);
  const others = sessions.filter((t) => t.id !== myId).sort((a, b) => new Date(b.lastUsedAt ?? b.createdAt).getTime() - new Date(a.lastUsedAt ?? a.createdAt).getTime());
  const shown = [...(mine ? [mine] : []), ...others.slice(0, 3)];
  const rest = others.length - Math.min(3, others.length);
  return (
    <Group label="Signed-in browsers" id="sessions" aside={others.length ? countWords(sessions.length, "browser") : undefined}>
      <Rows>
        {shown.map((t) => {
          const me = myId === t.id;
          return (
            <li key={t.id} className="flex items-center gap-3 py-2.5">
              <div className="min-w-0 flex-1 text-[0.875rem]">
                <span className="text-ink">{me ? "This browser" : "Another browser"}</span>
                <span className="text-ink-3">
                  {" "}
                  · signed in {relative(t.createdAt)}
                  {t.lastUsedAt && !me ? ` · last used ${relative(t.lastUsedAt)}` : ""}
                  <span className="hidden sm:inline"> · ends {relative(t.expiresAt ?? t.createdAt)}</span>
                </span>
              </div>
              {me ? (
                <span className="text-[0.8125rem] text-ink-3">you’re here</span>
              ) : (
                <Button variant="ghost" size="sm" onClick={() => onRevoke(t)} className="hover:text-danger">
                  Sign out
                </Button>
              )}
            </li>
          );
        })}
        {others.length > 1 && (
          <li className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2.5 text-[0.875rem]">
            <span className="min-w-0 flex-1 text-ink-3">
              {rest > 0 ? `and ${int(rest)} more, signed in earlier` : "That’s every other browser."}
            </span>
            <Button variant="ghost" size="sm" onClick={() => setAsking(true)} className="hover:text-danger">
              Sign out all other browsers
            </Button>
          </li>
        )}
      </Rows>
      <p className="mt-2 text-[0.8125rem] text-ink-3">Each sign-in link starts one. They end by themselves after 12 hours.</p>
      <Confirm
        open={asking}
        onClose={() => setAsking(false)}
        title={`Sign out ${countWords(others.length, "other browser", "other browsers")}?`}
        body={`Everyone using them is signed out at once and needs a new sign-in link. This browser stays signed in; tokens for agents and scripts are not touched.`}
        action={`Sign out ${int(others.length)}`}
        run={async () => {
          for (const t of others) await api.revokeToken(t.id);
        }}
        done={() => {
          qc.invalidateQueries({ queryKey: ["tokens"] });
          toast({ title: `Signed out ${countWords(others.length, "other browser", "other browsers")}.` });
        }}
      />
    </Group>
  );
}

function CreateDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  // The form lives inside the content, which unmounts on close, so every open starts fresh.
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <CreateForm onClose={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  );
}

function CreateForm({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const { data: projects } = useQuery(q.projects);
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"agent" | "human">("agent");
  const [level, setLevel] = useState(2);
  const [canTokens, setCanTokens] = useState(false);
  const [allProjects, setAllProjects] = useState(true);
  const [picked, setPicked] = useState<string[]>([]);
  const [ttl, setTtl] = useState(24 * 30);
  const [created, setCreated] = useState<CreatedToken | null>(null);

  const m = useMutation({
    mutationFn: () =>
      api.createToken({
        name: name.trim(),
        kind,
        scopes: [...ladder.slice(0, level + 1).map((l) => l.scope), ...(canTokens ? ["tokens"] : [])],
        projects: allProjects ? ["*"] : picked,
        ttlHours: ttl,
      }),
    onSuccess: (r) => {
      setCreated(r);
      qc.invalidateQueries({ queryKey: ["tokens"] });
    },
  });

  const invalid = !name.trim() || (!allProjects && picked.length === 0);

  return created ? (
    <SecretOnce created={created} onDone={onClose} />
  ) : (
    <form
      className="flex min-h-0 flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        if (!invalid) m.mutate();
      }}
    >
      <DialogHeader>
        <DialogTitle>New token</DialogTitle>
        <DialogDescription>Name it after whoever will hold it: the name signs everything it does. You’ll see the secret once.</DialogDescription>
      </DialogHeader>
      <DialogBody className="flex flex-col gap-6">
        <div className="grid gap-4 sm:grid-cols-[1fr_auto]">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="tok-name">Name</Label>
            <Input
              id="tok-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="claude-code"
              maxLength={64}
              autoFocus
              autoComplete="off"
              spellCheck={false}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-sm font-[550] text-ink" id="tok-kind">
              For
            </span>
            <Segmented
              labelledBy="tok-kind"
              value={kind}
              onChange={(v) => {
                setKind(v);
                if (v === "agent" && ttl === 0) setTtl(24 * 30);
              }}
              options={[
                { v: "agent", label: "An agent" },
                { v: "human", label: "A person" },
              ]}
            />
          </div>
        </div>

        <fieldset>
          <legend className="text-sm font-[550] text-ink">What can it do?</legend>
          <div className="mt-2 divide-y divide-rule border-y border-rule" role="radiogroup">
            {ladder.map((l, i) => (
              <label
                key={l.scope}
                className={cn(
                  "flex cursor-pointer gap-3 px-2 py-2.5 transition-colors hover:bg-paper-sunk has-[:focus-visible]:bg-paper-sunk",
                  level === i && "bg-paper-sunk",
                )}
              >
                <input type="radio" name="level" className="sr-only" checked={level === i} onChange={() => setLevel(i)} />
                {/* A ladder: each level includes the ones above it in the list. */}
                <span aria-hidden className="relative flex w-4 shrink-0 justify-center">
                  {i > 0 && <span className={cn("absolute -top-2.5 h-[calc(0.625rem+9px)] w-px", i <= level ? "bg-ink-2" : "bg-rule-2")} />}
                  {i < ladder.length - 1 && <span className={cn("absolute top-[9px] -bottom-2.5 w-px", i < level ? "bg-ink-2" : "bg-rule-2")} />}
                  <span
                    className={cn(
                      "relative mt-[3px] grid place-items-center rounded-full transition-all duration-200",
                      level === i
                        ? "size-3.5 bg-ink ring-4 ring-brass-wash"
                        : i < level
                          ? "mt-[5px] size-2.5 bg-ink-2"
                          : "mt-[5px] size-2.5 border border-rule-3 bg-paper-raised",
                    )}
                  >
                    {level === i && <span className="size-1 rounded-full bg-paper" />}
                  </span>
                </span>
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-2.5 text-base font-[550] text-ink">
                    {l.title}
                    {i >= 2 && <RiskDots tier={l.tier} label={false} />}
                    {i === 2 && <span className="text-xs font-normal text-ink-3">default</span>}
                  </span>
                  <span className="block text-sm text-ink-3">{l.body}</span>
                </span>
              </label>
            ))}
          </div>
          <label className="mt-3 flex cursor-pointer items-start gap-3 px-1">
            <Checkbox checked={canTokens} onCheckedChange={(v) => setCanTokens(v === true)} className="mt-0.5" />
            <span className="text-base text-ink-2">
              Can create and revoke tokens <span className="text-ink-3">(never with more power than its own)</span>
            </span>
          </label>
          {level === 4 && (
            <p className="mt-3 border-l-2 border-danger pl-3 text-sm text-danger">This token can delete data that undo can’t bring back. Most agents never need it.</p>
          )}
        </fieldset>

        <div className="grid gap-6 sm:grid-cols-2">
          <fieldset>
            <legend className="text-sm font-[550] text-ink">Projects</legend>
            <RadioGroup value={allProjects ? "all" : "some"} onValueChange={(v) => setAllProjects(v === "all")} className="mt-2 flex flex-col gap-2">
              <label className="flex cursor-pointer items-center gap-2.5 text-base text-ink-2">
                <Radio value="all" />
                All projects
              </label>
              <label className="flex cursor-pointer items-center gap-2.5 text-base text-ink-2">
                <Radio value="some" />
                Only some
              </label>
            </RadioGroup>
            {!allProjects && (
              <div className="mt-2 ml-6 flex flex-col gap-2">
                {(projects ?? []).map((p) => (
                  <label key={p.name} className="flex cursor-pointer items-center gap-2.5 font-mono text-sm text-ink">
                    <Checkbox
                      checked={picked.includes(p.name)}
                      onCheckedChange={(v) => setPicked((s) => (v === true ? [...s, p.name] : s.filter((x) => x !== p.name)))}
                    />
                    {p.name}
                  </label>
                ))}
              </div>
            )}
          </fieldset>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="tok-ttl">Expires after</Label>
            <Select
              id="tok-ttl"
              value={String(ttl)}
              onValueChange={(v) => setTtl(Number(v))}
              options={ttlOptions.filter((o) => o.h !== 0 || kind === "human").map((o) => ({ value: String(o.h), label: o.label }))}
            />
            <span className="text-sm text-ink-3">{kind === "agent" ? "Agent tokens always expire." : "People's tokens can last forever."}</span>
          </div>
        </div>

        {m.isError && <ProblemNote error={m.error} />}
      </DialogBody>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={invalid || m.isPending}>
          {m.isPending ? "Creating…" : name.trim() ? `Create ${name.trim().slice(0, 24)}` : "Create token"}
        </Button>
      </DialogFooter>
    </form>
  );
}

function SecretOnce({ created, onDone }: { created: CreatedToken; onDone: () => void }) {
  const mcp = `claude mcp add --transport http tiffin ${location.origin}/mcp --header "Authorization: Bearer ${created.secret}"`;
  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {actorName(created.token).name} is ready
        </DialogTitle>
        <DialogDescription>Copy the secret now. This is the only time Tiffin will show it.</DialogDescription>
      </DialogHeader>
      <DialogBody className="flex flex-col gap-5">
        <div className="animate-pop rounded-[10px] border border-brass bg-brass-wash p-1">
          <div className="flex items-center gap-2 rounded-[7px] bg-paper-raised px-3 py-2.5">
            <code data-testid="token-secret" className="min-w-0 flex-1 font-mono text-sm break-all text-ink select-all">
              {created.secret}
            </code>
            <CopyButton value={created.secret} label="Copy secret" />
          </div>
          <p className="px-3 pt-2 pb-1.5 text-sm text-ink-2">
            Put it in your agent's config or a password manager. If it's lost, revoke it and make a new one.
          </p>
        </div>
        {created.token.kind === "agent" && (
          <Section title="Connect Claude Code">
            <Command cmd={mcp} />
          </Section>
        )}
        <Section title="Or use it from a terminal">
          <Command cmd={`export TIFFIN_TOKEN=${created.secret}`} />
        </Section>
      </DialogBody>
      <DialogFooter>
        <Button variant="primary" onClick={onDone}>
          I've stored it
        </Button>
      </DialogFooter>
    </>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div>
      <p className="label mb-2">{title}</p>
      {children}
    </div>
  );
}

function RevokeDialog({ token, entries, onClose }: { token: Token | null; entries: number; onClose: () => void }) {
  return (
    <Dialog open={!!token} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-md">{token && <RevokeBody token={token} entries={entries} onClose={onClose} />}</DialogContent>
    </Dialog>
  );
}

function RevokeBody({ token, entries, onClose }: { token: Token; entries: number; onClose: () => void }) {
  const qc = useQueryClient();
  const session = !!token.person;
  const who = session ? "this browser session" : actorName(token).name;
  const m = useMutation({
    mutationFn: (id: string) => api.revokeToken(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["tokens"] });
      toast({ title: session ? "Signed that browser out." : `Revoked ${who}. Its secret no longer works.` });
      onClose();
    },
  });
  return (
    <>
      <DialogHeader>
        <DialogTitle>{session ? "Sign this browser out?" : `Revoke ${who}?`}</DialogTitle>
        <DialogDescription>
          {session
            ? "Whoever is using it is signed out at once."
            : `Anything using this token stops working at once, and tokens it made are revoked too. ${entries ? `Its ${countWords(entries, "signed change")} stay in the Ledger, signed.` : "Nothing it signed is touched."} This can’t be undone; make a new token instead.`}
        </DialogDescription>
      </DialogHeader>
      {m.isError && (
        <DialogBody>
          <ProblemNote error={m.error} />
        </DialogBody>
      )}
      <DialogFooter>
        <Button variant="ghost" onClick={onClose} autoFocus>
          Keep it
        </Button>
        <Button variant="danger" disabled={m.isPending} onClick={() => m.mutate(token.id)}>
          {m.isPending ? "Revoking…" : session ? "Sign out" : `Revoke ${who}`}
        </Button>
      </DialogFooter>
    </>
  );
}

function Segmented<T extends string>({
  value,
  onChange,
  options,
  labelledBy,
}: {
  value: T;
  onChange: (v: T) => void;
  options: Array<{ v: T; label: string }>;
  labelledBy: string;
}) {
  return (
    <div role="radiogroup" aria-labelledby={labelledBy} className="flex h-9 rounded-[8px] border border-rule-2 bg-paper p-0.5">
      {options.map((o) => (
        <button
          type="button"
          key={o.v}
          role="radio"
          aria-checked={value === o.v}
          onClick={() => onChange(o.v)}
          className={cn(
            "rounded-[6px] px-3 text-base whitespace-nowrap text-ink-3 transition-colors hover:text-ink",
            value === o.v && "bg-paper-sunk font-[550] text-ink",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
