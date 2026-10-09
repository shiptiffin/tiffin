"use client";

// The two monthly bills for the same setup, side by side: the separate
// services (lean or typical, the reader picks) against one ShipTiffin box.
// Every price was read on the vendor's own pricing page on 2026-10-08.
// Re-check each one, and the date in compare.tsx, before changing a number.
// Our side: $19 a box ($12 founding), plus about $10 for the smallest
// Hetzner server (cx23 $6.49 + IPv4 ~$0.60 + 40 GB volume ~$2.80, net, Hetzner
// API 2026-10-08), billed by Hetzner. Keep in step with pricing.tsx.

import { useId, useState, type CSSProperties } from "react";

type Mode = "lean" | "typical";

type Line = {
  need: string;
  vendor: Record<Mode, string>;
  usd: Record<Mode, number>;
  /** How the line is counted, shown under "How each line is counted". */
  note: string;
  /** The part of the box that covers the same need. */
  part: string;
};

const LINES: Line[] = [
  {
    need: "Hosting for 4 apps",
    vendor: { lean: "Vercel Pro", typical: "Vercel Pro" },
    usd: { lean: 20, typical: 20 },
    note: "Vercel Pro, one seat. Hobby is for non-commercial use only. Usage past the $20 credit is billed on top.",
    part: "Apps and previews",
  },
  {
    need: "Databases and sign-in",
    vendor: { lean: "Supabase Pro, one shared project", typical: "Supabase Pro, a project per app" },
    usd: { lean: 25, typical: 55 },
    note: "Supabase Pro is $25 with one project, about $10 for each more. Lean: all four apps share one. Free allows two, paused after a quiet week.",
    part: "Postgres and sign-in, per app",
  },
  {
    need: "Cache",
    vendor: { lean: "Upstash Redis, free", typical: "Upstash Redis, 250 MB" },
    usd: { lean: 0, typical: 10 },
    note: "Upstash Redis is free up to 500K commands a month, or $10 for a fixed 250 MB.",
    part: "KV cache",
  },
  {
    need: "Error tracking",
    vendor: { lean: "Sentry, free", typical: "Sentry Team" },
    usd: { lean: 0, typical: 26 },
    note: "Sentry is free for one person and 5K errors, or Team at $26 billed yearly.",
    part: "Error tracking",
  },
  {
    need: "Analytics",
    vendor: { lean: "PostHog, free", typical: "Plausible" },
    usd: { lean: 0, typical: 19 },
    note: "PostHog is free up to 1M events, or Plausible is $19 for up to 10 sites.",
    part: "Analytics",
  },
];

const PLAN = 19;
const FOUNDING = 12;
const SERVER = 10;
const OURS = PLAN + SERVER;

const total = (mode: Mode) => LINES.reduce((n, l) => n + l.usd[mode], 0);
/** Typical leaves out usage billed on top, so it is a floor. */
const OPEN_ENDED: Record<Mode, boolean> = { lean: false, typical: true };

const usd = (n: number) => `$${n.toLocaleString("en-US")}`;
const roundTo10 = (n: number) => Math.round(n / 10) * 10;

const MODES: { id: Mode; label: string; hint: string }[] = [
  { id: "lean", label: "Lean", hint: "free tiers where they fit" },
  { id: "typical", label: "Typical", hint: "paid plans" },
];

export function CompareLedger() {
  const [mode, setMode] = useState<Mode>("typical");
  const name = useId();
  const theirs = total(mode);
  const diff = theirs - OURS;
  const plus = OPEN_ENDED[mode];

  return (
    <div className="ledger" data-mode={mode}>
      <fieldset className="ledger-mode">
        <legend className="ledger-mode-legend">Count the separate services at</legend>
        <div className="seg" style={{ "--n": 2 } as CSSProperties}>
          {MODES.map((m) => (
            <label key={m.id} className="seg-opt">
              <input
                type="radio"
                name={name}
                value={m.id}
                checked={mode === m.id}
                onChange={() => setMode(m.id)}
              />
              <span>
                <span className="ledger-mode-label">{m.label}</span>
                <span className="sr-only">, </span>
                <span className="ledger-mode-hint">{m.hint}</span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>

      <table className="ledger-table">
        <caption className="sr-only">
          The same setup a month: bought separately ({mode}) and with ShipTiffin
        </caption>
        <colgroup>
          <col className="ledger-col-need" />
          <col />
          <col className="ledger-col-ours" />
        </colgroup>
        <thead>
          <tr>
            <th scope="col">
              <span className="sr-only">You need</span>
            </th>
            <th scope="col" className="ledger-head">
              Bought separately
            </th>
            <th scope="col" className="ledger-head ledger-ours">
              With ShipTiffin
            </th>
          </tr>
        </thead>
        <tbody>
          {LINES.map((l) => (
            <tr key={l.need}>
              <th scope="row">{l.need}</th>
              <td>
                <span className="ledger-cell">
                  <span className="ledger-what">{l.vendor[mode]}</span>
                  <span className="ledger-usd">{usd(l.usd[mode])}</span>
                </span>
              </td>
              <td className="ledger-ours">
                <span className="ledger-cell">
                  <span className="ledger-what">{l.part}</span>
                  <span className="ledger-usd ledger-incl">Included</span>
                </span>
              </td>
            </tr>
          ))}
          <tr className="ledger-ours-row">
            <th scope="row">The box</th>
            <td>
              <span className="ledger-none" aria-hidden="true">
                &ndash;
              </span>
              <span className="sr-only">Not needed</span>
            </td>
            <td className="ledger-ours">
              <span className="ledger-cell">
                <span className="ledger-what">ShipTiffin</span>
                <span className="ledger-usd">{usd(PLAN)}</span>
              </span>
            </td>
          </tr>
          <tr className="ledger-ours-row">
            <th scope="row">The server</th>
            <td>
              <span className="ledger-none" aria-hidden="true">
                &ndash;
              </span>
              <span className="sr-only">Not needed</span>
            </td>
            <td className="ledger-ours">
              <span className="ledger-cell">
                <span className="ledger-what">Smallest server, billed by Hetzner</span>
                <span className="ledger-usd">
                  <span className="ledger-approx">about </span>
                  {usd(SERVER)}
                </span>
              </span>
            </td>
          </tr>
        </tbody>
        <tfoot>
          <tr className="ledger-total">
            <th scope="row">A month</th>
            <td>
              <span className="ledger-big" key={`t-${mode}`}>
                {usd(theirs)}
                {plus ? <span className="ledger-plus">+</span> : null}
              </span>
              {plus ? <span className="ledger-sub">plus usage</span> : <span className="ledger-sub">&nbsp;</span>}
            </td>
            <td className="ledger-ours">
              <span className="ledger-big">
                <span className="ledger-about">about </span>
                {usd(OURS)}
              </span>
              <span className="ledger-sub">
                {usd(FOUNDING + SERVER)} for the first 100 customers
              </span>
            </td>
          </tr>
        </tfoot>
      </table>

      <p className="ledger-diff" aria-live="polite">
        <span className="ledger-diff-amount" key={`d-${mode}`}>
          <span className="ledger-about">about </span>
          {usd(diff)}
        </span>
        <span className="ledger-diff-text">
          less a month with ShipTiffin{plus ? ", before usage charges" : ""}. About {usd(roundTo10(diff * 12))} a
          year, and the bill stays the same as your traffic grows.
        </span>
      </p>

      <details className="ledger-how">
        <summary>How each line is counted</summary>
        <ul>
          {LINES.map((l) => (
            <li key={l.need}>{l.note}</li>
          ))}
          <li>
            ShipTiffin is ${PLAN} a month per box, or ${FOUNDING} for the first 100 customers, locked for 24 months.
            The server is the smallest Hetzner server (2 vCPU, 4 GB) with its IPv4 address and a 40 GB data volume, about ${SERVER} a month before VAT, billed by Hetzner to you.
          </li>
        </ul>
      </details>
    </div>
  );
}
