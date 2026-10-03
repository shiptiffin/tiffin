import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fingerprint, Link2, Plus, Trash2, UserPlus } from "lucide-react";
import { useState, type ReactNode } from "react";
import { ApiError, api, notOnBox, type Invite, type Passkey, type Person, type Role } from "@/api/client";
import { q } from "@/api/queries";
import { ActorMark } from "@/components/actor";
import { Command } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { ProblemNote, sentence } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Radio, RadioGroup } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuRadioGroup, MenuRadioItem, MenuSeparator, MenuTrigger } from "@/components/ui/dropdown";
import { Input, Label } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { roleCopy, useMe } from "@/lib/me";
import { expiry, relative } from "@/lib/time";
import { createCredential, passkeyError, webauthnSupported } from "@/lib/webauthn";
import { Page } from "./activity";
import { NotOnBox } from "./approvals";

// ---------------------------------------------------------------- passkeys

export function PasskeysPage() {
  useTitle("Passkeys");
  const qc = useQueryClient();
  const keys = useQuery(q.passkeys);
  const { name: me } = useMe();
  const [label, setLabel] = useState("");
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [removing, setRemoving] = useState<Passkey | null>(null);

  if (keys.isError && notOnBox(keys.error)) return <NotOnBox what="Passkeys" />;

  const add = async () => {
    setAdding(true);
    setError(null);
    try {
      const options = await api.beginPasskey();
      const credential = await createCredential(options);
      await api.addPasskey(label.trim() || defaultName(), credential);
      setLabel("");
      qc.invalidateQueries({ queryKey: ["passkeys"] });
    } catch (e) {
      setError(e instanceof ApiError ? sentence(e.problem.detail ?? e.message) : passkeyError(e));
    } finally {
      setAdding(false);
    }
  };

  const list = keys.data ?? [];
  return (
    <Page>
      <header className="animate-rise">
        <h1 className="display text-3xl text-ink">Passkeys</h1>
        <p className="mt-2 max-w-[36rem] text-md text-ink-2">
          Passkeys let you approve risky changes agents ask for. Your fingerprint, face or security key signs the exact plan, so a stolen session
          alone can't approve anything.
        </p>
      </header>

      <section className="mt-10 rounded-xl border border-rule bg-raised/60 p-5">
        <h2 className="text-md font-medium text-ink">Add a passkey{me ? ` for ${me}` : ""}</h2>
        <p className="mt-0.5 text-sm text-ink-3">Name it after the device, so you know which one to remove later.</p>
        <form
          className="mt-4 flex flex-col gap-2 sm:flex-row"
          onSubmit={(e) => {
            e.preventDefault();
            void add();
          }}
        >
          <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder={defaultName()} maxLength={64} aria-label="Passkey name" />
          <Button type="submit" variant="primary" disabled={adding || !webauthnSupported()} className="sm:h-9">
            <Fingerprint />
            {adding ? "Waiting for your device…" : "Add passkey"}
          </Button>
        </form>
        {!webauthnSupported() && (
          <p className="mt-3 text-sm text-ink-3">This browser can't use passkeys here. They need HTTPS or localhost, and a recent browser.</p>
        )}
        {error && (
          <p role="alert" className="mt-3 rounded-lg border border-irr-rule bg-irr-wash px-3 py-2 text-base text-ink">
            {error}
          </p>
        )}
      </section>

      <section className="mt-10" aria-labelledby="keys">
        <h2 id="keys" className="display-italic mb-3 text-xl text-ink">
          Your passkeys
        </h2>
        {keys.isError && <ProblemNote error={keys.error} />}
        {list.length === 0 && keys.isSuccess && <p className="text-base text-ink-3">None yet. Until you add one, approvals wait.</p>}
        <ul className="divide-y divide-rule border-y border-rule empty:border-0">
          {list.map((k) => (
            <li key={k.id} className="flex items-center gap-3 py-3">
              <span className="grid size-8 place-items-center rounded-lg bg-hover text-ink-2">
                <Fingerprint className="size-4" />
              </span>
              <div className="min-w-0 flex-1">
                <p className="truncate text-base font-medium text-ink">{k.name}</p>
                <p className="text-sm text-ink-3">
                  added {relative(k.createdAt)} · {k.lastUsed ? `last approved ${relative(k.lastUsed)}` : "never used"}
                </p>
              </div>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={`Remove ${k.name}`}
                title="Remove"
                onClick={() => setRemoving(k)}
                className="hover:text-irr"
              >
                <Trash2 />
              </Button>
            </li>
          ))}
        </ul>
      </section>

      <Confirm
        open={!!removing}
        onClose={() => setRemoving(null)}
        title={`Remove ${removing?.name ?? "this passkey"}?`}
        body="It stops working at once. Approvals already given stay valid."
        action="Remove passkey"
        run={() => api.deletePasskey(removing!.id)}
        done={() => qc.invalidateQueries({ queryKey: ["passkeys"] })}
      />
    </Page>
  );
}

function defaultName() {
  const ua = navigator.userAgent;
  const os = /Mac/.test(ua)
    ? "Mac"
    : /iPhone|iPad/.test(ua)
      ? "iPhone"
      : /Android/.test(ua)
        ? "Android phone"
        : /Windows/.test(ua)
          ? "Windows PC"
          : "This device";
  return `${os} passkey`;
}

// ---------------------------------------------------------------- people

const assignable: Array<Exclude<Role, "owner">> = ["admin", "member", "viewer"];

export function PeoplePage() {
  useTitle("People");
  const qc = useQueryClient();
  const people = useQuery(q.people);
  const { admin, me } = useMe();
  const [inviting, setInviting] = useState(false);
  const [link, setLink] = useState<Invite | null>(null);
  const [removing, setRemoving] = useState<Person | null>(null);
  const [error, setError] = useState<unknown>(null);

  const refresh = () => qc.invalidateQueries({ queryKey: ["people"] });
  const change = async (p: Person, role: Exclude<Role, "owner">) => {
    setError(null);
    try {
      await api.updatePerson(p.id, { role });
      refresh();
    } catch (e) {
      setError(e);
    }
  };
  const newLink = async (p: Person) => {
    setError(null);
    try {
      setLink(await api.personLink(p.id));
    } catch (e) {
      setError(e);
    }
  };

  const list = (people.data ?? []).filter((p) => !p.disabledAt);
  return (
    <Page wide>
      <header className="flex flex-col gap-5 sm:flex-row sm:items-end sm:justify-between">
        <div className="animate-rise">
          <h1 className="display text-3xl text-ink">People</h1>
          <p className="mt-2 max-w-[34rem] text-md text-ink-2">
            Everyone who can open this dashboard, and what they can do. Their changes show up under their own name.
          </p>
        </div>
        {admin && (
          <Button variant="primary" size="lg" onClick={() => setInviting(true)} className="self-start sm:self-auto">
            <UserPlus />
            Invite someone
          </Button>
        )}
      </header>

      {(people.isError || !!error) && <ProblemNote className="mt-8" error={people.error ?? error} />}

      <ul className="mt-10 divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
        {list.map((p, k) => {
          const you = me?.person === p.id;
          return (
            <li
              key={p.id}
              className="flex animate-rise flex-wrap items-center gap-x-4 gap-y-2 px-4 py-4 sm:px-5"
              style={{ animationDelay: `${k * 30}ms` }}
            >
              <ActorMark actor={{ kind: p.role === "owner" ? "owner" : "human", name: p.name, id: p.id }} className="size-8 text-xs" />
              <div className="min-w-0 flex-1">
                <p className="truncate text-md font-medium text-ink">
                  {p.name}
                  {you && <span className="ml-2 text-sm font-normal text-ink-3">you</span>}
                </p>
                <p className="truncate text-sm text-ink-3">
                  {p.email ? `${p.email} · ` : ""}added {relative(p.createdAt)}
                </p>
              </div>
              <div className="flex items-center gap-1">
                {admin && p.role !== "owner" && !you ? (
                  <Menu>
                    <MenuTrigger className="flex h-8 items-center gap-1.5 rounded-md border border-rule bg-paper px-2.5 text-sm text-ink transition-colors hover:border-rule-strong">
                      {roleCopy[p.role].label}
                      <span aria-hidden className="text-ink-4">
                        ▾
                      </span>
                    </MenuTrigger>
                    <MenuContent align="end" className="w-80">
                      <MenuLabel>Role</MenuLabel>
                      <MenuRadioGroup value={p.role} onValueChange={(v) => change(p, v as Exclude<Role, "owner">)}>
                        {assignable.map((r) => (
                          <MenuRadioItem key={r} value={r} className="h-auto items-start py-2">
                            <span>
                              <span className="block font-medium text-ink">{roleCopy[r].label}</span>
                              <span className="block text-sm text-ink-3">{roleCopy[r].blurb}</span>
                            </span>
                          </MenuRadioItem>
                        ))}
                      </MenuRadioGroup>
                      <MenuSeparator />
                      <MenuItem onSelect={() => newLink(p)}>
                        <Link2 />
                        New sign-in link
                      </MenuItem>
                      <MenuItem onSelect={() => setRemoving(p)} className="text-irr data-[highlighted]:text-irr [&_svg]:text-irr">
                        <Trash2 />
                        Remove from this box
                      </MenuItem>
                    </MenuContent>
                  </Menu>
                ) : (
                  <span
                    className={cn("rounded-full px-2.5 py-1 text-sm", p.role === "owner" ? "bg-brass-wash text-brass-ink" : "bg-hover text-ink-2")}
                  >
                    {roleCopy[p.role].label}
                  </span>
                )}
              </div>
            </li>
          );
        })}
      </ul>

      <dl className="mt-10 grid gap-x-8 gap-y-4 sm:grid-cols-2">
        {(["owner", ...assignable] as Role[]).map((r) => (
          <div key={r}>
            <dt className="text-sm font-medium text-ink">{roleCopy[r].label}</dt>
            <dd className="mt-0.5 text-sm text-ink-3">{roleCopy[r].blurb}</dd>
          </div>
        ))}
      </dl>

      <InviteDialog open={inviting} onOpenChange={setInviting} onDone={refresh} />
      <Dialog open={!!link} onOpenChange={(o) => !o && setLink(null)}>
        <DialogContent>{link && <LinkView invite={link} onDone={() => setLink(null)} />}</DialogContent>
      </Dialog>
      <Confirm
        open={!!removing}
        onClose={() => setRemoving(null)}
        title={`Remove ${removing?.name ?? "them"}?`}
        body="They lose access right away and any open sessions end. Their past changes stay in the log."
        action="Remove"
        run={() => api.removePerson(removing!.id)}
        done={refresh}
      />
    </Page>
  );
}

function InviteDialog({ open, onOpenChange, onDone }: { open: boolean; onOpenChange: (o: boolean) => void; onDone: () => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <InviteForm onClose={() => onOpenChange(false)} onDone={onDone} />
      </DialogContent>
    </Dialog>
  );
}

function InviteForm({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Exclude<Role, "owner">>("member");
  const m = useMutation({
    mutationFn: () => api.invite({ name: name.trim(), role, ...(email.trim() ? { email: email.trim() } : {}) }),
    onSuccess: onDone,
  });
  if (m.data) return <LinkView invite={m.data} onDone={onClose} />;
  return (
    <form
      className="flex min-h-0 flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        if (name.trim()) m.mutate();
      }}
    >
      <DialogHeader>
        <DialogTitle>Invite someone</DialogTitle>
        <DialogDescription>You'll get a sign-in link to send them. It works once, for 7 days.</DialogDescription>
      </DialogHeader>
      <DialogBody className="flex flex-col gap-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="p-name">Name</Label>
            <Input
              id="p-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Maya Okafor"
              maxLength={64}
              autoFocus
              autoComplete="off"
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="p-email">
              Email <span className="font-normal text-ink-3">(optional)</span>
            </Label>
            <Input
              id="p-email"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="maya@example.com"
              maxLength={254}
              autoComplete="off"
            />
          </div>
        </div>
        <fieldset>
          <legend className="text-sm font-medium text-ink">Role</legend>
          <RadioGroup
            value={role}
            onValueChange={(v) => setRole(v as Exclude<Role, "owner">)}
            className="mt-2 overflow-hidden rounded-lg border border-rule"
          >
            {assignable.map((r) => (
              <label
                key={r}
                className={cn(
                  "flex cursor-pointer gap-3 border-b border-rule px-3.5 py-2.5 last:border-b-0 hover:bg-hover/60",
                  role === r && "bg-hover",
                )}
              >
                <Radio value={r} className="mt-0.5" />
                <span>
                  <span className="block text-base font-medium text-ink">{roleCopy[r].label}</span>
                  <span className="block text-sm text-ink-3">{roleCopy[r].blurb}</span>
                </span>
              </label>
            ))}
          </RadioGroup>
        </fieldset>
        {m.isError && <ProblemNote error={m.error} />}
      </DialogBody>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!name.trim() || m.isPending}>
          <Plus />
          {m.isPending ? "Inviting…" : "Invite"}
        </Button>
      </DialogFooter>
    </form>
  );
}

function LinkView({ invite, onDone }: { invite: Invite; onDone: () => void }) {
  return (
    <>
      <DialogHeader>
        <DialogTitle>
          Send this to <span className="display-italic">{invite.person.name}</span>
        </DialogTitle>
        <DialogDescription>
          It signs them in as {roleCopy[invite.person.role].label.toLowerCase()}, once. It expires {expiry(invite.expiresAt)}.
        </DialogDescription>
      </DialogHeader>
      <DialogBody>
        <Command cmd={invite.url} />
        <p className="mt-3 text-sm text-ink-3">Anyone with the link can use it, so send it somewhere private.</p>
      </DialogBody>
      <DialogFooter>
        <Button variant="primary" onClick={onDone}>
          Done
        </Button>
      </DialogFooter>
    </>
  );
}

/** A small confirm dialog for destructive one-liners. */
export function Confirm({
  open,
  onClose,
  title,
  body,
  action,
  run,
  done,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  body: ReactNode;
  action: string;
  run: () => Promise<unknown>;
  done: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-md">
        {open && <ConfirmBody title={title} body={body} action={action} run={run} done={done} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  );
}

function ConfirmBody({
  title,
  body,
  action,
  run,
  done,
  onClose,
}: {
  title: string;
  body: ReactNode;
  action: string;
  run: () => Promise<unknown>;
  done: () => void;
  onClose: () => void;
}) {
  const m = useMutation({
    mutationFn: run,
    onSuccess: () => {
      done();
      onClose();
    },
  });
  return (
    <>
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{body}</DialogDescription>
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
        <Button variant="danger" disabled={m.isPending} onClick={() => m.mutate()}>
          <Trash2 />
          {m.isPending ? "Working…" : action}
        </Button>
      </DialogFooter>
    </>
  );
}
