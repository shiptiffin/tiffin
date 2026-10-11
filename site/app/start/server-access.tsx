"use client";
// /start's "Keep a way into your server": the owner's SSH public keys, from
// GitHub (the browser asks api.github.com itself; our server never does),
// pasted, or already in the Hetzner project. Every key is parsed and
// fingerprinted here, the owner ticks what goes on the server, and exactly
// those key texts go with Create. Or, on purpose, none.
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { fromGitHub, githubUserOf, preselect, type GitHubProfile } from "@/lib/cloud/github-keys";
import type { HetznerKey } from "@/lib/cloud/hetzner";
import { fingerprint, GITHUB_USER, keyKind, KeyError, parseKeys, parseLine, shortFingerprint, sourceWords, type KeyArg, type KeySource } from "@/lib/cloud/sshkey";

/** One key as the page shows it: parsed and fingerprinted, or why it can't be used. */
type Row = {
  id: string;
  line: string;
  kind: string;
  fp: string;
  /** A name: the Hetzner key's (GitHub's keys carry none). */
  title?: string;
  added?: string | null;
  lastUsed?: string | null;
  problem?: string;
};

/** A key that goes on the server, with what the page told the owner about it. */
export type Chosen = KeyArg & { kind: string; fp: string };

export type Access = {
  keys: Chosen[];
  /** The owner chose to continue without SSH. */
  skipped: boolean;
  /** Why Create should wait (a pasted key that doesn't parse), or null. */
  blocked: string | null;
};

const said = (e: unknown) => {
  const m = e instanceof KeyError ? e.message : "not an SSH public key";
  return m[0]!.toUpperCase() + m.slice(1) + ".";
};

/** Parses and fingerprints each key text; a key that doesn't parse keeps its reason. */
async function rows(items: { id: string; text: string; title?: string; added?: string | null; lastUsed?: string | null }[]): Promise<Row[]> {
  return Promise.all(
    items.map(async (it) => {
      try {
        const k = parseLine(it.text);
        return { ...it, line: k.line, kind: keyKind(k.type, k.bits), fp: await fingerprint(k), title: it.title ?? (k.comment || undefined) };
      } catch (e) {
        return { ...it, line: "", kind: it.text.split(/\s+/)[0]?.slice(0, 40) ?? "", fp: "", problem: said(e) };
      }
    }),
  );
}

const day = (iso: string) => new Date(iso).toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });

type Mode = "github" | "paste" | "hetzner";

export function ServerAccess({ hetzner, onChange }: { hetzner: HetznerKey[]; onChange: (a: Access) => void }) {
  const id = useId();
  const [mode, setMode] = useState<Mode>("github");
  const [skipAsk, setSkipAsk] = useState(false);
  const [skipped, setSkipped] = useState(false);
  const [gh, setGh] = useState<{ user: string; rows: Row[]; picked: Set<string> } | null>(null);
  const [paste, setPaste] = useState<{ rows: Row[]; error: string | null }>({ rows: [], error: null });
  const [hz, setHz] = useState<{ rows: Row[]; picked: Set<string> }>({ rows: [], picked: new Set() });

  // The project's keys, parsed once. None is ticked for you: they may be old, or someone else's.
  useEffect(() => {
    let stop = false;
    rows(hetzner.map((k, i) => ({ id: `hz-${i}`, text: k.public_key, title: k.name }))).then((r) => {
      if (!stop) setHz({ rows: r, picked: new Set() });
    });
    return () => {
      stop = true;
    };
  }, [hetzner]);

  const keys = useMemo(() => {
    const out: Chosen[] = [];
    const seen = new Set<string>();
    const add = (r: Row, source: KeySource) => {
      if (r.problem || seen.has(r.line)) return;
      seen.add(r.line);
      out.push({ key: r.line, source, kind: r.kind, fp: r.fp });
    };
    if (gh) for (const r of gh.rows) if (gh.picked.has(r.id)) add(r, `github:${gh.user}`);
    for (const r of paste.rows) add(r, "pasted");
    for (const r of hz.rows) if (hz.picked.has(r.id)) add(r, "hetzner");
    return out.slice(0, 10);
  }, [gh, paste, hz]);

  const blocked = paste.error ? "Fix the pasted key, or clear it." : null;
  const last = useRef("");
  useEffect(() => {
    const a: Access = { keys: skipped ? [] : keys, skipped, blocked: skipped ? null : blocked };
    const s = JSON.stringify(a);
    if (s === last.current) return;
    last.current = s;
    onChange(a);
  }, [keys, skipped, blocked, onChange]);

  if (skipped) {
    return (
      <section className="cp-card sa" aria-labelledby={`${id}-title`}>
        <h2 id={`${id}-title`}>Keep a way into your server</h2>
        <p className="sa-skipped">
          Continuing without SSH. You can add a key later in your dashboard, under Settings › Server access.{" "}
          <button type="button" className="cp-link" onClick={() => setSkipped(false)}>
            Add a key now
          </button>
        </p>
      </section>
    );
  }

  const modes: [Mode, string][] = [
    ["github", "GitHub"],
    ["paste", "Paste a key"],
    ...(hetzner.length ? ([["hetzner", "From Hetzner"]] as [Mode, string][]) : []),
  ];
  return (
    <section className="cp-card sa" aria-labelledby={`${id}-title`}>
      <h2 id={`${id}-title`}>Keep a way into your server</h2>
      <p className="sa-lede">
        Add your computer&rsquo;s SSH key. You can then log in to your server, and get back into your dashboard if you&rsquo;re ever signed out.
        ShipTiffin never holds a key.
      </p>

      <div className="sa-seg" role="radiogroup" aria-label="Where your key comes from">
        {modes.map(([m, label]) => (
          <label key={m} className="sa-seg-item">
            <input type="radio" name={`${id}-mode`} value={m} checked={mode === m} onChange={() => setMode(m)} />
            <span>{label}</span>
          </label>
        ))}
      </div>

      {/* All three stay mounted, so switching back finds what was typed or ticked. */}
      <GitHub hidden={mode !== "github"} value={gh} onChange={setGh} onPaste={() => setMode("paste")} />
      <Paste hidden={mode !== "paste"} onChange={setPaste} />
      {hetzner.length > 0 && <Hetzner hidden={mode !== "hetzner"} value={hz} onChange={setHz} />}

      {keys.length > 0 && (
        <div className="sa-chosen" aria-live="polite">
          <p className="cp-label">
            Goes on your server <span className="cp-count">{keys.length === 1 ? "1 key" : `${keys.length} keys`}</span>
          </p>
          <ul>
            {keys.map((k) => (
              <li key={k.key}>
                <span>{k.kind}</span> <span className="cp-mono">{shortFingerprint(k.fp)}</span>{" "}
                <span className="cp-muted">{sourceWords(k.source)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      <details className="cp-fold">
        <summary>What this does</summary>
        <ul className="sa-list">
          <li>Your key logs in as root, the server&rsquo;s administrator. Passwords never work, so anyone without one of these keys stays out.</li>
          <li>SSH stays open to keys only. Failed attempts are slowed down, and addresses that keep trying are banned.</li>
          <li>Remove or add keys any time in your dashboard, under Settings › Server access.</li>
          <li>
            Only the public half of a key leaves this page. The private half stays on your computer, so ShipTiffin can&rsquo;t use your key to get in. We
            keep its fingerprint to show you what was added.
          </li>
        </ul>
      </details>

      <div className="sa-skip">
        {!skipAsk ? (
          <button type="button" className="cp-link sa-quiet" onClick={() => setSkipAsk(true)}>
            Continue without SSH
          </button>
        ) : (
          <div className="sa-skip-ask" role="group" aria-label="Continue without SSH">
            <p>If you lose your passkey, getting back in means Hetzner&rsquo;s console. You can add a key later in your dashboard.</p>
            <div className="cp-row">
              <button type="button" className="btn btn-quiet btn-sm" onClick={() => setSkipped(true)}>
                Continue without SSH
              </button>
              <button type="button" className="cp-link" onClick={() => setSkipAsk(false)}>
                Add a key
              </button>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

type GitHubState = { user: string; rows: Row[]; picked: Set<string> } | null;
function GitHub({ hidden, value, onChange, onPaste }: { hidden: boolean; value: GitHubState; onChange: (v: GitHubState) => void; onPaste: () => void }) {
  const id = useId();
  const [typed, setTyped] = useState(value?.user ?? "");
  const [profile, setProfile] = useState<GitHubProfile | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const asked = useRef(0);
  const user = githubUserOf(typed);
  const valid = GITHUB_USER.test(user);

  async function find() {
    if (!valid || busy) return;
    const n = ++asked.current;
    setBusy(true);
    setError("");
    onChange(null);
    const r = await fromGitHub(user);
    if (n !== asked.current) return;
    setBusy(false);
    if (!r.ok) {
      setProfile(null);
      setError(r.message);
      return;
    }
    // GitHub's dates: null for a key never used; missing altogether, nothing is said.
    const list = await rows(r.keys.map((k) => ({ id: `gh-${k.id}`, text: k.key, added: k.created_at, lastUsed: k.last_used })));
    if (n !== asked.current) return;
    setProfile(r.profile);
    const first = preselect(list);
    onChange({ user: r.profile.login, rows: list, picked: new Set(first ? [first] : []) });
  }

  const toggle = (rid: string) => {
    if (!value) return;
    const picked = new Set(value.picked);
    if (picked.has(rid)) picked.delete(rid);
    else picked.add(rid);
    onChange({ ...value, picked });
  };

  return (
    <div className="sa-panel" hidden={hidden}>
      {!value || !profile ? (
        <div className="cp-field">
          <label htmlFor={`${id}-user`}>Your GitHub username</label>
          <div className="sa-inline">
            <input
              id={`${id}-user`}
              type="text"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={(e) => {
                // Enter looks the name up; it never submits the page's Create form.
                if (e.key === "Enter") {
                  e.preventDefault();
                  find();
                }
              }}
              placeholder="octocat"
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              maxLength={80}
              aria-describedby={`${id}-hint`}
            />
            <button type="button" className="btn btn-quiet" disabled={!valid || busy} onClick={find}>
              {busy ? "Asking GitHub…" : "Find my keys"}
            </button>
          </div>
          {error ? (
            <div className="sa-problem" role="alert">
              <p className="cp-err">{error} Paste your key instead: it works the same.</p>
              <button type="button" className="btn btn-quiet btn-sm" onClick={onPaste}>
                Paste a key
              </button>
            </div>
          ) : (
            <p className="cp-hint" id={`${id}-hint`}>
              Your browser asks GitHub for the public keys on your profile (github.com/{valid ? user : "you"}.keys). Our server never contacts GitHub.
            </p>
          )}
        </div>
      ) : (
        <>
          <div className="sa-who">
            {profile.avatar ? <img src={profile.avatar} alt="" width={40} height={40} referrerPolicy="no-referrer" /> : <span className="sa-who-blank" aria-hidden="true" />}
            <div>
              <b>{profile.name || profile.login}</b>
              <span className="cp-muted">@{profile.login}</span>
            </div>
            <button
              type="button"
              className="cp-link sa-who-change"
              onClick={() => {
                asked.current++;
                setProfile(null);
                onChange(null);
              }}
            >
              Not you?
            </button>
          </div>
          {value.rows.length === 0 ? (
            <div className="sa-problem" role="alert">
              <p className="cp-err">@{profile.login} has no SSH keys on GitHub. Paste your key instead.</p>
              <button type="button" className="btn btn-quiet btn-sm" onClick={onPaste}>
                Paste a key
              </button>
            </div>
          ) : (
            <>
              <fieldset className="sa-keys">
                <legend className="cp-label">Tick the keys of computers you use</legend>
                {value.rows.map((r) => (
                  <KeyRow key={r.id} r={r} checked={value.picked.has(r.id)} onToggle={() => toggle(r.id)}>
                    {r.added ? `Added ${day(r.added)}` : ""}
                    {r.added && r.lastUsed !== undefined ? " · " : ""}
                    {r.lastUsed ? `last used ${day(r.lastUsed)}` : r.lastUsed === null ? "never used" : ""}
                  </KeyRow>
                ))}
              </fieldset>
              <p className="cp-hint">Whoever holds the private half of a ticked key can log in to your server as root.</p>
            </>
          )}
        </>
      )}
    </div>
  );
}

function KeyRow({ r, checked, onToggle, children }: { r: Row; checked: boolean; onToggle: () => void; children?: React.ReactNode }) {
  return (
    <label className="cp-option sa-key" data-problem={r.problem ? "" : undefined}>
      <input type="checkbox" checked={checked && !r.problem} disabled={Boolean(r.problem)} onChange={onToggle} />
      <b>
        {r.title ? `${r.title} · ` : ""}
        {r.kind}
      </b>
      {r.fp && <span className="cp-mono sa-fp">{shortFingerprint(r.fp)}</span>}
      {(r.problem || children) && <small>{r.problem ?? children}</small>}
    </label>
  );
}

function Paste({ hidden, onChange }: { hidden: boolean; onChange: (v: { rows: Row[]; error: string | null }) => void }) {
  const id = useId();
  const [text, setText] = useState("");
  const [shown, setShown] = useState<{ rows: Row[]; error: string | null }>({ rows: [], error: null });
  useEffect(() => {
    let stop = false;
    const t = text.trim();
    if (!t) {
      setShown({ rows: [], error: null });
      onChange({ rows: [], error: null });
      return;
    }
    let keys;
    try {
      keys = parseKeys(t);
    } catch (e) {
      const v = { rows: [], error: said(e) };
      setShown(v);
      onChange(v);
      return;
    }
    rows(keys.map((k, i) => ({ id: `p-${i}`, text: k.line, title: k.comment || undefined }))).then((r) => {
      if (stop) return;
      const v = { rows: r, error: null };
      setShown(v);
      onChange(v);
    });
    return () => {
      stop = true;
    };
  }, [text, onChange]);

  return (
    <div className="sa-panel" hidden={hidden}>
      <div className="cp-field">
        <label htmlFor={`${id}-key`}>Your public key</label>
        <textarea
          id={`${id}-key`}
          className="cp-mono sa-paste"
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder="ssh-ed25519 AAAA… you@laptop"
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          rows={3}
          aria-invalid={shown.error ? true : undefined}
          aria-describedby={`${id}-hint`}
        />
        {shown.error ? (
          <p className="cp-err" role="alert" id={`${id}-hint`}>
            {shown.error}
          </p>
        ) : shown.rows.length ? (
          <p className="cp-ok" id={`${id}-hint`}>
            {shown.rows.length === 1 ? "That's a public key." : `That's ${shown.rows.length} public keys.`}
          </p>
        ) : (
          <p className="cp-hint" id={`${id}-hint`}>
            Copy yours on a Mac: <code>pbcopy &lt; ~/.ssh/id_ed25519.pub</code>
          </p>
        )}
      </div>
      <details className="cp-fold">
        <summary>Don&rsquo;t have one?</summary>
        <p className="cp-hint">Make one in Terminal (press Enter at each question), then copy it as above:</p>
        <p>
          <code>ssh-keygen -t ed25519</code>
        </p>
        <p className="cp-hint">
          On Linux, <code>cat ~/.ssh/id_ed25519.pub</code> prints the line to copy. Paste only the file that ends in .pub, never the private key next to it.
        </p>
      </details>
    </div>
  );
}

function Hetzner({ hidden, value, onChange }: { hidden: boolean; value: { rows: Row[]; picked: Set<string> }; onChange: (v: { rows: Row[]; picked: Set<string> }) => void }) {
  const toggle = (rid: string) => {
    const picked = new Set(value.picked);
    if (picked.has(rid)) picked.delete(rid);
    else picked.add(rid);
    onChange({ ...value, picked });
  };
  return (
    <div className="sa-panel" hidden={hidden}>
      <fieldset className="sa-keys">
        <legend className="cp-label">Keys already in your Hetzner project</legend>
        {value.rows.map((r) => (
          <KeyRow key={r.id} r={r} checked={value.picked.has(r.id)} onToggle={() => toggle(r.id)} />
        ))}
      </fieldset>
      <p className="cp-hint">Whoever holds the private half of a ticked key can log in to your server as root.</p>
    </div>
  );
}
