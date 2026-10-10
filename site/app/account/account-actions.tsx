"use client";
// The account page's buttons and dialogs. Every change goes through
// /api/cloud/boxes/:id/action, which checks the box is yours. Changes
// that need a second look open a native modal <dialog> (showModal: the page
// behind is inert, focus stays inside, Esc closes it).
import { useEffect, useId, useRef, useState } from "react";
import { UNMANAGED } from "@/lib/cloud/billing";
import { endsNowWords, GUARANTEE } from "@/lib/cloud/money";

async function post(url: string, body?: unknown): Promise<{ ok: boolean; message?: string; url?: string }> {
  try {
    const res = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
    const j = (await res.json().catch(() => ({}))) as { ok?: boolean; message?: string; url?: string };
    return { ok: res.ok && j.ok !== false, message: j.message, url: j.url };
  } catch {
    return { ok: false, message: "That didn't work. Try again in a minute." };
  }
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

/** What the card's buttons need to know about a box (the page works it out). */
export type BoxView = {
  id: string;
  name: string | null;
  status: string;
  domain: string | null;
  /** The subscription's extras are on. */
  active: boolean;
  renewable: boolean;
  cancelAtPeriodEnd: boolean;
  periodEnd: string;
  hasSubscription: boolean;
  hasCustomer: boolean;
  /** The first sign-in hand-off, while it lasts: a link we hold, one asked for, or none. */
  signin: "held" | "asked" | "expired" | null;
  signinUntil: string;
  serverType: string | null;
  /** "$12 a month" while a subscription runs; null once it ended. */
  price: string | null;
  sizes: string[];
};

type Msg = { ok: boolean; text: string } | null;
type Act = (body: Record<string, unknown>) => Promise<void>;

/** Sends a box action; on success the page reloads to show the new state. */
function useAct(id: string) {
  const [msg, setMsg] = useState<Msg>(null);
  const [busy, setBusy] = useState(false);
  const act: Act = async (body) => {
    setBusy(true);
    setMsg(null);
    const r = await post(`/api/cloud/boxes/${id}/action`, body);
    setMsg({ ok: r.ok, text: r.message ?? (r.ok ? "Done." : "That didn't work.") });
    if (r.ok) setTimeout(() => window.location.reload(), 1200);
    else setBusy(false);
  };
  return { msg, setMsg, busy, setBusy, act };
}

function Status({ msg }: { msg: Msg }) {
  return msg ? (
    <p className={msg.ok ? "cp-ok" : "cp-err"} role={msg.ok ? "status" : "alert"}>
      {msg.text}
    </p>
  ) : null;
}

/**
 * A modal dialog: native <dialog> opened with showModal(), titled by its
 * heading, closed by Esc or its Go back button (not while a request runs).
 * Its body mounts only while open, so typed text never outlives it. Focus
 * starts on the first field or button inside and returns to the button
 * that opened it.
 */
function Dialog({ open, onClose, title, busy, children }: { open: boolean; onClose: () => void; title: string; busy: boolean; children: React.ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  const id = useId();
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      className="cp-dialog"
      aria-labelledby={`${id}-title`}
      onCancel={(e) => {
        if (busy) e.preventDefault();
      }}
      onClose={onClose}
    >
      {open && (
        <div className="cp-dialog-in">
          <h2 id={`${id}-title`}>{title}</h2>
          {children}
        </div>
      )}
    </dialog>
  );
}

function DialogButtons({ back = "Go back", busy, onBack, children }: { back?: string; busy: boolean; onBack: () => void; children: React.ReactNode }) {
  return (
    <div className="cp-dialog-buttons">
      <button type="button" className="btn btn-quiet btn-sm" disabled={busy} onClick={onBack}>
        {back}
      </button>
      {children}
    </div>
  );
}

function TokenInput({ id, value, onChange, hint }: { id: string; value: string; onChange: (v: string) => void; hint: string }) {
  return (
    <div className="cp-field">
      <label htmlFor={id}>Hetzner API token (Read &amp; Write)</label>
      <input
        id={id}
        type="password"
        className="cp-mono"
        autoComplete="off"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-describedby={`${id}-hint`}
      />
      <p className="cp-hint" id={`${id}-hint`}>
        {hint} Make one in the Hetzner console under your project, Security, API tokens.
      </p>
    </div>
  );
}

const tokenOk = (t: string) => /^[A-Za-z0-9]{20,128}$/.test(t.trim());

/**
 * The main button, the everyday changes, the first sign-in, and the one way
 * out: Cancel subscription, which asks what happens to the server (keep it,
 * or delete it). With no subscription left to cancel, Delete server instead.
 */
export function BoxActions({ box }: { box: BoxView }) {
  const { msg, setMsg, busy, setBusy, act } = useAct(box.id);
  const [open, setOpen] = useState<"" | "resize" | "cancel" | "delete" | "forget">("");
  const close = () => {
    if (busy) return;
    setOpen("");
    setMsg(null);
  };

  const running = box.status === "active";
  const managed = !UNMANAGED.has(box.status);
  const setup = box.status === "paid" || box.status === "failed" || box.status === "provisioning" || box.status === "cert_pending";
  const canResize = running && box.active && box.sizes.length > 0;
  const canCancel = box.hasSubscription && box.active && managed;
  // A server we can delete: set up, or a setup that stopped part way.
  const canDelete = Boolean(box.name) && (running || box.status === "cert_pending" || box.status === "failed");
  const name = box.name ?? "";
  const addr = box.domain ?? "its address";

  // Billing (Stripe's portal) and Renew (Checkout) leave the page.
  async function leave(url: string, body: unknown, fallback: string) {
    setBusy(true);
    setMsg(null);
    const r = await post(url, body);
    if (r.ok && r.url) window.location.href = r.url;
    else {
      setBusy(false);
      setMsg({ ok: false, text: r.message ?? fallback });
    }
  }

  if (!(running || box.renewable || setup || canResize || box.hasCustomer || canCancel || canDelete || box.signin)) return null;
  return (
    <>
      <div className="cp-box-actions">
        {running && (
          <a className="btn btn-primary btn-sm" href={`/api/cloud/boxes/${box.id}/open`}>
            Open dashboard
          </a>
        )}
        {box.renewable && (
          <button
            className={`btn btn-sm ${running ? "btn-quiet" : "btn-primary"}`}
            disabled={busy}
            onClick={() => leave("/api/cloud/checkout", { renew: box.id }, "Payment isn't available right now.")}
          >
            Renew subscription
          </button>
        )}
        {setup && !box.renewable && (
          <a className="btn btn-primary btn-sm" href="/start">
            {box.status === "provisioning" || box.status === "cert_pending" ? "See progress" : "Continue setup"}
          </a>
        )}
        {canResize && (
          <button className="btn btn-quiet btn-sm" disabled={busy} onClick={() => setOpen("resize")}>
            Resize server
          </button>
        )}
        {box.hasCustomer && (
          <button className="btn btn-quiet btn-sm" disabled={busy} onClick={() => leave("/api/cloud/portal", undefined, "Billing isn't available right now.")}>
            Billing and invoices
          </button>
        )}
        {canCancel &&
          (box.cancelAtPeriodEnd ? (
            <button className="btn btn-quiet btn-sm" disabled={busy} onClick={() => act({ action: "resume" })}>
              Keep subscription
            </button>
          ) : (
            <button className="btn btn-quiet btn-sm" disabled={busy} onClick={() => setOpen("cancel")}>
              Cancel subscription
            </button>
          ))}
        {canDelete && (!canCancel || box.cancelAtPeriodEnd) && (
          <button className="btn btn-quiet btn-sm cp-danger-outline" disabled={busy} onClick={() => setOpen("delete")}>
            Delete server…
          </button>
        )}
      </div>

      {box.signin && (
        <div className="cp-signin">
          <p>
            <strong>First sign-in.</strong>{" "}
            {box.signin === "held" ? (
              <>Open dashboard signs you in with a one-time link your box made. It works once, until {box.signinUntil}. We forget it once you&rsquo;ve signed in.</>
            ) : box.signin === "asked" ? (
              "We asked your box for a new one-time sign-in link. It arrives at its next check-in, within about ten minutes; then click Open dashboard again."
            ) : (
              "The one-time sign-in link your box made has expired. Ask for a new one, or sign in on the box itself."
            )}
          </p>
          <div className="cp-row">
            {box.signin === "expired" && (
              <button className="btn btn-quiet btn-sm" disabled={busy} onClick={() => act({ action: "new-signin" })}>
                Get a new sign-in link
              </button>
            )}
            <button className="cp-link" disabled={busy} onClick={() => setOpen("forget")}>
              {box.signin === "held" ? "Forget the sign-in link" : "I’ll sign in on the box"}
            </button>
          </div>
        </div>
      )}

      {!open && <Status msg={msg} />}

      <Dialog open={open === "resize"} onClose={close} title={`Resize ${box.name ?? "this box"}`} busy={busy}>
        <ResizeForm box={box} busy={busy} msg={msg} act={act} onBack={close} />
      </Dialog>

      <CancelDialog open={open === "cancel"} onClose={close} box={box} canDelete={canDelete} busy={busy} msg={msg} setMsg={setMsg} act={act} />

      <Dialog open={open === "delete"} onClose={close} title={`Delete ${name}’s server`} busy={busy}>
        <DeleteForm name={name} addr={addr} price={box.price} busy={busy} msg={msg} act={act} onBack={close} />
      </Dialog>

      <Dialog open={open === "forget"} onClose={close} title="Sign in on the box instead?" busy={busy}>
        <p className="cp-dialog-text">
          We drop the sign-in link we hold and never ask your box for another, so Open dashboard takes you to the box&rsquo;s own sign-in page. Sign in there
          with your passkey. Without one, open the server&rsquo;s console at Hetzner, run{" "}
          <code>tiffin login --home /var/lib/tiffin/platform</code> and open the path it prints on your dashboard&rsquo;s address.
        </p>
        <Status msg={msg} />
        <DialogButtons busy={busy} onBack={close}>
          <button className="btn btn-primary btn-sm" disabled={busy} onClick={() => act({ action: "forget-signin" })}>
            Forget the sign-in link
          </button>
        </DialogButtons>
      </Dialog>
    </>
  );
}

function ResizeForm({ box, busy, msg, act, onBack }: { box: BoxView; busy: boolean; msg: Msg; act: Act; onBack: () => void }) {
  const id = useId();
  const [size, setSize] = useState(box.sizes[0] ?? "");
  const [token, setToken] = useState("");
  const ready = Boolean(size) && tokenOk(token);
  return (
    <form
      className="cp-dialog-form"
      onSubmit={(e) => {
        e.preventDefault();
        if (ready && !busy) act({ action: "resize", serverType: size, token: token.trim() });
      }}
    >
      <div className="cp-field">
        <label htmlFor={`${id}-size`}>New size (now {box.serverType})</label>
        <select id={`${id}-size`} value={size} onChange={(e) => setSize(e.target.value)}>
          {box.sizes.map((s) => (
            <option key={s}>{s}</option>
          ))}
        </select>
        <p className="cp-hint">
          The server restarts and is offline for about 2 minutes. Hetzner bills the new size from then. A server&rsquo;s disk can&rsquo;t shrink, so Hetzner may
          refuse a smaller size.
        </p>
      </div>
      <TokenInput id={`${id}-token`} value={token} onChange={setToken} hint="Used for this resize, then forgotten." />
      <Status msg={msg} />
      <DialogButtons busy={busy} onBack={onBack}>
        <button type="submit" className="btn btn-primary btn-sm" disabled={busy || !ready}>
          {busy ? "Resizing…" : `Resize to ${size}`}
        </button>
      </DialogButtons>
    </form>
  );
}

/**
 * Cancel subscription: one dialog, two choices, both of which cancel. Keep the
 * server (the default) cancels at the period's end; Delete the server goes on
 * to the delete form (a Hetzner token and the typed name), which ends the
 * subscription now. A box with no server to delete gets the first only.
 */
function CancelDialog({
  open,
  onClose,
  box,
  canDelete,
  busy,
  msg,
  setMsg,
  act,
}: {
  open: boolean;
  onClose: () => void;
  box: BoxView;
  canDelete: boolean;
  busy: boolean;
  msg: Msg;
  setMsg: (m: Msg) => void;
  act: Act;
}) {
  const id = useId();
  const [choice, setChoice] = useState<"keep" | "delete">("keep");
  const [step, setStep] = useState<"choose" | "delete">("choose");
  const body = useRef<HTMLElement>(null);
  const moved = useRef(false);
  useEffect(() => {
    if (open) {
      setChoice("keep");
      setStep("choose");
    }
  }, [open]);
  // Between the two steps, focus goes to the new step's first field (showModal places it on opening).
  useEffect(() => {
    if (!moved.current) return;
    moved.current = false;
    body.current?.querySelector<HTMLInputElement>(step === "choose" ? "input:checked" : "input")?.focus();
  }, [step]);
  const go = (to: "choose" | "delete") => {
    setMsg(null);
    moved.current = true;
    setStep(to);
  };
  const name = box.name ?? "";
  const addr = box.domain ?? "The address";
  const end = box.periodEnd || "the last day you paid for";
  const keep = (
    <ul className="cp-dialog-list">
      <li>The subscription ends on {end}. Until then nothing changes.</li>
      <li>After that, the server and apps keep running in your Hetzner account. Updates, monitoring and off-site backups stop.</li>
      <li>{addr} goes 30 days later.</li>
      <li>You can undo this until {end}.</li>
    </ul>
  );
  if (step === "delete") {
    return (
      <Dialog open={open} onClose={onClose} title={`Delete ${name}’s server`} busy={busy}>
        <div ref={body as React.RefObject<HTMLDivElement>}>
          <DeleteForm name={name} addr={box.domain ?? "its address"} price={box.price} busy={busy} msg={msg} act={act} onBack={() => !busy && go("choose")} />
        </div>
      </Dialog>
    );
  }
  return (
    <Dialog open={open} onClose={onClose} title="Cancel your subscription?" busy={busy}>
      <form
        ref={body as React.RefObject<HTMLFormElement>}
        className="cp-dialog-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (busy) return;
          if (canDelete && choice === "delete") go("delete");
          else act({ action: "cancel" });
        }}
      >
        <p className="cp-dialog-text">
          {canDelete ? "Both choices cancel" : "This cancels"} your ShipTiffin subscription{box.price ? ` (${box.price})` : ""}.
        </p>
        {canDelete ? (
          <fieldset className="cp-choices">
            <legend>What happens to the server?</legend>
            <label className="cp-option">
              <input type="radio" name={`${id}-choice`} value="keep" checked={choice === "keep"} onChange={() => setChoice("keep")} />
              <b>Keep the server</b>
              {keep}
            </label>
            <label className="cp-option" data-tone="danger">
              <input type="radio" name={`${id}-choice`} value="delete" checked={choice === "delete"} onChange={() => setChoice("delete")} />
              <b>Delete the server</b>
              <ul className="cp-dialog-list">
                <li>The server is deleted now, with its data if you choose, and {box.domain ?? "its address"} is removed.</li>
                <li>The subscription ends now. The month already paid isn&rsquo;t refunded.</li>
                <li>Next, you paste a Hetzner token and type the box&rsquo;s name.</li>
              </ul>
            </label>
          </fieldset>
        ) : (
          keep
        )}
        <Status msg={msg} />
        <DialogButtons back="Don’t cancel" busy={busy} onBack={onClose}>
          {canDelete && choice === "delete" ? (
            <button type="submit" className="btn btn-quiet btn-sm cp-danger-outline" disabled={busy}>
              Delete the server…
            </button>
          ) : (
            <button type="submit" className="btn btn-quiet btn-sm cp-danger-outline" disabled={busy}>
              {busy ? "Cancelling…" : "Cancel subscription"}
            </button>
          )}
        </DialogButtons>
      </form>
    </Dialog>
  );
}

type FormProps = { name: string; addr: string; price: string | null; busy: boolean; msg: Msg; act: Act; onBack: () => void };

/** The money, where it can't be missed (above the name field and the button): the subscription ends now, no refund, the guarantee. */
function Money({ price }: { price: string | null }) {
  const w = endsNowWords(price);
  return (
    <div className="cp-money" role="note">
      <svg className="cp-money-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false">
        <path d="M10 2.5 18.5 17h-17z" />
        <path d="M10 8v4.2M10 14.6v.1" />
      </svg>
      <div>
        <p>
          <strong>{w.title}</strong> {w.body}
        </p>
        {price && <p className="cp-money-small">{GUARANTEE}</p>}
      </div>
    </div>
  );
}

function TypeName({ id, name, value, onChange }: { id: string; name: string; value: string; onChange: (v: string) => void }) {
  return (
    <div className="cp-field">
      <label htmlFor={id}>
        Type <span className="cp-mono">{name}</span> to confirm
      </label>
      <input id={id} type="text" value={value} onChange={(e) => onChange(e.target.value)} autoComplete="off" autoCapitalize="none" spellCheck={false} />
    </div>
  );
}

function DeleteForm({ name, addr, price, busy, msg, act, onBack }: FormProps) {
  const id = useId();
  const [typed, setTyped] = useState("");
  const [token, setToken] = useState("");
  const [data, setData] = useState(false);
  const ready = typed.trim() === name && tokenOk(token);
  return (
    <form
      className="cp-dialog-form"
      onSubmit={(e) => {
        e.preventDefault();
        if (ready && !busy) act({ action: "delete-server", confirm: typed.trim(), token: token.trim(), deleteData: data });
      }}
    >
      <ul className="cp-dialog-list">
        <li>{addr} is removed first.</li>
        <li>
          Then the server and its firewall are deleted: only what carries this box&rsquo;s <span className="cp-mono">shiptiffin-box</span> label.
        </li>
        <li>The data volume stays unless you tick the box below.</li>
        <li>Off-site backups are deleted after 7 days.</li>
      </ul>
      <TokenInput id={`${id}-token`} value={token} onChange={setToken} hint="We delete only with a token you paste now; it is used for this, then forgotten." />
      <label className="cp-check">
        <input type="checkbox" checked={data} onChange={(e) => setData(e.target.checked)} />
        <span>Also delete the data volume: everything your apps stored. This can&rsquo;t be undone.</span>
      </label>
      <Money price={price} />
      <TypeName id={`${id}-name`} name={name} value={typed} onChange={setTyped} />
      <Status msg={msg} />
      <DialogButtons busy={busy} onBack={onBack}>
        <button type="submit" className="btn btn-sm btn-danger" disabled={busy || !ready}>
          {busy ? "Deleting…" : data ? `Delete ${name}’s server and data` : `Delete ${name}’s server`}
        </button>
      </DialogButtons>
    </form>
  );
}

/** A delete that stopped: paste a key and try again (the address is already gone; the rest picks up where it stopped). */
export function RetryDelete({ id, name, domain }: { id: string; name: string; domain: string }) {
  const { msg, setMsg, busy, act } = useAct(id);
  const [open, setOpen] = useState(false);
  const close = () => {
    if (busy) return;
    setOpen(false);
    setMsg(null);
  };
  return (
    <>
      <div className="cp-row">
        <button className="btn btn-quiet btn-sm cp-danger-outline" onClick={() => setOpen(true)}>
          Try again…
        </button>
      </div>
      <Dialog open={open} onClose={close} title={`Delete ${name}’s server`} busy={busy}>
        <DeleteForm name={name} addr={domain} price={null} busy={busy} msg={msg} act={act} onBack={close} />
      </Dialog>
    </>
  );
}

/** Past invoices (Stripe's billing portal), for an account whose boxes are all gone. */
export function PastInvoices() {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  return (
    <>
      <button
        className="cp-link cp-quiet-link"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          setErr("");
          const r = await post("/api/cloud/portal");
          if (r.ok && r.url) window.location.href = r.url;
          else {
            setBusy(false);
            setErr(r.message ?? "Billing isn't available right now.");
          }
        }}
      >
        Past invoices
      </button>
      {err && (
        <span className="cp-err" role="alert">
          {err}
        </span>
      )}
    </>
  );
}
