"use client";
// The steps of /start that happen in the browser: connect Hetzner (the key is
// checked, and stays in this page's memory until setup starts), choose (name,
// size, place and a way into the server), pay (Stripe's form inside this
// page, so the page and the key are never left), watch, open. Nobody pays
// before they have seen what their Hetzner account can order and what it costs.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { nameProblem, ZONE } from "@/lib/cloud/names";
import type { CheckResult, HetznerKey, Option } from "@/lib/cloud/hetzner";
import { Progress, Stopped, type Job } from "./progress";
import { ServerAccess, type Access } from "./server-access";

const STEPS = ["Account", "Connect Hetzner", "Pay", "Create", "Open"];

export function Steps({ now }: { now: number }) {
  return (
    <ol className="cp-steps" aria-label="Steps">
      {STEPS.map((s, i) => (
        <li key={s} data-state={i < now ? "done" : i === now ? "now" : "next"} aria-current={i === now ? "step" : undefined}>
          {s}
        </li>
      ))}
    </ol>
  );
}

/** The page's frame: the steps, the title and what belongs to the step. */
export function Frame({ step, title, sub, children }: { step: number; title: string; sub?: string; children: React.ReactNode }) {
  return (
    <section className="cp wrap">
      <div className="cp-col">
        <Steps now={step} />
        <h1>{title}</h1>
        {sub && <p className="cp-sub">{sub}</p>}
        {children}
      </div>
    </section>
  );
}

const OFFLINE = "We couldn't reach the server. Check your connection and try again.";

type Answer<T> = T & { ok: boolean; message?: string; status: number };

/** POSTs JSON. Never throws: a failed request, even one that never got an answer (status 0), comes back as ok: false with a message. */
async function post<T>(url: string, body?: unknown): Promise<Answer<T>> {
  let res: Response;
  try {
    res = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch {
    return { ok: false, message: OFFLINE, status: 0 } as Answer<T>;
  }
  const json = (await res.json().catch(() => ({ ok: false, message: "That didn't work. Try again in a minute." }))) as T & { ok: boolean; message?: string };
  if (!res.ok && json.ok !== false) return { ...json, ok: false, status: res.status };
  return { ...json, status: res.status };
}

/** Whether setup is running after all: an answer may have been lost on the way back (the server starts it once). */
async function setupStarted(boxId: string): Promise<boolean> {
  try {
    const res = await fetch(`/api/cloud/boxes/${boxId}`, { cache: "no-store" });
    const j = (await res.json()) as { box?: { status: string } };
    return j.box?.status === "provisioning" || j.box?.status === "cert_pending" || j.box?.status === "active";
  } catch {
    return false;
  }
}

// ---- Stripe's payment form, inside the page ----

/** Stripe.js on the release train of the server's API version (lib/cloud/stripe.ts). Always loaded from js.stripe.com, never bundled. */
const STRIPE_JS = "https://js.stripe.com/endive/stripe.js";

type EmbeddedCheckout = { mount(el: HTMLElement): void; destroy(): void };
// createEmbeddedCheckoutPage is this release's name for it (initEmbeddedCheckout was removed in it).
type StripeJs = (key: string) => { createEmbeddedCheckoutPage(o: { fetchClientSecret: () => Promise<string>; onComplete?: () => void }): Promise<EmbeddedCheckout> };

let stripeJs: Promise<StripeJs> | null = null;
function loadStripeJs(): Promise<StripeJs> {
  stripeJs ??= new Promise<StripeJs>((resolve, reject) => {
    const have = () => (window as unknown as { Stripe?: StripeJs }).Stripe;
    if (have()) return resolve(have()!);
    const el = document.createElement("script");
    el.src = STRIPE_JS;
    el.async = true;
    el.onload = () => (have() ? resolve(have()!) : reject(new Error("Stripe didn't load.")));
    el.onerror = () => reject(new Error("We couldn't load the payment form. Check your connection, or an ad blocker, and try again."));
    document.head.appendChild(el);
  });
  // A failed load can be tried again.
  stripeJs.catch(() => (stripeJs = null));
  return stripeJs;
}

// Stripe allows one payment form on a page at a time: each mount waits for the one before it to be taken down.
let lastForm: Promise<unknown> = Promise.resolve();

/**
 * The payment form. The session never redirects (redirect_on_completion:
 * never), so this page, and the Hetzner key in its memory, are still here
 * when onComplete runs.
 */
function PayForm({ publishableKey, onComplete }: { publishableKey: string; onComplete: () => void }) {
  const slot = useRef<HTMLDivElement>(null);
  const done = useRef(onComplete);
  done.current = onComplete;
  const [error, setError] = useState("");
  const [tries, setTries] = useState(0);
  useEffect(() => {
    let gone = false;
    setError("");
    const mine = lastForm.then(async (): Promise<EmbeddedCheckout | null> => {
      if (gone) return null;
      try {
        // The session first, by ourselves: a failure here is ours to explain (Stripe's form would only say "Something went wrong").
        const [load, r] = await Promise.all([loadStripeJs(), post<{ clientSecret?: string }>("/api/cloud/checkout")]);
        if (!r.ok || !r.clientSecret) throw new Error(r.message ?? "Payment isn't available right now.");
        const secret = r.clientSecret;
        const form = await load(publishableKey).createEmbeddedCheckoutPage({ fetchClientSecret: async () => secret, onComplete: () => done.current() });
        if (gone || !slot.current) {
          form.destroy();
          return null;
        }
        form.mount(slot.current);
        return form;
      } catch (e) {
        if (!gone) setError(e instanceof Error && e.message ? e.message : "Payment isn't available right now.");
        return null;
      }
    });
    lastForm = mine;
    return () => {
      gone = true;
      lastForm = mine.then((form) => form?.destroy()).catch(() => {});
    };
  }, [publishableKey, tries]);
  return (
    <>
      <div ref={slot} className="cp-pay" />
      {error && (
        <div className="cp-row">
          <p className="cp-err" role="alert">
            {error}
          </p>
          <button type="button" className="cp-link" onClick={() => setTries((n) => n + 1)}>
            Try again
          </button>
        </div>
      )}
    </>
  );
}

/** How to make a key that sees only one project, with direct links. */
export function HetznerGuide() {
  return (
    <ol className="cp-guide">
      <li>
        Open the{" "}
        <a href="https://console.hetzner.cloud/projects" target="_blank" rel="noreferrer">
          Hetzner Cloud Console
        </a>{" "}
        (no account yet?{" "}
        <a href="https://accounts.hetzner.com/signUp" target="_blank" rel="noreferrer">
          sign up
        </a>
        ).
      </li>
      <li>
        Click <strong>+ New project</strong> and call it <strong>shiptiffin</strong>. A project of its own means the key sees only
        this box, nothing else you run at Hetzner.
      </li>
      <li>
        In that project: <strong>Security</strong> → <strong>API tokens</strong> → <strong>Generate API token</strong>. Name it
        ShipTiffin and choose <strong>Read &amp; Write</strong>.
      </li>
      <li>Copy the token (Hetzner shows it once) and paste it below.</li>
    </ol>
  );
}

/** Dollars always: exact for a USD account, approximate (≈) when converted. */
function dollars(n: number, currency: string, usdRate: number | null) {
  if (usdRate == null) return money(n, currency);
  const usd = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 2 }).format(n * usdRate);
  return currency.toUpperCase() === "USD" ? usd : `≈ ${usd}`;
}

function money(n: number, currency: string) {
  try {
    return new Intl.NumberFormat("en", { style: "currency", currency, maximumFractionDigits: 2 }).format(n);
  } catch {
    return `${n.toFixed(2)} ${currency}`;
  }
}

/** Paste and check a key. Calls onChecked with the key and what it can order. */
export function KeyField({ boxId, onChecked, label = "Hetzner API token" }: { boxId: string; onChecked: (token: string, r: Extract<CheckResult, { ok: true }>) => void; label?: string }) {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function check(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const r = await post<CheckResult>(`/api/cloud/boxes/${boxId}/check`, { token: token.trim() });
    setBusy(false);
    if (r.ok) onChecked(token.trim(), r as Extract<CheckResult, { ok: true }>);
    else setError(r.message ?? "That key didn't work.");
  }
  return (
    <form className="cp-field" onSubmit={check}>
      <label htmlFor="hz-token">{label}</label>
      <input id="hz-token" type="password" className="cp-mono" autoComplete="off" spellCheck={false} value={token} onChange={(e) => setToken(e.target.value)} placeholder="Paste the token" />
      <div className="cp-row">
        <button className="btn btn-primary" disabled={busy || token.trim().length < 20}>
          {busy ? "Checking with Hetzner…" : "Check the key"}
        </button>
      </div>
      {error && (
        <p className="cp-err" role="alert">
          {error}
        </p>
      )}
    </form>
  );
}

type Box = { id: string; name: string | null; status: string; fingerprint: string | null };

/** What paying needs: Stripe's publishable key (public by design) and whether the founding price applies. */
export type Pay = { publishableKey: string; founding: boolean };

/** What the customer chose before paying. It stays, with the key, in this page's memory until setup starts. */
type Choice = { name: string; serverType: string; location: string; sshKeys: { key: string; source: string }[]; option: Option | null };

/** How long the page waits for Stripe to confirm a payment its form just completed (it asks every 2 seconds). */
const CONFIRM_TRIES = 45;

export function StartFlow({ box: initialBox, job: initialJob, pay }: { box: Box; job: Job; pay: Pay }) {
  const [name, setName] = useState(initialBox.name);
  const box = { ...initialBox, name };
  const [status, setStatus] = useState(box.status);
  const [job, setJob] = useState<Job>(initialJob);
  const [checked, setChecked] = useState<{ token: string; r: Extract<CheckResult, { ok: true }> } | null>(null);
  // Not paid yet: what was chosen (the payment form shows while it is set).
  const [choice, setChoice] = useState<Choice | null>(null);
  // The payment form completed: setup is being started with the key still in memory.
  const [starting, setStarting] = useState(false);
  const [notice, setNotice] = useState("");
  const unpaid = status === "awaiting_payment";

  // Live progress while the worker runs, and while the certificate is pending
  // (often, while the worker itself waits for it; slowly once it has handed
  // over to the minutely check).
  const handedOver = Boolean(job?.steps.some((s) => s.text.startsWith("Installed. The certificate is still pending")));
  useEffect(() => {
    if (status !== "provisioning" && status !== "cert_pending") return;
    let stop = false;
    let t: ReturnType<typeof setTimeout>;
    const tick = async () => {
      try {
        // watch=1: this is the setup screen watching (the route counts how setup ended).
        const res = await fetch(`/api/cloud/boxes/${box.id}?watch=1`, { cache: "no-store" });
        const j = (await res.json()) as { box?: { status: string; name: string | null }; job?: Job };
        if (!stop && j.box) {
          setStatus(j.box.status);
          if (j.box.name) setName(j.box.name);
          setJob(j.job ?? null);
        }
      } catch {}
      if (!stop) t = setTimeout(tick, status === "cert_pending" && handedOver ? 15_000 : 2000);
    };
    t = setTimeout(tick, 1500);
    return () => {
      stop = true;
      clearTimeout(t);
    };
  }, [status, box.id, handedOver]);

  // Leaving now would lose the key (and, while paying, the place in the form): the browser asks first.
  const holding = Boolean(checked) && (unpaid || starting);
  useEffect(() => {
    if (!holding) return;
    const ask = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", ask);
    return () => window.removeEventListener("beforeunload", ask);
  }, [holding]);

  /**
   * The payment form completed. Start setup with what was chosen and the key
   * this page still holds. The server confirms the payment with Stripe
   * itself; 402 means Stripe hasn't confirmed it yet, so ask again shortly.
   */
  const startAfterPayment = useCallback(async () => {
    if (!checked || !choice) return;
    setStarting(true);
    setNotice("");
    const body = { token: checked.token, name: choice.name, serverType: choice.serverType, location: choice.location, sshKeys: choice.sshKeys };
    let why = "Stripe hasn't confirmed the payment yet. Give it a minute, then create the box below.";
    for (let i = 0; i < CONFIRM_TRIES; i++) {
      const res = await post(`/api/cloud/boxes/${box.id}/create`, body);
      if (res.ok || (await setupStarted(box.id))) {
        setName(choice.name);
        setStatus("provisioning");
        setStarting(false);
        return;
      }
      if (res.status !== 402 && res.status !== 0) {
        why = res.message ?? "Setup couldn't start.";
        break;
      }
      await new Promise((r) => setTimeout(r, 2000));
    }
    // Paid, but setup didn't start (the name was taken meanwhile, say): the box waits as paid, and is created from the form below.
    setNotice(`Your payment went through, but setup hasn't started. ${why}`);
    setChoice(null);
    setStatus("paid");
    setStarting(false);
  }, [checked, choice, box.id]);

  if (status === "provisioning" || status === "cert_pending" || status === "active") {
    return (
      <Frame step={status === "active" ? 4 : 3} title={status === "active" ? "Your box is ready" : "Creating your box"}>
        <Progress box={box} status={status} job={job} />
      </Frame>
    );
  }
  if (starting) {
    return (
      <Frame step={3} title="Creating your box">
        <div className="cp-card" role="status">
          <p className="cp-sub">Payment received. Starting setup…</p>
        </div>
      </Frame>
    );
  }
  const paying = unpaid && checked && choice;
  const step = paying ? 2 : unpaid ? 1 : 3;
  const title = paying ? "Start your subscription" : checked ? "Choose your box" : "Connect Hetzner";
  const sub = paying
    ? "One subscription per box. Setup starts the moment the payment goes through."
    : checked
      ? undefined
      : unpaid
        ? "First we check what your Hetzner account can order, and what it costs. You pay after that; nothing is charged before."
        : status === "paid"
          ? "Your payment went through. Paste your Hetzner token to set up the box: we never stored it."
          : undefined;
  return (
    <Frame step={step} title={title} sub={sub}>
      {status === "failed" && <Stopped box={box} job={job} />}
      {notice && (
        <p className="cp-err" role="alert">
          {notice}
        </p>
      )}
      {!checked ? (
        <div className="cp-card">
          <h2>Your Hetzner API token</h2>
          <HetznerGuide />
          <KeyField boxId={box.id} onChecked={(token, r) => setChecked({ token, r })} />
          <p className="cp-hint">
            We use the key to set up your server, then forget it; a resize later asks for a key again. Every call we make with it
            is listed in your account. Delete the token in Hetzner whenever you like; your box keeps running.
          </p>
        </div>
      ) : (
        <>
          {/* Kept mounted (hidden) while paying, so going back finds the name, size and SSH keys as they were. */}
          <div hidden={Boolean(paying)}>
            <Choose
              box={box}
              token={checked.token}
              r={checked.r}
              unpaid={unpaid}
              onContinue={setChoice}
              onStarted={(n) => {
                setName(n);
                setStatus("provisioning");
              }}
              onBack={() => {
                setChoice(null);
                setChecked(null);
              }}
            />
          </div>
          {paying && (
            <div className="cp-card">
              <div className="cp-row spread">
                <h2>{pay.founding ? "$12 a month, locked for 24 months" : "$19 a month per box"}</h2>
                <button type="button" className="cp-link" onClick={() => setChoice(null)}>
                  Change name or size
                </button>
              </div>
              {pay.founding && <p className="cp-muted cp-price-note">Founding price for our first 100 customers. $19 a month after that.</p>}
              <ul className="cp-perks">
                <li>Tiffin installed on your own server, then kept up to date</li>
                <li>Monitoring from outside, with an email if your box stops answering</li>
                <li>Backups copied off your server every 6 hours, encrypted with a key only you hold; kept 30 days</li>
                <li>
                  Your address, <strong>{choice.name}.{ZONE}</strong>, with HTTPS
                </li>
                <li>Resizing from your account, and support by email</li>
              </ul>
              <ul className="cp-facts">
                {choice.option && (
                  <li>
                    Hetzner bills the server itself, directly: {dollars(choice.option.monthlyNet, checked.r.currency, checked.r.usdRate)} a month before VAT for{" "}
                    {choice.option.serverType} in {choice.option.city}, with its address and a 40 GB data volume.
                  </li>
                )}
                <li>Cancel any time. Your server and apps keep running.</li>
                <li>14-day money-back guarantee.</li>
              </ul>
              <PayForm publishableKey={pay.publishableKey} onComplete={startAfterPayment} />
              <p className="cp-hint">Payment by Stripe, inside this page. Keep the page open: your box is created as soon as the payment goes through.</p>
            </div>
          )}
        </>
      )}
    </Frame>
  );
}

const NO_KEYS: HetznerKey[] = [];

function Choose({
  box,
  token,
  r,
  unpaid,
  onContinue,
  onStarted,
  onBack,
}: {
  box: Box;
  token: string;
  r: Extract<CheckResult, { ok: true }>;
  /** Not paid yet: the form leads to the payment form (onContinue) instead of starting setup. */
  unpaid: boolean;
  onContinue: (c: Choice) => void;
  onStarted: (name: string) => void;
  onBack: () => void;
}) {
  const [name, setName] = useState(box.name ?? "");
  // Whether the typed name is free, asked of the server a moment after typing stops.
  const [free, setFree] = useState<{ name: string; available: boolean; message: string } | null>(null);
  const [pick, setPick] = useState(r.suggested ? `${r.suggested.serverType}@${r.suggested.location}` : "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [access, setAccess] = useState<Access>({ keys: [], skipped: false, blocked: null });
  const onAccess = useCallback((a: Access) => setAccess(a), []);
  const why = name ? nameProblem(name) : null;
  // Answers already given, so going back to a name doesn't ask again.
  const asked = useRef(new Map<string, { name: string; available: boolean; message: string }>());
  useEffect(() => {
    if (box.name || !name || why) return;
    const known = asked.current.get(name);
    if (known) {
      setFree(known);
      return;
    }
    let stop = false;
    const t = setTimeout(async () => {
      try {
        const res = await fetch(`/api/cloud/names?name=${encodeURIComponent(name)}`, { cache: "no-store" });
        const j = (await res.json()) as { available?: boolean; message?: string };
        if (typeof j.available !== "boolean") return;
        const answer = { name, available: j.available, message: j.message ?? "" };
        asked.current.set(name, answer);
        if (!stop) setFree(answer);
      } catch {}
    }, 500);
    return () => {
      stop = true;
      clearTimeout(t);
    };
  }, [name, why, box.name]);
  const taken = free?.name === name && !free.available;
  const eu = useMemo(() => r.options.filter((o) => o.region === "eu"), [r.options]);
  const us = useMemo(() => r.options.filter((o) => o.region === "us"), [r.options]);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    const [serverType, location] = pick.split("@") as [string, string];
    // Exactly the keys the owner reviewed above, as they were shown.
    const sshKeys = access.keys.map(({ key, source }) => ({ key, source }));
    if (unpaid) {
      onContinue({ name, serverType, location, sshKeys, option: r.options.find((o) => o.serverType === serverType && o.location === location) ?? null });
      return;
    }
    setBusy(true);
    setError("");
    const res = await post(`/api/cloud/boxes/${box.id}/create`, { token, name, serverType, location, sshKeys });
    if (res.ok || (await setupStarted(box.id))) onStarted(name);
    else {
      setError(res.message ?? "Setup couldn't start.");
      setBusy(false);
    }
  }

  const group = (title: string, opts: Option[]) =>
    opts.length > 0 && (
      <fieldset className="cp-field" style={{ border: 0, padding: 0, margin: 0 }}>
        <legend className="cp-label">{title}</legend>
        <div className="cp-options">
          {opts.map((o) => {
            const id = `${o.serverType}@${o.location}`;
            return (
              <label key={id} className="cp-option">
                <input type="radio" name="size" value={id} checked={pick === id} disabled={!o.available} onChange={() => setPick(id)} />
                <b>
                  {o.serverType} · {o.city}
                </b>
                <span className="cp-price">{dollars(o.monthlyNet, r.currency, r.usdRate)}/mo</span>
                <small>
                  {o.cores} vCPU{o.arch === "arm64" ? " (ARM)" : ""}, {o.memoryGB} GB RAM, {o.diskGB} GB disk + 40 GB data volume
                  {o.available ? "" : " · sold out here right now"}
                </small>
              </label>
            );
          })}
        </div>
      </fieldset>
    );

  const accessReady = !access.blocked && (access.skipped || access.keys.length > 0);
  return (
    <form onSubmit={create}>
      <div className="cp-card">
        <div className="cp-row spread">
          <h2>Name, size and place</h2>
          <button type="button" className="cp-link" onClick={onBack}>
            Use another key
          </button>
        </div>
        <p className="cp-ok">Key checked: read &amp; write, prices from your account.</p>
        {r.servers > 0 && (
          <p className="cp-hint">
            This project already has {r.servers} server{r.servers > 1 ? "s" : ""}. We only ever touch what we create for this box (labelled
            shiptiffin-box with its id), but a new, empty project keeps the key away from everything else.
          </p>
        )}
        <div className="cp-field">
          <label htmlFor="box-name">Name</label>
          <div className="cp-name">
            <input
              id="box-name"
              type="text"
              value={name}
              readOnly={Boolean(box.name)}
              onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ""))}
              placeholder="your-name"
              autoComplete="off"
              spellCheck={false}
              maxLength={30}
            />
            <span>.{ZONE}</span>
          </div>
          {why ? (
            <p className="cp-err">{why}</p>
          ) : taken ? (
            <p className="cp-err">{free.message}</p>
          ) : (
            <p className="cp-hint">
              {free?.name === name && free.available ? `${free.message} ` : ""}Your dashboard will be dashboard.{name || "your-name"}.{ZONE}; apps go under it.
              Add your own domain any time.
            </p>
          )}
        </div>
        {group("Europe", eu)}
        {group("United States", us)}
        {r.options.length === 0 ? (
          <p className="cp-err">Hetzner offers none of our sizes to this account right now. Try again later{unpaid ? "; nothing has been charged" : ""}.</p>
        ) : (
          !r.options.some((o) => o.available) && (
            <p className="cp-err">Every size we offer is sold out at Hetzner right now. Try again later{unpaid ? "; nothing has been charged" : ""}.</p>
          )
        )}
        <p className="cp-hint">
          Prices are Hetzner&rsquo;s, from your account, before VAT: the server, its IPv4 address and a 40 GB data volume. Hetzner bills
          you directly
          {r.currency.toUpperCase() !== "USD" && r.usdRate != null
            ? `, in ${r.currency.toUpperCase()}; the dollar amounts are approximate, at today's exchange rate`
            : ""}
          .
        </p>
      </div>
      <ServerAccess hetzner={r.sshKeys ?? NO_KEYS} onChange={onAccess} />
      <div className="cp-create">
        <div className="cp-row">
          <button className="btn btn-primary" disabled={busy || !name || Boolean(why) || taken || !pick || !accessReady}>
            {busy ? "Starting…" : unpaid ? "Continue to payment" : "Create my box"}
          </button>
          <span className="cp-hint">
            {access.blocked ??
              (access.keys.length
                ? `With ${access.keys.length === 1 ? "your SSH key" : `${access.keys.length} SSH keys`}.`
                : access.skipped
                  ? "Without SSH."
                  : "Add a key above, or continue without SSH.")}
          </span>
        </div>
        {error && (
          <p className="cp-err" role="alert">
            {error}
          </p>
        )}
      </div>
    </form>
  );
}
