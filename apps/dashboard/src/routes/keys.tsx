import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { api, isProblem, type CreatedToken, type Token } from "@/api/client";
import { q } from "@/api/queries";
import { Confirm } from "@/components/confirm";
import { ConfirmItsYou } from "@/components/confirm-its-you";
import { AgentCommand } from "@/components/agent-command";
import { CopyButton } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { RadioGroup, RadioItem } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { actorName } from "@/lib/actors";
import { cn } from "@/lib/cn";
import { onThisComputer } from "@/lib/mcp";
import { relative } from "@/lib/time";
import { ProjectIcon } from "@/components/project-icon";

/**
 * API keys: how Claude Code, other agents and scripts reach this box. Each
 * key says which projects it reaches (all, including future ones, or only
 * some), whether it can change things or only read, and when it expires.
 * Whatever a key does shows up in History under its name. Only an admin
 * (or a key with full access to all projects) manages keys.
 *
 * A key is its own credential, like a GitHub or Vercel personal access
 * token: signing out doesn't touch it. So a key that lasts more than a day,
 * or has full access, needs a recent strong sign-in ("sudo mode"): when the
 * box answers reauth_required, the dialog asks the person to confirm with a
 * passkey or to sign in again, then makes the key.
 */

export const keyProjects = (t: Token): string[] | "all" => (t.projects === "all" || !t.projects?.length ? "all" : t.projects);
/** Live keys only (not revoked). */
export const onlyKeys = (all: Token[]) => all.filter((t) => !t.revokedAt);

/** How long a key works: the box's choices, 0 is never. */
type Days = 30 | 90 | 365 | 0;
type NewKey = { name: string; projects: "all" | string[]; access: "full" | "read"; expiresInDays: Days };
const createKey = (k: NewKey) => api.createToken(k);
/** Where "Sign in again" comes back to: this dialog, open. */
const backHere = "/settings/keys?create=true";

export function KeysPage({ create }: { create?: boolean }) {
  useTitle("API keys");
  const tokens = useQuery(q.tokens);
  const navigate = useNavigate();
  const setCreate = (o: boolean) => navigate({ to: "/settings/keys", search: o ? { create: true } : {}, replace: true });
  const all = tokens.data ?? [];
  const keys = onlyKeys(all);

  return (
    <Page>
      <PageHeader
        title="API keys"
        lede="Keys let Claude Code, other agents and your scripts work on this box. Whatever a key does shows up in History under its name, and you can revoke it any time."
        actions={
          <Button variant="primary" size="lg" onClick={() => setCreate(true)}>
            Create key
          </Button>
        }
      />
      {tokens.isError && <ProblemNote className="mt-8" error={tokens.error} title="Couldn’t load the keys." />}

      <section className="mt-9" aria-label="Keys">
        {tokens.isPending ? <Skeleton className="h-32" /> : <KeyList keys={keys} empty="No keys yet. Create one for Claude Code or Codex and paste the line it gives you." />}
      </section>

      <section className="mt-10" aria-label="Connect your agent">
        <h2 className="text-[0.9375rem] font-[550] text-ink">Connect your agent</h2>
        <p className="mt-1 mb-3 text-sm text-ink-2">Run this once in a terminal with your key. Creating a key gives you the line ready to paste.</p>
        <AgentCommand secret="<key>" />
      </section>


      <CreateKeyDialog open={!!create} onOpenChange={setCreate} />
    </Page>
  );
}

/** Keys as rows: name, which projects, full or read only, expiry, last used, Revoke. */
export function KeyList({ keys, empty }: { keys: Token[]; empty: string }) {
  const [revoke, setRevoke] = useState<Token | null>(null);
  const qc = useQueryClient();
  if (keys.length === 0) return <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">{empty}</p>;
  return (
    <>
      <ul className="divide-y divide-rule border-y border-rule">
        {keys.map((t) => {
          const p = keyProjects(t);
          const access = t.access;
          return (
            <li key={t.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-3">
              <div className="min-w-0 flex-1">
                <p className="truncate text-[0.9375rem] text-ink">{t.name === "owner" ? "The owner’s key" : actorName({ kind: "agent", name: t.name }).name}</p>
                <p className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[0.8125rem] text-ink-3">
                  <span className="inline-flex flex-wrap items-center gap-x-2 text-ink-2">
                    {p === "all"
                      ? "All projects"
                      : p.map((x) => (
                          <span key={x} className="inline-flex items-center gap-1.5">
                            <ProjectIcon project={x} size={14} />
                            {x}
                          </span>
                        ))}
                  </span>
                  <span>· {access === "full" ? "Full access" : "Read only"}</span>
                  <span>· {t.expiresAt ? (relative(t.expiresAt).endsWith("ago") ? "expired" : `expires ${relative(t.expiresAt)}`) : "never expires"}</span>
                  <span>· {t.lastUsedAt ? `used ${relative(t.lastUsedAt)}` : "never used"}</span>
                </p>
                {t.note && <p className="mt-0.5 text-xs text-ink-3">Note: {t.note}.</p>}
              </div>
              {t.name === "owner" ? (
                <span className="text-xs text-ink-3">made with the box</span>
              ) : (
                <Button variant="ghost" size="sm" className="hover:text-danger" onClick={() => setRevoke(t)} aria-label={`Revoke ${t.name}`}>
                  Revoke
                </Button>
              )}
            </li>
          );
        })}
      </ul>
      <Confirm
        open={!!revoke}
        onClose={() => setRevoke(null)}
        title={`Revoke ${revoke ? actorName({ kind: "agent", name: revoke.name }).name : "this key"}?`}
        body="It stops working at once. What it already did stays in History."
        action={`Revoke ${revoke ? actorName({ kind: "agent", name: revoke.name }).name : "key"}`}
        run={() => api.revokeToken(revoke!.id)}
        done={() => {
          void qc.invalidateQueries({ queryKey: ["tokens"] });
          toast({ title: `Revoked ${revoke ? actorName({ kind: "agent", name: revoke.name }).name : "the key"}.` });
        }}
      />
    </>
  );
}

const field =
  "h-9 w-full rounded-[8px] border border-rule-2 bg-paper-raised px-2.5 text-[0.875rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]";

/** Create key: a name, and whole box or one project. The secret shows once. */
export function CreateKeyDialog({ open, onOpenChange, project }: { open: boolean; onOpenChange: (o: boolean) => void; project?: string }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">{open && <CreateKey onClose={() => onOpenChange(false)} fixed={project} />}</DialogContent>
    </Dialog>
  );
}

function CreateKey({ onClose, fixed }: { onClose: () => void; fixed?: string }) {
  const qc = useQueryClient();
  const projects = useQuery(q.projects);
  const names = (projects.data ?? []).map((p) => p.name);
  const [name, setName] = useState("claude-code");
  const [all, setAll] = useState(!fixed);
  const [picked, setPicked] = useState<string[]>(fixed ? [fixed] : []);
  const [access, setAccess] = useState<"full" | "read">("full");
  const [expires, setExpires] = useState<Days>(90);
  const [created, setCreated] = useState<CreatedToken | null>(null);
  const [confirming, setConfirming] = useState(false);
  const make = useMutation({
    mutationFn: () => createKey({ name: name.trim(), projects: all ? "all" : picked, access, expiresInDays: expires }),
    onSuccess: (c) => {
      setCreated(c);
      setConfirming(false);
      void qc.invalidateQueries({ queryKey: ["tokens"] });
    },
    onError: (e) => isProblem(e, "reauth_required") && setConfirming(true),
  });
  const ok = /^[a-z0-9][a-z0-9-_.]{0,63}$/i.test(name.trim()) && (all || picked.length > 0);
  if (created) return <SecretOnce created={created} onDone={onClose} />;
  if (confirming)
    return (
      <ConfirmItsYou
        why={`${name.trim() || "This key"} will keep working after you sign out, so Tiffin checks it’s really you first. Keys that last more than a day, or have full access, need a sign-in from the last 10 minutes.`}
        back={backHere}
        working={make.isPending}
        workingLabel="Creating…"
        onBack={() => (setConfirming(false), make.reset())}
        onConfirmed={() => make.mutate()}
      />
    );
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (ok) make.mutate();
      }}
    >
      <DialogHeader>
        <DialogTitle>Create key</DialogTitle>
        <DialogDescription>Name it after what will use it, so History reads “claude-code added a database”.</DialogDescription>
      </DialogHeader>
      <DialogBody className="flex flex-col gap-5">
        <label className="block">
          <span className="mb-1 block text-xs font-[550] text-ink-2">Name</span>
          <input value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" spellCheck={false} className={cn(field, "ident")} />
        </label>
        <Choices label="Projects" value={all ? "all" : "some"} onValueChange={(v) => setAll(v === "all")}>
          <Pick value="all" title="All projects" note="Including ones you make later." />
          <Pick value="some" title="Only these">
            {!all && (
              <div className="mt-2 flex flex-wrap gap-1.5">
                {names.map((p) => {
                  const on = picked.includes(p);
                  return (
                    <button
                      key={p}
                      type="button"
                      aria-pressed={on}
                      onClick={() => setPicked(on ? picked.filter((x) => x !== p) : [...picked, p])}
                      className={cn(
                        "inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-[0.8125rem]",
                        on ? "border-brass bg-paper-raised text-ink" : "border-rule-2 text-ink-2 hover:border-rule-3",
                      )}
                    >
                      <ProjectIcon project={p} size={14} />
                      {p}
                    </button>
                  );
                })}
              </div>
            )}
          </Pick>
        </Choices>
        <Choices label="Access" row value={access} onValueChange={(v) => setAccess(v as typeof access)}>
          <Pick value="full" title="Full access" />
          <Pick value="read" title="Read only" />
        </Choices>
        <div>
          <Choices label="Expires" row="grid" value={String(expires)} onValueChange={(v) => setExpires(Number(v) as Days)}>
            <Pick value="30" title="30 days" />
            <Pick value="90" title="90 days" />
            <Pick value="365" title="1 year" />
            <Pick value="0" title="Never" />
          </Choices>
          <p className="mt-1.5 text-xs text-ink-3">It keeps working after you sign out. Revoke it here any time.</p>
        </div>
        {make.isError && !isProblem(make.error, "reauth_required") && <ProblemNote error={make.error} />}
      </DialogBody>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!ok || make.isPending}>
          {make.isPending ? "Creating…" : `Create ${name.trim() || "key"}`}
        </Button>
      </DialogFooter>
    </form>
  );
}

/**
 * A Radix radio group of Picks: arrow keys move and choose, one tab stop.
 * row lays them side by side; "grid" in equal columns, four to a row (two on
 * a phone), so short choices like expiry never leave one hanging on its own.
 */
function Choices({
  label,
  row,
  value,
  onValueChange,
  children,
}: {
  label: string;
  row?: boolean | "grid";
  value: string;
  onValueChange: (v: string) => void;
  children: ReactNode;
}) {
  return (
    <fieldset>
      <legend className="mb-1.5 text-xs font-[550] text-ink-2">{label}</legend>
      <RadioGroup
        aria-label={label}
        orientation={row ? "horizontal" : "vertical"}
        value={value}
        onValueChange={onValueChange}
        className={cn(row === "grid" ? "grid grid-cols-2 gap-1.5 sm:grid-cols-4" : cn("flex gap-1.5", row ? "flex-row flex-wrap" : "flex-col"))}
      >
        {children}
      </RadioGroup>
    </fieldset>
  );
}

function Pick({ value, title, note, children }: { value: string; title: string; note?: string; children?: ReactNode }) {
  return (
    <div className="rounded-[10px] border border-rule-2 px-3 py-2 has-[>[role=radio][data-state=checked]]:border-brass has-[>[role=radio][data-state=checked]]:bg-brass-wash">
      <RadioItem value={value} className="group flex w-full items-center gap-2.5 text-left">
        <span className="grid size-4 shrink-0 place-items-center rounded-full border border-rule-3 group-data-[state=checked]:border-brass" aria-hidden>
          <span className="hidden size-2 rounded-full bg-brass group-data-[state=checked]:block" />
        </span>
        <span className="min-w-0">
          <span className="block text-[0.875rem] font-[550] whitespace-nowrap text-ink">{title}</span>
          {note && <span className="block text-xs text-ink-3">{note}</span>}
        </span>
      </RadioItem>
      {children}
    </div>
  );
}

function SecretOnce({ created, onDone }: { created: CreatedToken; onDone: () => void }) {
  return (
    <>
      <DialogHeader>
        <DialogTitle>{actorName({ kind: "agent", name: created.key.name }).name} is ready</DialogTitle>
        <DialogDescription>Copy it now. This is the only time Tiffin shows the key.</DialogDescription>
      </DialogHeader>
      <DialogBody className="flex flex-col gap-5">
        <div className="flex items-center gap-2 rounded-[8px] border border-brass bg-brass-wash px-3 py-2.5">
          <code data-testid="token-secret" className="min-w-0 flex-1 font-mono text-sm break-all text-ink select-all">
            {created.secret}
          </code>
          <CopyButton value={created.secret} label="Copy key" />
        </div>
        <div>
          <p className="mb-2 text-sm text-ink-2">
            Use it as <span className="ident text-ink">TIFFIN_TOKEN</span>
            {!onThisComputer(location.hostname) && (
              <>
                {" "}
                with <span className="ident text-ink">TIFFIN_URL={location.origin}</span>
              </>
            )}
            , or connect your agent:
          </p>
          <AgentCommand secret={created.secret} />
        </div>
      </DialogBody>
      <DialogFooter>
        <Button variant="primary" onClick={onDone}>
          I’ve stored it
        </Button>
      </DialogFooter>
    </>
  );
}
