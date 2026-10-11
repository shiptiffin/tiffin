import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { access, aq, type ServerAccess, type ServerKey } from "@/api/access";
import { isProblem } from "@/api/client";
import { q } from "@/api/queries";
import { ConfirmItsYou } from "@/components/confirm-its-you";
import { Command } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { accessCrumbs, Group, Rows } from "@/components/health-kit";
import { StateSentence } from "@/components/jobs-words";
import { Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Segmented } from "@/components/segmented";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input, Label } from "@/components/ui/input";
import { envField } from "@/components/env-dialog";
import { shipTiffinAccount } from "@/lib/box";
import { cn } from "@/lib/cn";
import { countWords } from "@/lib/format";
import { useMe } from "@/lib/me";
import {
  addedWords,
  fetchGitHubKeys,
  fingerprint,
  githubLogin,
  keysSentence,
  mostRecent,
  parseKeyLine,
  parseKeys,
  shortFingerprint,
  sourceWords,
  typeWords,
  type GitHubKey,
  type GitHubUser,
  type PublicKey,
} from "@/lib/ssh-key";
import { relative } from "@/lib/time";

const back = "/settings/server-access";
const sudoWhy = "A key here can log in to the server as root, so Tiffin checks it’s really you first: changing keys needs a sign-in from the last 10 minutes.";

/**
 * Settings › Server access (the owner's): the SSH keys that can log in to the
 * server as root, to add (pasted, or picked from someone's GitHub keys) and
 * remove. The box keeps them in its own key file; keys added by hand in
 * /root/.ssh stay the owner's and aren't listed. Changing them needs a recent
 * sign-in (reauth_required → Confirm it's you, then it tries again).
 */
export function ServerAccessPage() {
  useTitle("Server access");
  const { role, me } = useMe();
  const owner = role === "owner";
  const sa = useQuery({ ...aq.serverAccess, enabled: owner });
  const box = useQuery(aq.box);
  const status = useQuery(q.status());
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<ServerKey | null>(null);

  const keys = sa.data?.keys ?? [];
  const host = sa.data?.host || box.data?.host || "your-box";
  const managed = !!box.data?.managed;
  const account = shipTiffinAccount(status.data);

  if (me && !owner)
    return (
      <Page>
        <PageHeader eyebrow={accessCrumbs} title="Server access" />
        <p className="mt-6 text-md text-ink-2">Only the box’s owner can see and change who can log in to the server.</p>
      </Page>
    );

  return (
    <Page>
      <PageHeader
        eyebrow={accessCrumbs}
        title="Server access"
        lede="SSH keys that can log in to this server as root, with full control of it."
        actions={
          sa.isSuccess && (
            <Button variant={keys.length ? "secondary" : "primary"} size="lg" onClick={() => setAdding(true)}>
              Add key
            </Button>
          )
        }
      />
      {sa.isSuccess && (
        <StateSentence className="mt-6">{keys.length ? keysSentence(keys.length) : <span className="text-ink-2">{keysSentence(0)}</span>}</StateSentence>
      )}
      {sa.isError && <ProblemNote className="mt-8" error={sa.error} title="Couldn’t read the server’s keys." />}

      <Group label="Keys" id="keys">
        {(sa.isPending || !me) && <Skeleton className="h-20" />}
        {sa.isSuccess && keys.length === 0 && (
          <p className="border-y border-rule py-4 text-[0.875rem] text-ink-2">
            Add the public key from your computer. Then you can log in to the server, and get back into this dashboard if you’re ever signed out.
          </p>
        )}
        {keys.length > 0 && (
          <Rows>
            {keys.map((k) => (
              <li key={k.fingerprint} className="flex items-center gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-[0.875rem] text-ink">{sourceWords(k.source)}</p>
                  <p className="text-[0.8125rem] text-ink-3">
                    {typeWords(k.type, k.bits)} ·{" "}
                    <code className="ident text-ink-3" title={k.fingerprint}>
                      {shortFingerprint(k.fingerprint)}
                    </code>
                    {addedWords(k.added) && ` · added ${addedWords(k.added)}`}
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

      <details className="group mt-11">
        <summary className="cursor-pointer list-none text-[0.8125rem] text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
          <span className="inline-block transition-transform group-open:rotate-90">›</span> Log in from your computer
        </summary>
        <div className="mt-4 max-w-[40rem] text-[0.875rem]">
          <p className="text-ink-2">Log in to the server:</p>
          <Command cmd={`ssh root@${host}`} className="mt-2" />
          <p className="mt-5 text-ink-2">Or get a link that signs you in to this dashboard, if you’re ever signed out:</p>
          <Command cmd={`ssh root@${host} tiffin login`} className="mt-2" />
          <p className="mt-5 text-ink-3">
            {managed ? (
              <>
                If you skipped SSH when you set up the box, the server doesn’t let it in yet. Open it from your{" "}
                {account ? (
                  <a href={account} target="_blank" rel="noreferrer" className="text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                    ShipTiffin account page
                  </a>
                ) : (
                  "ShipTiffin account page"
                )}
                .
              </>
            ) : (
              "If it doesn’t connect, check that the server’s firewall lets in SSH (port 22)."
            )}
          </p>
          {sa.data?.file && (
            <p className="mt-3 text-ink-3">
              Tiffin keeps these keys in <code className="ident text-ink-2">{sa.data.file}</code>. Keys you add yourself to{" "}
              <code className="ident text-ink-2">/root/.ssh/authorized_keys</code> keep working and aren’t listed here.
            </p>
          )}
        </div>
      </details>

      <AddKeyDialog open={adding} onClose={() => setAdding(false)} />
      <RemoveKeyDialog serverKey={removing} last={keys.length === 1} managed={managed} onClose={() => setRemoving(null)} />
    </Page>
  );
}

/** After a change: the page shows the box's answer at once, and the banner rechecks. */
function useSaved() {
  const qc = useQueryClient();
  return (data: ServerAccess) => {
    qc.setQueryData(aq.serverAccess.queryKey, data);
    void qc.invalidateQueries({ queryKey: ["recovery"] });
  };
}

// ---------------------------------------------------------------- remove

function RemoveKeyDialog({ serverKey, last, managed, onClose }: { serverKey: ServerKey | null; last: boolean; managed: boolean; onClose: () => void }) {
  return (
    <Dialog open={!!serverKey} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-md">{serverKey && <RemoveKey key={serverKey.fingerprint} serverKey={serverKey} last={last} managed={managed} onClose={onClose} />}</DialogContent>
    </Dialog>
  );
}

function RemoveKey({ serverKey: k, last, managed, onClose }: { serverKey: ServerKey; last: boolean; managed: boolean; onClose: () => void }) {
  const saved = useSaved();
  const [confirming, setConfirming] = useState(false);
  const m = useMutation({
    mutationFn: () => access.removeKey(k.fingerprint),
    onSuccess: (data) => {
      saved(data);
      toast({ title: "Removed the key." });
      onClose();
    },
    onError: (e) => isProblem(e, "reauth_required") && setConfirming(true),
  });
  if (confirming)
    return (
      <ConfirmItsYou
        why={sudoWhy}
        back={back}
        working={m.isPending}
        workingLabel="Removing…"
        onBack={() => (setConfirming(false), m.reset())}
        onConfirmed={() => m.mutate()}
      />
    );
  return (
    <>
      <DialogHeader>
        <DialogTitle>Remove this key?</DialogTitle>
        <DialogDescription>
          {sourceWords(k.source)}, {typeWords(k.type, k.bits)} <code className="ident">{shortFingerprint(k.fingerprint)}</code>. It can’t log in to the server any more.
        </DialogDescription>
      </DialogHeader>
      <DialogBody className="flex flex-col gap-3 text-[0.875rem]">
        {last && (
          <p className="text-warn-ink">
            It’s the last key. Without one, getting back in if you’re ever signed out means {managed ? "Hetzner’s console" : "the server’s console"}.
          </p>
        )}
        <p className="text-ink-3">SSH sessions already open with it stay open until they end.</p>
        {m.isError && !isProblem(m.error, "reauth_required") && <ProblemNote error={m.error} />}
      </DialogBody>
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={onClose}>
          Keep it
        </Button>
        <Button variant="danger" disabled={m.isPending} onClick={() => m.mutate()}>
          {m.isPending ? "Removing…" : "Remove key"}
        </Button>
      </DialogFooter>
    </>
  );
}

// ---------------------------------------------------------------- add

function AddKeyDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>{open && <AddKey onClose={onClose} />}</DialogContent>
    </Dialog>
  );
}

type Chosen = { keys: string; source: "pasted" | `github:${string}`; count: number };

function AddKey({ onClose }: { onClose: () => void }) {
  const saved = useSaved();
  const [tab, setTab] = useState<"paste" | "github">("paste");
  const [pick, setPick] = useState<Chosen | null>(null);
  const [confirming, setConfirming] = useState(false);
  const m = useMutation({
    mutationFn: (p: Chosen) => access.addKeys(p.keys, p.source),
    onSuccess: (data, p) => {
      saved(data);
      toast({ title: p.count === 1 ? "Added the key." : `Added ${countWords(p.count, "key")}.`, detail: p.count === 1 ? "It can log in to the server as root now." : "They can log in to the server as root now." });
      onClose();
    },
    onError: (e) => isProblem(e, "reauth_required") && setConfirming(true),
  });
  // The form stays mounted under "Confirm it's you", so Back finds the key as it was.
  return (
    <>
      {confirming && (
        <ConfirmItsYou
          why={sudoWhy}
          back={back}
          working={m.isPending}
          workingLabel="Adding…"
          onBack={() => (setConfirming(false), m.reset())}
          onConfirmed={() => pick && m.mutate(pick)}
        />
      )}
      <form
        className={cn("flex min-h-0 flex-col", confirming && "hidden")}
        onSubmit={(e) => {
          e.preventDefault();
          if (pick) m.mutate(pick);
        }}
      >
        <DialogHeader>
          <DialogTitle>Add an SSH key</DialogTitle>
          <DialogDescription>Anyone holding the matching private key can log in to this server as root. Add only your own.</DialogDescription>
        </DialogHeader>
        <DialogBody className="flex flex-col gap-4">
          <Segmented
            label="Where the key comes from"
            value={tab}
            onChange={(v) => {
              setTab(v);
              setPick(null);
            }}
            options={[
              { value: "paste", label: "Paste" },
              { value: "github", label: "From GitHub" },
            ]}
            className="self-start"
          />
          {tab === "paste" ? <PasteKey onPick={setPick} /> : <GitHubKeys onPick={setPick} onPaste={() => setTab("paste")} />}
          {m.isError && !isProblem(m.error, "reauth_required") && <ProblemNote error={m.error} />}
        </DialogBody>
        <DialogFooter>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!pick || pick.count === 0 || m.isPending}>
            {m.isPending ? "Adding…" : pick && pick.count > 1 ? `Add ${countWords(pick.count, "key")}` : "Add key"}
          </Button>
        </DialogFooter>
      </form>
    </>
  );
}

const isMac = typeof navigator !== "undefined" && /Mac/.test(navigator.userAgent);

/** The SHA256 fingerprints of parsed keys, as they arrive. */
function useFingerprints(keys: PublicKey[]): Record<string, string> {
  const [fps, setFps] = useState<Record<string, string>>({});
  const lines = keys.map((k) => k.line).join("\n");
  useEffect(() => {
    let live = true;
    void Promise.all(keys.map(async (k) => [k.line, await fingerprint(k.blob)] as const))
      .then((pairs) => live && setFps(Object.fromEntries(pairs)))
      .catch(() => undefined);
    return () => {
      live = false;
    };
    // The lines say everything about the keys.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lines]);
  return fps;
}

function PasteKey({ onPick }: { onPick: (p: Chosen | null) => void }) {
  const [text, setText] = useState("");
  const parsed = parseKeys(text);
  const fps = useFingerprints(parsed.keys);
  const ready = text.trim() !== "" && !parsed.problem && parsed.keys.length > 0;
  useEffect(() => {
    onPick(ready ? { keys: parsed.keys.map((k) => k.line).join("\n"), source: "pasted", count: parsed.keys.length } : null);
    // Recomputed from the text alone.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text]);
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor="ssh-key">Public key</Label>
      <textarea
        id="ssh-key"
        autoFocus
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={4}
        placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA… you@laptop"
        autoComplete="off"
        spellCheck={false}
        aria-invalid={!!parsed.problem && text.trim() !== ""}
        className={cn(envField, "ident block min-h-[5.5rem] resize-y py-2 leading-5 [overflow-wrap:anywhere]")}
      />
      {text.trim() !== "" && parsed.problem ? (
        <p className="text-sm text-danger">{parsed.problem}</p>
      ) : ready ? (
        <ul className="text-[0.8125rem] text-ink-3">
          {parsed.keys.map((k) => (
            <li key={k.line}>
              {typeWords(k.type, k.bits)}
              {k.comment && ` · ${k.comment}`}
              {fps[k.line] && (
                <>
                  {" · "}
                  <code className="ident" title={fps[k.line]}>
                    {shortFingerprint(fps[k.line])}
                  </code>
                </>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <div className="text-[0.8125rem] text-ink-3">
          <p>Copy yours on your computer with:</p>
          <Command cmd={isMac ? "pbcopy < ~/.ssh/id_ed25519.pub" : "cat ~/.ssh/id_ed25519.pub"} className="mt-1.5" />
          <details className="group mt-3">
            <summary className="cursor-pointer list-none select-none hover:text-ink [&::-webkit-details-marker]:hidden">
              <span className="inline-block transition-transform group-open:rotate-90">›</span> Don’t have one?
            </summary>
            <p className="mt-2">Make one, pressing Enter to accept the defaults, then copy it as above:</p>
            <Command cmd='ssh-keygen -t ed25519 -C "you@laptop"' className="mt-1.5" />
          </details>
        </div>
      )}
    </div>
  );
}

type Found = { user: GitHubUser; keys: Array<GitHubKey & { parsed: PublicKey | string; fp?: string }> };

function GitHubKeys({ onPick, onPaste }: { onPick: (p: Chosen | null) => void; onPaste: () => void }) {
  const [name, setName] = useState("");
  const [found, setFound] = useState<Found | null>(null);
  const [chosen, setChosen] = useState<Set<number>>(new Set());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const login = githubLogin(name);

  const look = async () => {
    if (!login) return;
    setBusy(true);
    setError(null);
    setFound(null);
    onPick(null);
    try {
      const r = await fetchGitHubKeys(login);
      const keys = await Promise.all(
        r.keys.map(async (k) => {
          const parsed = parseKeyLine(k.key);
          return { ...k, parsed, fp: typeof parsed === "string" ? undefined : await fingerprint(parsed.blob) };
        }),
      );
      const usable = keys.filter((k) => typeof k.parsed !== "string");
      const first = mostRecent(usable);
      setFound({ user: r.user, keys });
      choose(new Set(first === undefined ? [] : [first]), { user: r.user, keys });
    } catch (e) {
      setError(e instanceof Error ? e.message : "GitHub didn’t answer. Try again, or paste the key.");
    } finally {
      setBusy(false);
    }
  };

  // Exactly the key texts reviewed here are what the box gets: no second fetch.
  const choose = (next: Set<number>, f: Found | null = found) => {
    setChosen(next);
    const picked = (f?.keys ?? []).filter((k) => next.has(k.id) && typeof k.parsed !== "string").map((k) => (k.parsed as PublicKey).line);
    onPick(f && picked.length ? { keys: picked.join("\n"), source: `github:${f.user.login}`, count: picked.length } : null);
  };

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="gh-user">GitHub username</Label>
        <div className="flex gap-2">
          <Input
            id="gh-user"
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                void look();
              }
            }}
            placeholder="octocat"
            autoComplete="off"
            spellCheck={false}
            maxLength={60}
            aria-invalid={name.trim() !== "" && !login}
          />
          <Button type="button" size="lg" className="h-9" onClick={look} disabled={!login || busy}>
            {busy ? "Looking…" : "Look up"}
          </Button>
        </div>
        <p className="text-xs text-ink-3">Your browser asks GitHub for the account’s public keys. The keys are copied once; later changes on GitHub don’t reach the server.</p>
      </div>

      {error && (
        <div role="alert" className="text-[0.875rem]">
          <p className="text-danger">{error}</p>
          <button type="button" onClick={onPaste} className="mt-1 text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
            Paste the key instead
          </button>
        </div>
      )}

      {found && (
        <div>
          <div className="flex items-center gap-3 border-t border-rule pt-3">
            {found.user.avatar_url && (
              <img
                src={`${found.user.avatar_url}${found.user.avatar_url.includes("?") ? "&" : "?"}s=64`}
                alt=""
                width={32}
                height={32}
                className="size-8 shrink-0 rounded-full bg-paper-sunk"
                onError={(e) => (e.currentTarget.style.display = "none")}
              />
            )}
            <p className="min-w-0 text-[0.875rem]">
              <span className="block truncate font-[550] text-ink">{found.user.name || found.user.login}</span>
              <span className="block truncate text-[0.8125rem] text-ink-3">@{found.user.login}</span>
            </p>
          </div>
          {found.keys.length === 0 ? (
            <p className="mt-3 text-[0.875rem] text-ink-2">@{found.user.login} has no public keys on GitHub.</p>
          ) : (
            <ul className="mt-3 divide-y divide-rule border-y border-rule">
              {found.keys.map((k) => {
                const bad = typeof k.parsed === "string" ? k.parsed : null;
                const p = bad ? null : (k.parsed as PublicKey);
                return (
                  <li key={k.id}>
                    <label className={cn("flex gap-3 py-2.5", bad ? "cursor-not-allowed opacity-60" : "cursor-pointer")}>
                      <Checkbox
                        className="mt-0.5"
                        checked={chosen.has(k.id)}
                        disabled={!!bad}
                        onCheckedChange={(c) => {
                          const next = new Set(chosen);
                          if (c === true) next.add(k.id);
                          else next.delete(k.id);
                          choose(next);
                        }}
                      />
                      <span className="min-w-0 text-[0.875rem]">
                        <span className="block text-ink">
                          {p ? typeWords(p.type, p.bits) : k.key.split(" ")[0]}
                          {k.fp && (
                            <code className="ident ml-2 text-[0.8125rem] text-ink-3" title={k.fp}>
                              {shortFingerprint(k.fp)}
                            </code>
                          )}
                        </span>
                        <span className="block text-[0.8125rem] text-ink-3">
                          {bad ?? [k.created_at && `added ${relative(k.created_at)}`, k.last_used && `last used ${relative(k.last_used)}`].filter(Boolean).join(" · ")}
                        </span>
                      </span>
                    </label>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
