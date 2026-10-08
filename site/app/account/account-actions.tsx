"use client";
// The account page's buttons. Every change goes through
// /api/cloud/boxes/:id/action, which checks the box is yours.
import { useState } from "react";

async function post(url: string, body?: unknown): Promise<{ ok: boolean; message?: string; url?: string }> {
  try {
    const res = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
    const j = (await res.json().catch(() => ({}))) as { ok?: boolean; message?: string; url?: string };
    return { ok: res.ok && j.ok !== false, message: j.message, url: j.url };
  } catch {
    return { ok: false, message: "That didn't work. Try again in a minute." };
  }
}

export function BillingButton() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <>
      <button
        className="btn btn-quiet"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          const r = await post("/api/cloud/portal");
          if (r.ok && r.url) window.location.href = r.url;
          else {
            setError(r.message ?? "Billing isn't available right now.");
            setBusy(false);
          }
        }}
      >
        Billing and invoices
      </button>
      {error && <span className="cp-err">{error}</span>}
    </>
  );
}

export function SignOut() {
  return (
    <button
      className="cp-link"
      onClick={async () => {
        await fetch("/api/auth/sign-out", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" }).catch(() => {});
        window.location.href = "/";
      }}
    >
      Sign out
    </button>
  );
}

type Box = {
  id: string;
  name: string | null;
  status: string;
  active: boolean;
  cancelAtPeriodEnd: boolean;
  hasSubscription: boolean;
  keyStored: boolean;
  signinLink: boolean;
  handoffOpen: boolean;
  renewable: boolean;
  serverType: string | null;
  sizes: string[];
};

export function BoxActions({ box }: { box: Box }) {
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState<"" | "resize" | "key" | "release" | "delete">("");

  async function act(body: Record<string, unknown>, confirmText?: string) {
    if (confirmText && !window.confirm(confirmText)) return;
    setBusy(true);
    setMsg(null);
    const r = await post(`/api/cloud/boxes/${box.id}/action`, body);
    setBusy(false);
    setMsg({ ok: r.ok, text: r.message ?? (r.ok ? "Done." : "That didn't work.") });
    if (r.ok) setTimeout(() => window.location.reload(), 1200);
  }

  const running = box.status === "active";
  return (
    <div>
      <div className="cp-row">
        {running && (
          <a className="btn btn-primary btn-sm" href={`/api/cloud/boxes/${box.id}/open`}>
            Open dashboard
          </a>
        )}
        {box.renewable && (
          <button
            className="btn btn-primary btn-sm"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              const r = await post("/api/cloud/checkout", { renew: box.id });
              if (r.ok && r.url) window.location.href = r.url;
              else {
                setBusy(false);
                setMsg({ ok: false, text: r.message ?? "Payment isn't available right now." });
              }
            }}
          >
            Renew the subscription
          </button>
        )}
        {(box.status === "paid" || box.status === "failed" || box.status === "provisioning" || box.status === "cert_pending") && (
          <a className="btn btn-primary btn-sm" href="/start">
            {box.status === "provisioning" || box.status === "cert_pending" ? "See progress" : "Continue setup"}
          </a>
        )}
        {running && box.active && box.sizes.length > 0 && (
          <button className="btn btn-quiet btn-sm" onClick={() => setOpen(open === "resize" ? "" : "resize")}>
            Resize
          </button>
        )}
        {box.status !== "released" && box.status !== "deleting" && (
          <button className="btn btn-quiet btn-sm" onClick={() => setOpen(open === "key" ? "" : "key")}>
            Hetzner key
          </button>
        )}
        {box.hasSubscription && box.active && box.status !== "released" && (
          <button
            className="btn btn-quiet btn-sm"
            disabled={busy}
            onClick={() =>
              box.cancelAtPeriodEnd
                ? act({ action: "resume" })
                : act({ action: "cancel" }, "Cancel at the end of this period? Your server and apps keep running; updates and the extras stop, and the address stays 30 days.")
            }
          >
            {box.cancelAtPeriodEnd ? "Keep subscription" : "Cancel subscription"}
          </button>
        )}
        {box.status !== "released" && box.status !== "deleting" && box.status !== "awaiting_payment" && box.name && (
          <>
            <button className="cp-link" onClick={() => setOpen(open === "release" ? "" : "release")}>
              Release from ShipTiffin
            </button>
            {box.status === "active" || box.status === "cert_pending" || box.status === "failed" ? (
              <button className="cp-link cp-danger" onClick={() => setOpen(open === "delete" ? "" : "delete")}>
                Delete the server
              </button>
            ) : null}
          </>
        )}
      </div>

      {open === "resize" && <Resize box={box} busy={busy} act={act} />}
      {open === "key" && <KeyPanel box={box} busy={busy} act={act} />}
      {open === "release" && (
        <Confirm
          name={box.name!}
          busy={busy}
          button="Release"
          text="We stop managing this box: the subscription ends now, its shiptiffin.app address goes, and we keep no key or sign-in link. The server and apps in your Hetzner project are untouched and keep running; it no longer gets updates from us."
          onConfirm={(confirm) => act({ action: "release", confirm })}
        />
      )}
      {open === "delete" && <DeleteServer box={box} busy={busy} act={act} />}
      {msg && (
        <p className={msg.ok ? "cp-ok" : "cp-err"} role="status">
          {msg.text}
        </p>
      )}
    </div>
  );
}

type Act = (body: Record<string, unknown>, confirmText?: string) => Promise<void>;

function TokenInput({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  return (
    <div className="cp-field">
      <label>{label}</label>
      <input type="password" className="cp-mono" autoComplete="off" spellCheck={false} value={value} onChange={(e) => onChange(e.target.value)} placeholder="Paste a Hetzner API token (Read & Write)" />
    </div>
  );
}

function Resize({ box, busy, act }: { box: Box; busy: boolean; act: Act }) {
  const [size, setSize] = useState(box.sizes[0] ?? "");
  const [token, setToken] = useState("");
  const [keep, setKeep] = useState(false);
  return (
    <div className="cp-card">
      <div className="cp-field">
        <label htmlFor={`size-${box.id}`}>New size (now {box.serverType})</label>
        <select id={`size-${box.id}`} value={size} onChange={(e) => setSize(e.target.value)}>
          {box.sizes.map((s) => (
            <option key={s}>{s}</option>
          ))}
        </select>
        <p className="cp-hint">The server restarts: about 2 minutes offline. Hetzner bills the new size from then. A server&rsquo;s disk can&rsquo;t shrink, so smaller sizes may be refused.</p>
      </div>
      {!box.keyStored && (
        <>
          <TokenInput value={token} onChange={setToken} label="Hetzner key (used for this resize, then forgotten)" />
          <label className="cp-check">
            <input type="checkbox" checked={keep} onChange={(e) => setKeep(e.target.checked)} />
            <span>Keep it for one-click resizes next time</span>
          </label>
        </>
      )}
      <button
        className="btn btn-primary btn-sm"
        disabled={busy || !size || (!box.keyStored && token.trim().length < 20)}
        onClick={() => act({ action: "resize", serverType: size, token: token.trim() || undefined, keepKey: keep }, `Resize ${box.name} to ${size}? It is offline for about 2 minutes.`)}
      >
        Resize to {size}
      </button>
    </div>
  );
}

function KeyPanel({ box, busy, act }: { box: Box; busy: boolean; act: Act }) {
  const [token, setToken] = useState("");
  return (
    <div className="cp-card">
      {box.keyStored ? (
        <>
          <p className="cp-sub">Your Hetzner key is stored, sealed so that only our setup worker can open it (not this website). We use it only when you click Resize.</p>
          <button className="btn btn-quiet btn-sm" disabled={busy} onClick={() => act({ action: "forget-key" })}>
            Remove the stored key
          </button>
        </>
      ) : (
        <>
          <p className="cp-sub">We don&rsquo;t hold a Hetzner key for this box. Store one for one-click resizes (we check it first, and list the check in the log):</p>
          <TokenInput value={token} onChange={setToken} label="Hetzner key" />
          <button className="btn btn-quiet btn-sm" disabled={busy || token.trim().length < 20} onClick={() => act({ action: "keep-key", token: token.trim() })}>
            Store it, encrypted
          </button>
        </>
      )}
      {box.handoffOpen && (
        <>
          <p className="cp-hint">
            {box.signinLink
              ? "We also hold a one-time sign-in link your box made (it works once, for 24 hours), so \u201cOpen dashboard\u201d signs you in. We forget it once your box tells us you signed in."
              : "Your box's one-time sign-in link expired. Until you first sign in, your box makes a new one when you ask (no SSH: it sends it at its next check-in)."}
          </p>
          <div className="cp-row">
            {!box.signinLink && (
              <button className="btn btn-quiet btn-sm" disabled={busy} onClick={() => act({ action: "new-signin" })}>
                Get a new sign-in link
              </button>
            )}
            <button
              className="btn btn-quiet btn-sm"
              disabled={busy}
              onClick={() => act({ action: "forget-signin" }, "Forget the sign-in link, and never ask your box for another? You then sign in on the box itself.")}
            >
              {box.signinLink ? "Forget the sign-in link" : "I'll sign in on the box"}
            </button>
          </div>
        </>
      )}
      <p className="cp-hint">You can also delete the token in Hetzner (Security → API tokens) at any time; the box keeps running.</p>
    </div>
  );
}

function Confirm({ name, text, button, busy, onConfirm, children }: { name: string; text: string; button: string; busy: boolean; onConfirm: (confirm: string) => void; children?: React.ReactNode }) {
  const [typed, setTyped] = useState("");
  return (
    <div className="cp-card">
      <p className="cp-sub">{text}</p>
      {children}
      <div className="cp-field">
        <label>
          Type <strong>{name}</strong> to confirm
        </label>
        <input type="text" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
      </div>
      <button className="btn btn-quiet btn-sm cp-danger" disabled={busy || typed.trim() !== name} onClick={() => onConfirm(typed.trim())}>
        {button}
      </button>
    </div>
  );
}

function DeleteServer({ box, busy, act }: { box: Box; busy: boolean; act: Act }) {
  const [token, setToken] = useState("");
  const [data, setData] = useState(false);
  return (
    <Confirm
      name={box.name!}
      busy={busy || token.trim().length < 20}
      button="Delete the server"
      text="This removes the box's shiptiffin.app address, then deletes the server, its firewall and anything else we made for this box in your Hetzner project (only what carries its shiptiffin-box label), and ends the subscription. We only do it with a key you paste now. The data volume stays unless you tick the box: it is your apps' data."
      onConfirm={(confirm) => act({ action: "delete-server", confirm, token: token.trim(), deleteData: data })}
    >
      <TokenInput value={token} onChange={setToken} label="Hetzner key (used for this, then forgotten)" />
      <label className="cp-check">
        <input type="checkbox" checked={data} onChange={(e) => setData(e.target.checked)} />
        <span>Also delete the data volume (everything your apps stored). This can&rsquo;t be undone.</span>
      </label>
    </Confirm>
  );
}
