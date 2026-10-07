import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RenameSelf } from "@/components/name-ask";
import { useState, type ReactNode } from "react";
import { ApiError, api, isProblem, notOnBox, type Invite, type Passkey, type Person, type Role } from "@/api/client";
import { q } from "@/api/queries";
import { ActorMark } from "@/components/actor";
import { Confirm } from "@/components/confirm";
import { ConfirmItsYou } from "@/components/confirm-its-you";
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
import { createCredential, passkeyError, passkeyWords, webauthnSupported } from "@/lib/webauthn";
import { NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { accessCrumbs, Facts, Group, Rows } from "@/components/health-kit";
import { countWords } from "@/lib/format";
import { StateSentence } from "@/components/jobs-words";
import { boxMail, type BoxMailResult } from "@/api/modules";
import { EmailDialog, MailOutcome } from "@/components/person-email";
import { PersonSessionsDialog } from "@/components/person-sessions";
import { Link } from "@tanstack/react-router";

// ---------------------------------------------------------------- passkeys

export function PasskeysPage() {
  const words = passkeyWords();
  useTitle(words.title);
  const qc = useQueryClient();
  const keys = useQuery(q.passkeys);
  const { name: me } = useMe();
  const [label, setLabel] = useState("");
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [removing, setRemoving] = useState<Passkey | null>(null);
  // Sudo mode: a passkey signs in for good, so adding one needs a sign-in
  // from the last 10 minutes; the box says reauth_required and we ask.
  const [confirm, setConfirm] = useState<"ask" | "done" | null>(null);

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
      if (isProblem(e, "reauth_required")) setConfirm("ask");
      else setError(e instanceof ApiError ? sentence(e.problem.detail ?? e.message) : passkeyError(e));
    } finally {
      setAdding(false);
    }
  };

  const list = keys.data ?? [];
  return (
    <Page>
      <PageHeader
        title={`Sign in with ${words.name}`}
        lede={`Use ${words.how} instead of a sign-in link. It works on each device you set up (it’s a passkey kept on that device).`}
      />
      {keys.isSuccess && (
        <StateSentence className="mt-6">
          {list.length === 0 ? (
            <span className="text-ink-2">Not set up yet, so you sign in with a link from the terminal.</span>
          ) : (
            `${countWords(list.length, "device", "devices", true)} can sign you in${list.some((k) => k.lastUsed) ? `; the last was used ${relative(list.map((k) => k.lastUsed ?? "").sort().pop()!)}` : ""}.`
          )}
        </StateSentence>
      )}

      <Group label={me ? `Your devices, ${me}` : "Your devices"} id="keys" aside={list.length ? countWords(list.length, "device") : undefined}>
        {keys.isError && <ProblemNote error={keys.error} />}
        {keys.isPending && <Skeleton className="h-20" />}
        {list.length === 0 && keys.isSuccess && (
          <p className="border-y border-rule py-4 text-[0.875rem] text-ink-2">None yet. Set up this device below.</p>
        )}
        {list.length > 0 && (
          <Rows>
            {list.map((k) => (
              <li key={k.id} className="flex items-center gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-[0.875rem] text-ink">{k.name}</p>
                  <p className="text-[0.8125rem] text-ink-3">
                    Added {relative(k.createdAt)} · {k.lastUsed ? `last used ${relative(k.lastUsed)}` : "not used yet"}
                  </p>
                </div>
                <Button variant="ghost" size="sm" onClick={() => setRemoving(k)} className="hover:text-danger">
                  Remove…
                </Button>
              </li>
            ))}
          </Rows>
        )}
      </Group>

      <Group label="Set up this device" id="add">
        <form
          className="flex flex-col gap-2 sm:flex-row"
          onSubmit={(e) => {
            e.preventDefault();
            void add();
          }}
        >
          <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder={defaultName()} maxLength={64} aria-label="Device name" />
          <Button type="submit" variant={list.length ? "secondary" : "primary"} size="lg" className="h-9" disabled={adding || !webauthnSupported()}>
            {adding ? `Waiting for ${words.button}…` : "Set up this device"}
          </Button>
        </form>
        <p className="mt-2 text-[0.8125rem] text-ink-3">Name it after the device, so you know which one to remove later. Your browser asks for {words.how}.</p>
        {!webauthnSupported() && <p className="mt-2 text-[0.8125rem] text-warn-ink">This browser can’t do it here. It needs HTTPS (or localhost) and a recent browser.</p>}
        {error && (
          <p role="alert" className="mt-3 text-[0.875rem] text-danger">
            {error}
          </p>
        )}
      </Group>

      <Dialog open={!!confirm} onOpenChange={(o) => !o && setConfirm(null)}>
        <DialogContent className="max-w-md">
          {confirm === "ask" && (
            <ConfirmItsYou
              why="A passkey signs in as you from now on, so Tiffin checks it’s really you first: adding one needs a sign-in from the last 10 minutes."
              back="/settings/passkeys"
              onBack={() => setConfirm(null)}
              onConfirmed={() => setConfirm("done")}
              noPasskeyNote="With Google, GitHub or an emailed link you ask for; the owner can also use a fresh tiffin login. A link someone else made doesn’t count."
            />
          )}
          {confirm === "done" && (
            <>
              <DialogHeader>
                <DialogTitle>It’s you</DialogTitle>
                <DialogDescription>For the next 10 minutes you can add passkeys. Your browser asks for {words.how} next.</DialogDescription>
              </DialogHeader>
              <DialogFooter>
                <Button type="button" variant="ghost" onClick={() => setConfirm(null)}>
                  Cancel
                </Button>
                <Button
                  variant="primary"
                  onClick={() => {
                    setConfirm(null);
                    void add();
                  }}
                >
                  Set up this device
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>

      <Confirm
        open={!!removing}
        onClose={() => setRemoving(null)}
        title={`Remove ${removing?.name ?? "this device"}?`}
        body="It can’t sign you in any more. You can set it up again later."
        action="Remove device"
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
  return os;
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
  const [emailing, setEmailing] = useState<Person | null>(null);
  const [ending, setEnding] = useState<Person | null>(null);
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
  const newLink = async (p: Person, email = false) => {
    setError(null);
    try {
      setLink(email ? ((await boxMail.emailLink(p.id)) as Invite) : await api.personLink(p.id));
    } catch (e) {
      setError(e);
    }
  };

  const list = (people.data ?? []).filter((p) => !p.disabledAt);
  return (
    <Page wide>
      <PageHeader
        eyebrow={accessCrumbs}
        title="People"
        lede="Everyone who can open this dashboard, and what they may do. Their changes are signed with their own names."
        actions={
          admin && (
            <Button variant="primary" size="lg" onClick={() => setInviting(true)}>
              Invite someone
            </Button>
          )
        }
      />

      {(people.isError || !!error) && <ProblemNote className="mt-8" error={people.error ?? error} />}

      <Group label="On this box" id="people" aside={list.length ? countWords(list.length, "person", "people") : undefined}>
        {people.isPending && <Skeleton className="h-20" />}
        <Rows>
          {list.map((p) => {
            const you = me?.person === p.id;
            return (
              <li key={p.id} className="grid grid-cols-[1.75rem_minmax(0,1fr)_auto] items-center gap-x-3 py-3 sm:gap-x-4">
                <ActorMark actor={{ kind: p.role === "owner" ? "owner" : "human", name: p.name, id: p.id }} className="size-6 text-[0.6875rem]" />
                <div className="min-w-0">
                  <p className="truncate text-[0.875rem] text-ink">
                    {p.name}
                    {you && <span className="ml-2 text-[0.8125rem] text-ink-3">you</span>}
                  </p>
                  <p className="truncate text-[0.8125rem] text-ink-3">
                    {p.email ? `${p.email} · ` : ""}joined {relative(p.createdAt)}
                  </p>
                  {you && (
                    <div className="flex flex-wrap items-baseline gap-x-4">
                      {admin && <RenameSelf current={p.name} />}
                      <button
                        type="button"
                        onClick={() => setEmailing(p)}
                        className="mt-0.5 text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink"
                      >
                        {p.email ? "Change your email" : "Add your email"}
                      </button>
                      <Link
                        to="/settings/sign-ins"
                        className="mt-0.5 text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink"
                      >
                        Your sign-ins
                      </Link>
                    </div>
                  )}
                </div>
                {admin && p.role !== "owner" && !you ? (
                  <Menu>
                    <MenuTrigger className="flex h-8 items-center gap-1.5 rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.84375rem] text-ink transition-colors hover:bg-paper-hover">
                      {roleCopy[p.role].label}
                      <span aria-hidden className="text-ink-3">
                        ▾
                      </span>
                    </MenuTrigger>
                    <MenuContent align="end" className="w-80">
                      <MenuLabel>Role</MenuLabel>
                      <MenuRadioGroup value={p.role} onValueChange={(v) => change(p, v as Exclude<Role, "owner">)}>
                        {assignable.map((r) => (
                          <MenuRadioItem key={r} value={r} className="h-auto items-start py-2">
                            <span>
                              <span className="block font-[550] text-ink">{roleCopy[r].label}</span>
                              <span className="block text-sm text-ink-3">{roleCopy[r].blurb}</span>
                            </span>
                          </MenuRadioItem>
                        ))}
                      </MenuRadioGroup>
                      <MenuSeparator />
                      <MenuItem onSelect={() => newLink(p)}>New sign-in link</MenuItem>
                      {p.email && <MenuItem onSelect={() => newLink(p, true)}>Email a new sign-in link</MenuItem>}
                      <MenuItem onSelect={() => setEmailing(p)}>{p.email ? "Change email…" : "Add email…"}</MenuItem>
                      <MenuItem onSelect={() => setEnding(p)}>End sessions…</MenuItem>
                      <MenuItem variant="danger" onSelect={() => setRemoving(p)}>
                        Remove from this box…
                      </MenuItem>
                    </MenuContent>
                  </Menu>
                ) : (
                  <span className={cn("text-[0.84375rem]", p.role === "owner" ? "text-brass-ink" : "text-ink-2")}>{roleCopy[p.role].label}</span>
                )}
              </li>
            );
          })}
        </Rows>
        {list.length === 1 && admin && <p className="mt-3 text-[0.8125rem] text-ink-3">Just you so far. Invite someone and they get a one-time sign-in link, good for seven days.</p>}
      </Group>

      <Group label="What each role may do" id="roles">
        <Facts items={(["owner", ...assignable] as Role[]).map((r) => [roleCopy[r].label, <span className="text-ink-2">{roleCopy[r].blurb}</span>] as [ReactNode, ReactNode])} />
      </Group>

      <InviteDialog open={inviting} onOpenChange={setInviting} onDone={refresh} />
      <PersonSessionsDialog person={ending} onClose={() => setEnding(null)} />
      <EmailDialog person={emailing} self={!!emailing && me?.person === emailing.id} onClose={() => setEmailing(null)} onDone={refresh} />
      <Dialog open={!!link} onOpenChange={(o) => !o && setLink(null)}>
        <DialogContent>{link && <LinkView invite={link} onDone={() => setLink(null)} />}</DialogContent>
      </Dialog>
      <Confirm
        open={!!removing}
        onClose={() => setRemoving(null)}
        title={`Remove ${removing?.name ?? "them"}?`}
        body="They lose access at once and any open sessions end. Their past changes stay in History under their name."
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
        <DialogDescription>They get a sign-in link that works once, for 7 days. Add their email and the box sends it to them.</DialogDescription>
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
          <legend className="text-sm font-[550] text-ink">Role</legend>
          <RadioGroup
            value={role}
            onValueChange={(v) => setRole(v as Exclude<Role, "owner">)}
            className="mt-2 divide-y divide-rule border-y border-rule"
          >
            {assignable.map((r) => (
              <label
                key={r}
                className={cn(
                  "flex cursor-pointer gap-3 px-2 py-2.5 hover:bg-paper-hover",
                  role === r && "bg-paper-select",
                )}
              >
                <Radio value={r} className="mt-0.5" />
                <span>
                  <span className="block text-base font-[550] text-ink">{roleCopy[r].label}</span>
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
          {m.isPending ? "Inviting…" : name.trim() ? `Invite ${name.trim().split(" ")[0]}` : "Invite"}
        </Button>
      </DialogFooter>
    </form>
  );
}

function LinkView({ invite, onDone }: { invite: Invite & { email?: BoxMailResult }; onDone: () => void }) {
  const sent = invite.email?.delivery === "relay";
  return (
    <>
      <DialogHeader>
        <DialogTitle>{sent ? `Sent to ${invite.person.name}` : `Send this to ${invite.person.name}`}</DialogTitle>
        <DialogDescription>
          It signs them in as {roleCopy[invite.person.role].label.toLowerCase()}, once. It expires {expiry(invite.expiresAt)}.
        </DialogDescription>
      </DialogHeader>
      <DialogBody>
        {invite.email && <MailOutcome result={invite.email} className="mb-4" />}
        <Command cmd={invite.url} />
        <p className="mt-3 text-sm text-ink-3">
          {sent ? "The same link, if you’d rather send it another way. " : ""}Anyone with the link can use it, so send it somewhere private.
        </p>
      </DialogBody>
      <DialogFooter>
        <Button variant="primary" onClick={onDone}>
          Done
        </Button>
      </DialogFooter>
    </>
  );
}

export { Confirm };
