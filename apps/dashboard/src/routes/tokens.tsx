import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { KeyRound, Monitor, Plus, ShieldAlert, Trash2 } from "lucide-react";
import { useState, type ReactNode } from "react";
import { api, type CreatedToken, type Tier, type Token } from "@/api/client";
import { q } from "@/api/queries";
import { ActorMark } from "@/components/actor";
import { Command, CopyButton } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { ProblemNote } from "@/components/problem";
import { RiskMark } from "@/components/risk";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Checkbox, Radio, RadioGroup, Select } from "@/components/ui/choice";
import { Input, Label } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { expiry, relative } from "@/lib/time";
import { Page } from "./activity";

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
  useTitle("Tokens");
  const tokens = useQuery(q.tokens);
  const { data: me } = useQuery(q.whoami);
  const navigate = useNavigate();
  const [revoke, setRevoke] = useState<Token | null>(null);
  const setCreate = (o: boolean) => navigate({ to: "/tokens", search: o ? { create: true } : {}, replace: true });

  const all = tokens.data ?? [];
  const names = new Map(all.map((t) => [t.id, t.name]));
  // Dashboard logins are tokens too; they read better as "browsers signed in".
  const isSession = (t: Token) => t.kind === "human" && t.name === "dashboard session";
  const list = all.filter((t) => !isSession(t));
  const sessions = all.filter(isSession);

  return (
    <Page wide>
      <header className="flex flex-col gap-5 sm:flex-row sm:items-end sm:justify-between">
        <div className="animate-rise">
          <h1 className="display text-3xl text-ink">Tokens</h1>
          <p className="mt-2 max-w-[34rem] text-md text-ink-2">
            Give every agent its own token, with only the power it needs. Its name shows up on every change it makes, and you can cut it off at any
            time.
          </p>
        </div>
        <Button variant="primary" size="lg" onClick={() => setCreate(true)} className="self-start sm:self-auto">
          <Plus />
          Create token
        </Button>
      </header>

      {tokens.isError && <ProblemNote className="mt-8" error={tokens.error} title="Couldn't load tokens" />}

      <div className="mt-10 overflow-hidden rounded-xl border border-rule bg-raised/60">
        <div className="hidden grid-cols-[minmax(12rem,1.3fr)_1.5fr_1fr_0.8fr_2.5rem] gap-4 border-b border-rule px-5 py-2.5 text-2xs font-medium tracking-wider text-ink-3 uppercase md:grid">
          <span>Name</span>
          <span>Can</span>
          <span>Projects</span>
          <span>Expires</span>
          <span />
        </div>
        {tokens.isPending && <div className="h-40 animate-pulse bg-hover/40" />}
        <ul className="divide-y divide-rule">
          {list.map((t, k) => {
            const p = power(t);
            const undeletable = t.kind === "owner" || me?.tokenId === t.id;
            const projects = (t.projects ?? []).includes("*") ? "All projects" : (t.projects ?? []).join(", ");
            return (
              <li
                key={t.id}
                className="grid animate-rise grid-cols-[1fr_auto] items-center gap-x-4 gap-y-2.5 px-4 py-4 sm:px-5 md:grid-cols-[minmax(12rem,1.3fr)_1.5fr_1fr_0.8fr_2.5rem]"
                style={{ animationDelay: `${k * 30}ms` }}
              >
                <div className="flex min-w-0 items-center gap-3">
                  <ActorMark actor={{ kind: t.kind, name: t.name, id: t.id }} className="size-7 text-xs" />
                  <div className="min-w-0">
                    <p className="truncate text-md font-medium text-ink">{t.name}</p>
                    <p className="truncate text-sm text-ink-3">
                      {t.kind}
                      {t.sponsor && names.get(t.sponsor) ? ` · by ${names.get(t.sponsor)}` : ""}
                      {" · "}
                      {t.lastUsedAt ? `used ${relative(t.lastUsedAt)}` : "never used"}
                    </p>
                  </div>
                </div>
                <div className="col-span-2 flex flex-wrap items-center gap-x-2 gap-y-1 pl-10 text-base text-ink-2 md:col-span-1 md:pl-0">
                  <RiskMark tier={p.tier} />
                  <span>{p.label}</span>
                  {p.tokens && t.kind !== "owner" && <span className="rounded-full bg-hover px-2 py-px text-xs text-ink-2">+ tokens</span>}
                  <span className="text-sm text-ink-3 md:hidden">
                    · {projects} · {t.expiresAt ? `expires ${expiry(t.expiresAt)}` : "never expires"}
                  </span>
                </div>
                <div className={cn("hidden truncate text-sm md:block", projects === "All projects" ? "text-ink-3" : "font-mono text-ink-2")}>
                  {projects}
                </div>
                <div className={cn("hidden text-sm md:block", t.expiresAt ? "text-ink-2" : "text-ink-3")}>{expiry(t.expiresAt)}</div>
                <div className="col-start-2 row-start-1 justify-self-end md:col-auto md:row-auto">
                  {!undeletable && (
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Revoke ${t.name}`}
                      title="Revoke"
                      onClick={() => setRevoke(t)}
                      className="hover:text-irr"
                    >
                      <Trash2 />
                    </Button>
                  )}
                </div>
              </li>
            );
          })}
        </ul>
      </div>
      <p className="mt-4 text-sm text-ink-3">Secrets are never stored, only a hash. If one is lost, revoke it and make another.</p>

      {sessions.length > 0 && (
        <section className="mt-14" aria-labelledby="sessions">
          <h2 id="sessions" className="display-italic text-xl text-ink">
            Signed-in browsers
          </h2>
          <p className="mt-1 text-base text-ink-3">Each login link starts one. They end on their own after 12 hours.</p>
          <ul className="mt-4 divide-y divide-rule border-y border-rule">
            {sessions.map((t) => {
              const mine = me?.tokenId === t.id;
              return (
                <li key={t.id} className="flex items-center gap-3 py-3">
                  <Monitor className="size-4 shrink-0 text-ink-3" />
                  <div className="min-w-0 flex-1 text-base">
                    <span className="text-ink">{mine ? "This browser" : "Another browser"}</span>
                    <span className="text-ink-3">
                      {" "}
                      · signed in {relative(t.createdAt)} · ends {relative(t.expiresAt ?? t.createdAt)}
                    </span>
                  </div>
                  {mine ? (
                    <span className="rounded-full bg-rev-wash px-2 py-0.5 text-xs font-medium text-rev">You're here</span>
                  ) : (
                    <Button variant="ghost" size="sm" onClick={() => setRevoke(t)} className="hover:text-irr">
                      Sign out
                    </Button>
                  )}
                </li>
              );
            })}
          </ul>
        </section>
      )}

      <CreateDialog open={!!create} onOpenChange={setCreate} />
      <RevokeDialog token={revoke} onClose={() => setRevoke(null)} />
    </Page>
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
        <DialogDescription>Name it after whoever will hold it. You'll see the secret once.</DialogDescription>
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
            <span className="text-sm font-medium text-ink" id="tok-kind">
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
          <legend className="text-sm font-medium text-ink">What can it do?</legend>
          <div className="mt-2 overflow-hidden rounded-lg border border-rule" role="radiogroup">
            {ladder.map((l, i) => (
              <label
                key={l.scope}
                className={cn(
                  "flex cursor-pointer gap-3 border-b border-rule px-3.5 py-2.5 transition-colors last:border-b-0 hover:bg-hover/60 has-[:focus-visible]:bg-hover",
                  level === i && "bg-hover",
                )}
              >
                <input type="radio" name="level" className="sr-only" checked={level === i} onChange={() => setLevel(i)} />
                {/* A ladder: each level includes the ones above it in the list. */}
                <span aria-hidden className="relative flex w-4 shrink-0 justify-center">
                  {i > 0 && <span className={cn("absolute -top-2.5 h-[calc(0.625rem+9px)] w-px", i <= level ? "bg-ink-2" : "bg-rule-strong")} />}
                  {i < ladder.length - 1 && <span className={cn("absolute top-[9px] -bottom-2.5 w-px", i < level ? "bg-ink-2" : "bg-rule-strong")} />}
                  <span
                    className={cn(
                      "relative mt-[3px] grid place-items-center rounded-full transition-all duration-200",
                      level === i
                        ? "size-3.5 bg-ink ring-4 ring-hover"
                        : i < level
                          ? "mt-[5px] size-2.5 bg-ink-2"
                          : "mt-[5px] size-2.5 border border-rule-strong bg-raised",
                    )}
                  >
                    {level === i && <span className="size-1 rounded-full bg-on-ink" />}
                  </span>
                </span>
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-2 text-base font-medium text-ink">
                    {l.title}
                    {i >= 2 && <RiskMark tier={l.tier} />}
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
            <p className="mt-3 flex animate-pop gap-2 rounded-lg bg-irr-wash px-3 py-2 text-sm text-ink">
              <ShieldAlert className="mt-0.5 size-4 shrink-0 text-irr" />
              This token can delete data that undo can't bring back. Most agents never need it.
            </p>
          )}
        </fieldset>

        <div className="grid gap-6 sm:grid-cols-2">
          <fieldset>
            <legend className="text-sm font-medium text-ink">Projects</legend>
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
          <KeyRound />
          {m.isPending ? "Creating…" : "Create token"}
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
          <span className="display-italic">{created.token.name}</span> is ready
        </DialogTitle>
        <DialogDescription>Copy the secret now. This is the only time Tiffin will show it.</DialogDescription>
      </DialogHeader>
      <DialogBody className="flex flex-col gap-5">
        <div className="animate-pop rounded-lg border border-brass/50 bg-brass-wash p-1">
          <div className="flex items-center gap-2 rounded-md bg-raised px-3 py-2.5">
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
      <p className="mb-2 text-sm font-medium text-ink-2">{title}</p>
      {children}
    </div>
  );
}

function RevokeDialog({ token, onClose }: { token: Token | null; onClose: () => void }) {
  return (
    <Dialog open={!!token} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-md">{token && <RevokeBody token={token} onClose={onClose} />}</DialogContent>
    </Dialog>
  );
}

function RevokeBody({ token, onClose }: { token: Token; onClose: () => void }) {
  const qc = useQueryClient();
  const m = useMutation({
    mutationFn: (id: string) => api.revokeToken(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["tokens"] });
      onClose();
    },
  });
  return (
    <>
      <DialogHeader>
        <DialogTitle>Revoke {token.name}?</DialogTitle>
        <DialogDescription>
          Anything using this token stops working right away. Tokens it created are revoked too. Its changes stay in the log.
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
          <Trash2 />
          {m.isPending ? "Revoking…" : "Revoke token"}
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
    <div role="radiogroup" aria-labelledby={labelledBy} className="flex h-9 rounded-md border border-rule bg-paper p-0.5">
      {options.map((o) => (
        <button
          type="button"
          key={o.v}
          role="radio"
          aria-checked={value === o.v}
          onClick={() => onChange(o.v)}
          className={cn(
            "rounded-[5px] px-3 text-base whitespace-nowrap text-ink-3 transition-colors hover:text-ink",
            value === o.v && "bg-raised text-ink shadow-[0_1px_2px_oklch(0_0_0/0.1)] ring-1 ring-rule",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
