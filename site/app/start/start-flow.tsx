"use client";
// The steps of /start that happen in the browser: pay, connect Hetzner (the
// key stays in this page's memory until "Create"), choose, watch, open.
import { useEffect, useMemo, useState } from "react";
import { nameProblem, ZONE } from "@/lib/cloud/names";
import type { CheckResult, Option } from "@/lib/cloud/hetzner";

const STEPS = ["Account", "Pay", "Connect Hetzner", "Create", "Open"];

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

async function post<T>(url: string, body?: unknown): Promise<T & { ok: boolean; message?: string }> {
  const res = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
  const json = (await res.json().catch(() => ({ ok: false, message: "That didn't work. Try again in a minute." }))) as T & { ok: boolean; message?: string };
  if (!res.ok && json.ok !== false) return { ...json, ok: false };
  return json;
}

export function PayButton() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function pay() {
    setBusy(true);
    setError("");
    const r = await post<{ url?: string }>("/api/cloud/checkout");
    if (r.ok && r.url) window.location.href = r.url;
    else {
      setError(r.message ?? "Payment isn't available right now.");
      setBusy(false);
    }
  }
  return (
    <div className="cp-row">
      <button className="btn btn-primary" onClick={pay} disabled={busy}>
        {busy ? "Opening Stripe…" : "Continue to payment"}
      </button>
      {error && (
        <span className="cp-err" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}

export function WaitForPayment() {
  useEffect(() => {
    const t = setTimeout(() => window.location.reload(), 3000);
    return () => clearTimeout(t);
  }, []);
  return (
    <div className="cp-card" role="status">
      <p className="cp-sub">Stripe is confirming the payment. This page checks again in a few seconds.</p>
    </div>
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

type Job = { status: string; steps: { at: string; text: string }[]; error: string | null } | null;
type Box = { id: string; name: string | null; status: string; fingerprint: string | null };

export function StartFlow({ box, job: initialJob }: { box: Box; job: Job }) {
  const [status, setStatus] = useState(box.status);
  const [job, setJob] = useState<Job>(initialJob);
  const [checked, setChecked] = useState<{ token: string; r: Extract<CheckResult, { ok: true }> } | null>(null);

  // Live progress while the worker runs, and while the certificate is pending.
  useEffect(() => {
    if (status !== "provisioning" && status !== "cert_pending") return;
    let stop = false;
    const tick = async () => {
      try {
        const res = await fetch(`/api/cloud/boxes/${box.id}`, { cache: "no-store" });
        const j = (await res.json()) as { box?: { status: string }; job?: Job };
        if (!stop && j.box) {
          setStatus(j.box.status);
          setJob(j.job ?? null);
        }
      } catch {}
      if (!stop) setTimeout(tick, status === "cert_pending" ? 15_000 : 2000);
    };
    const t = setTimeout(tick, 1500);
    return () => {
      stop = true;
      clearTimeout(t);
    };
  }, [status, box.id]);

  if (status === "provisioning" || status === "cert_pending" || status === "active") {
    return <Progress box={box} status={status} job={job} />;
  }
  return (
    <>
      {status === "failed" && (
        <div className="cp-card" role="alert">
          <h2>Setup stopped</h2>
          <p className="cp-sub">{job?.error ?? "Something went wrong."}</p>
          <p className="cp-hint">
            We removed the box&rsquo;s address and deleted what this try made in your Hetzner project (only what carries this
            box&rsquo;s shiptiffin-box label; nothing else in your account is touched), and forgot your key. Paste it again to start
            over.
          </p>
        </div>
      )}
      {!checked ? (
        <div className="cp-card">
          <h2>Your Hetzner API token</h2>
          <HetznerGuide />
          <KeyField boxId={box.id} onChecked={(token, r) => setChecked({ token, r })} />
          <p className="cp-hint">
            We use the key to set up your server, then forget it (unless you ask us to keep it). Every call we make with it is listed
            in your account. Delete the token in Hetzner whenever you like; your box keeps running.
          </p>
        </div>
      ) : (
        <Choose box={box} token={checked.token} r={checked.r} onStarted={() => setStatus("provisioning")} onBack={() => setChecked(null)} />
      )}
    </>
  );
}

function Choose({ box, token, r, onStarted, onBack }: { box: Box; token: string; r: Extract<CheckResult, { ok: true }>; onStarted: () => void; onBack: () => void }) {
  const [name, setName] = useState(box.name ?? "");
  const [pick, setPick] = useState(r.suggested ? `${r.suggested.serverType}@${r.suggested.location}` : "");
  const [keep, setKeep] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const why = name ? nameProblem(name) : null;
  const eu = useMemo(() => r.options.filter((o) => o.region === "eu"), [r.options]);
  const us = useMemo(() => r.options.filter((o) => o.region === "us"), [r.options]);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    const [serverType, location] = pick.split("@");
    setBusy(true);
    setError("");
    const res = await post(`/api/cloud/boxes/${box.id}/create`, { token, name, serverType, location, keepKey: keep });
    if (res.ok) onStarted();
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

  return (
    <form className="cp-card" onSubmit={create}>
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
            placeholder="acme"
            autoComplete="off"
            spellCheck={false}
            maxLength={30}
          />
          <span>.{ZONE}</span>
        </div>
        {why ? <p className="cp-err">{why}</p> : <p className="cp-hint">Your dashboard will be dashboard.{name || "name"}.{ZONE}; apps go under it. Add your own domain any time.</p>}
      </div>
      {group("Europe", eu)}
      {group("United States", us)}
      {r.options.length === 0 && <p className="cp-err">Hetzner offers none of our sizes to this account right now. Try again later.</p>}
      <p className="cp-hint">
        Prices are Hetzner&rsquo;s, from your account, before VAT: the server, its IPv4 address and a 40 GB data volume. Hetzner bills
        you directly
        {r.currency.toUpperCase() !== "USD" && r.usdRate != null
          ? `, in ${r.currency.toUpperCase()}; the dollar amounts are approximate, at today's exchange rate`
          : ""}
        .
      </p>
      <label className="cp-check">
        <input type="checkbox" checked={keep} onChange={(e) => setKeep(e.target.checked)} />
        <span>
          Keep my key so I can resize in one click. <span className="cp-hint">Stored sealed (only our setup worker can open it); remove it any time. Unticked, we forget it once the box is up.</span>
        </span>
      </label>
      <div className="cp-row">
        <button className="btn btn-primary" disabled={busy || !name || Boolean(why) || !pick}>
          {busy ? "Starting…" : "Create my box"}
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

function Progress({ box, status, job }: { box: Box; status: string; job: Job }) {
  const steps = job?.steps ?? [];
  const live = status === "provisioning";
  const pending = status === "cert_pending";
  return (
    <div className="cp-card" aria-live="polite">
      <h2>{live ? `Creating ${box.name}.${ZONE}` : pending ? `${box.name}.${ZONE}: certificate pending` : `${box.name}.${ZONE} is ready`}</h2>
      {live && <p className="cp-hint">This takes about five minutes. You can close this page; we email you when it&rsquo;s done.</p>}
      {pending && (
        <p className="cp-hint" role="status">
          Tiffin is installed, but the dashboard doesn&rsquo;t answer over HTTPS with a valid certificate yet (Let&rsquo;s Encrypt can take a while,
          or be rate-limited). We check every minute and email you when it&rsquo;s ready. You can close this page.
        </p>
      )}
      <ul className="cp-progress">
        {steps.length === 0 && <li data-live>Waiting for a worker</li>}
        {steps.map((s, i) => (
          <li key={i} data-live={live && i === steps.length - 1 ? "" : undefined}>
            {s.text}
          </li>
        ))}
      </ul>
      {!live && !pending && (
        <>
          <div className="cp-row">
            <a className="btn btn-primary" href={`/api/cloud/boxes/${box.id}/open`}>
              Open your dashboard
            </a>
            <a className="btn btn-quiet" href="/account">
              Your account
            </a>
          </div>
          <p className="cp-hint">
            This first time signs you in with a one-time link your box made (it works once, within 24 hours). Add a passkey in the dashboard
            then: after that, you sign in on the box itself.
          </p>
        </>
      )}
    </div>
  );
}
