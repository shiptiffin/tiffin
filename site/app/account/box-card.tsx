// One box on /account, as a card: name, address and status; one main
// button; a few facts; what needs your attention; the everyday changes
// and the way out (Cancel subscription); and the Hetzner activity.
// A box being deleted is a live progress card instead (deleting.tsx); a
// deleted one, a quiet row under "Deleted" (DeletedRow). Pure (no
// database): the page passes the rows in.
import { renewable } from "@/lib/cloud/actions";
import { DNS_GRACE_DAYS, ENDED, extrasOn, UNMANAGED } from "@/lib/cloud/billing";
import type { BoxRow, CallRow, JobRow } from "@/lib/cloud/db";
import { family, RESIZE_TYPES } from "@/lib/cloud/hetzner";
import { boxDomain, dashboardUrl } from "@/lib/cloud/names";
import { BoxActions, PastInvoices, type BoxView } from "./account-actions";
import { Deleting } from "./deleting";
import { goneLines, releasedLine } from "./words";
import { dayWords, endedWords, priceWords } from "@/lib/cloud/money";

const when = (d: Date | null) => (d ? d.toLocaleString("en-GB", { dateStyle: "medium", timeStyle: "short", timeZone: "UTC" }) + " UTC" : "never");
const day = (d: Date | null) => (d ? d.toLocaleDateString("en-GB", { dateStyle: "medium", timeZone: "UTC" }) : "");

/** Hetzner's locations we offer, by city. */
const CITY: Record<string, string> = { fsn1: "Falkenstein", nbg1: "Nuremberg", hel1: "Helsinki", ash: "Ashburn", hil: "Hillsboro" };

type Tone = "good" | "warn" | "bad" | undefined;

function statusPill(b: BoxRow): [string, Tone] {
  if (b.attention && !UNMANAGED.has(b.status)) return ["Needs attention", "bad"];
  switch (b.status) {
    case "active":
      if (b.dns_state === "killed") return ["Address turned off", "bad"];
      if (b.dns_state === "parked") return ["Address parked", "warn"];
      if (b.down_alerted_at) return ["Not answering", "bad"];
      return ["Running", "good"];
    case "cert_pending":
      return ["Waiting for certificate", "warn"];
    case "provisioning":
      return ["Being set up", "warn"];
    case "deleting":
      return ["Being deleted", "warn"];
    case "failed":
      return ["Setup stopped", "bad"];
    case "paid":
      return ["Waiting for Hetzner", "warn"];
    case "awaiting_payment":
      return ["Not paid", undefined];
    case "deleted":
      return ["Deleted", undefined];
    case "released":
      return ["Not managed", undefined];
  }
}

/** "$12 a month · renews 8 Nov" */
function planWords(b: BoxRow, now: Date): string {
  if (b.plan_status === "none") return "Not paid yet";
  const price = priceWords(b.founding);
  const on = (d: Date | null) => (d ? ` ${dayWords(d, now)}` : "");
  if (b.refunded_at) return "Refunded; the subscription has ended";
  if (extrasOn(b.plan_status, Boolean(b.first_paid_at))) {
    if (b.cancel_at_period_end) return `${price} · ends${on(b.current_period_end)}`;
    if (b.plan_status === "past_due") return `${price} · payment overdue`;
    return b.current_period_end ? `${price} · renews${on(b.current_period_end)}` : price;
  }
  if (!ENDED.has(b.plan_status)) return `${price} · not active (${b.plan_status.replace("_", " ")})`;
  return `Ended${on(b.plan_ended_at ?? b.current_period_end)}`;
}

function addressWords(b: BoxRow & { name: string }): string {
  if (UNMANAGED.has(b.status) || b.dns_state === "removed") return "address removed";
  if (b.dns_state === "killed") return "address turned off";
  if (b.dns_state === "parked") return "address parked until the box checks in";
  return "address not live yet";
}

type Note = { tone: "bad" | "warn" | "info"; body: React.ReactNode };

function notes(b: BoxRow, job: JobRow | null): Note[] {
  const out: Note[] = [];
  if (UNMANAGED.has(b.status)) return out;
  if (b.attention) {
    out.push({
      tone: "bad",
      body: (
        <>
          {b.attention} We&rsquo;ve been told too; write to <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a> with questions.
        </>
      ),
    });
  }
  if (job && (job.status === "queued" || job.status === "running" || (job.status === "failed" && job.kind !== "provision"))) {
    const what = job.kind.replace("_", " ");
    out.push(
      job.status === "failed"
        ? { tone: "bad", body: `The ${what} stopped: ${job.error ?? "no reason given"}` }
        : { tone: "info", body: `${what[0]!.toUpperCase()}${what.slice(1)}: ${job.steps.at(-1)?.text ?? job.status}` },
    );
  }
  if (b.plan_status === "past_due" && extrasOn(b.plan_status, Boolean(b.first_paid_at))) {
    out.push({ tone: "warn", body: "The last payment didn’t go through. Update your card under Billing and invoices; Stripe tries again by itself." });
  }
  if (b.first_paid_at && !extrasOn(b.plan_status, true) && !b.refunded_at) {
    const until = b.extras_paused_at ? new Date(b.extras_paused_at.getTime() + DNS_GRACE_DAYS * 86_400_000) : null;
    out.push({
      tone: "warn",
      body: `${ENDED.has(b.plan_status) ? "The subscription has ended" : "The subscription isn’t active"}, so updates and the extras are paused. Your server and apps keep running${
        b.dns_state === "live" && until ? `; the address stays until ${day(until)}` : ""
      }. ${ENDED.has(b.plan_status) ? "Renew" : "Update your card under Billing and invoices"} to turn everything back on.`,
    });
  }
  if ((b.status === "active" || b.status === "cert_pending") && b.failing.length) {
    out.push({ tone: "warn", body: `Failing on the box: ${b.failing.join(", ")}.` });
  }
  return out;
}

/** Where a server is: "Hetzner cx23, Falkenstein (fsn1), 203.0.113.5". */
function serverWords(b: BoxRow): string {
  const city = CITY[b.location ?? ""];
  return `Hetzner ${b.server_type}, ${city ?? b.location}${city ? ` (${b.location})` : ""}${b.ipv4 ? `, ${b.ipv4}` : ""}`;
}

/** A deleted box: its name, a badge and what went. Nothing to open or change. */
function DeletedRow({ b }: { b: BoxRow }) {
  const [first, ...rest] = goneLines({ name: b.name, deletedAt: b.deleted_at, dataDeleted: Boolean(b.data_deleted) });
  const paid = b.first_paid_at ? endedWords(b, b.deleted_at) : null;
  return (
    <li className="cp-gone-row" id={b.id}>
      <div className="cp-box-title">
        <h3>{b.name ?? "Unnamed box"}</h3>
        <span className="cp-pill">Deleted</span>
      </div>
      <p>{first}</p>
      {paid && <p className="cp-muted">{paid}</p>}
      {rest.map((l) => (
        <p key={l} className="cp-muted">
          {l}
        </p>
      ))}
    </li>
  );
}

export function BoxCard({ b, calls, job, now = new Date() }: { b: BoxRow; calls: CallRow[]; job: JobRow | null; now?: Date }) {
  if (b.status === "deleting" || b.status === "deleted") {
    // While it's deleted (and right after, until the page is next loaded): the live card.
    const j = job?.kind === "delete_server" ? job : null;
    return (
      <Deleting
        box={{ id: b.id, name: b.name }}
        status={b.status}
        job={j && { status: j.status, steps: j.steps, error: j.error }}
        deletedAt={b.deleted_at?.toISOString() ?? null}
        dataDeleted={b.data_deleted}
      />
    );
  }
  const [label, tone] = statusPill(b);
  const active = extrasOn(b.plan_status, Boolean(b.first_paid_at));
  const held = Boolean(b.signin_code && b.signin_expires_at && b.signin_expires_at > now);
  const view: BoxView = {
    id: b.id,
    name: b.name,
    status: b.status,
    domain: b.name ? boxDomain(b.name) : null,
    active,
    renewable: renewable(b),
    cancelAtPeriodEnd: b.cancel_at_period_end,
    periodEnd: day(b.current_period_end),
    hasSubscription: Boolean(b.stripe_subscription_id),
    hasCustomer: Boolean(b.stripe_customer_id),
    signin: b.status === "active" && !b.handoff_closed_at ? (held ? "held" : b.signin_requested_at ? "asked" : "expired") : null,
    signinUntil: held ? when(b.signin_expires_at) : "",
    serverType: b.server_type,
    price: b.stripe_subscription_id && !ENDED.has(b.plan_status) && !b.refunded_at ? priceWords(b.founding) : null,
    sizes: RESIZE_TYPES.filter((t) => b.server_type && t !== b.server_type && family(t) === family(b.server_type)),
  };
  const released = b.status === "released";
  const facts: [string, React.ReactNode][] = released ? [] : [["Subscription", planWords(b, now)]];
  if (b.server_type) facts.push(["Server", serverWords(b)]);
  if (b.status === "active" || b.status === "cert_pending") {
    facts.push(["Last check-in", `${when(b.last_heartbeat_at)}${b.last_version ? `, Tiffin ${b.last_version}` : ""}`]);
  }
  const list = notes(b, job);
  return (
    <article className="cp-card cp-box" id={b.id} aria-labelledby={`${b.id}-name`}>
      <header className="cp-box-head">
        <div className="cp-box-title">
          <h2 id={`${b.id}-name`}>{b.name ?? "New box"}</h2>
          <span className="cp-pill" data-tone={tone}>
            {label}
          </span>
        </div>
        {b.name && (
          <p className="cp-box-addr">
            {b.dns_state === "live" && !released ? (
              <a href={dashboardUrl(b.name)}>{boxDomain(b.name)}</a>
            ) : (
              <>
                {boxDomain(b.name)} <span className="cp-muted">{addressWords(b as BoxRow & { name: string })}</span>
              </>
            )}
          </p>
        )}
      </header>

      {released && <p className="cp-box-line">{releasedLine(b.released_at)}</p>}

      <dl className="cp-box-facts">
        {facts.map(([k, v]) => (
          <div key={k}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>

      {list.length > 0 && (
        <ul className="cp-notes" role="status">
          {list.map((n, i) => (
            <li key={i} className="cp-note" data-tone={n.tone}>
              {n.body}
            </li>
          ))}
        </ul>
      )}

      <BoxActions box={view} />

      <details className="cp-fold">
        <summary>
          Hetzner activity <span className="cp-count">{calls.length === 200 ? "200+" : calls.length} calls</span>
        </summary>
        <p className="cp-hint">Every call we made with your Hetzner key, newest first.</p>
        <p className="cp-hint">
          We keep no key: each change asks for one, then forgets it.
          {b.token_fingerprint ? (
            <>
              {" "}
              Setup used the key with fingerprint <span className="cp-mono">{b.token_fingerprint}</span>.
            </>
          ) : null}
        </p>
        {calls.length > 0 && (
          <div className="cp-scroll cp-table-wrap">
            <table className="cp-log">
              <thead>
                <tr>
                  <th>When (UTC)</th>
                  <th>Why</th>
                  <th>Request</th>
                  <th>Answer</th>
                </tr>
              </thead>
              <tbody>
                {calls.map((c, i) => (
                  <tr key={i}>
                    <td>{c.at.toISOString().replace("T", " ").slice(0, 19)}</td>
                    <td>{c.purpose}</td>
                    <td>
                      {c.method} {c.path}
                    </td>
                    <td>{c.status ?? c.error ?? "no answer"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </details>
    </article>
  );
}

/** Deleted boxes, at the bottom: a small heading and one row each; folded away when there are several. */
export function DeletedList({ rows, invoices }: { rows: BoxRow[]; invoices: boolean }) {
  const list = (
    <ul className="cp-gone-list">
      {rows.map((b) => (
        <DeletedRow key={b.id} b={b} />
      ))}
    </ul>
  );
  const head = (
    <>
      Deleted <span className="cp-count">{rows.length}</span>
    </>
  );
  return (
    <section className="cp-gone" aria-label="Deleted boxes">
      {rows.length > 3 ? (
        <details>
          <summary className="cp-gone-head">{head}</summary>
          {list}
        </details>
      ) : (
        <>
          <h2 className="cp-gone-head">{head}</h2>
          {list}
        </>
      )}
      {invoices && (
        <p className="cp-gone-foot">
          <PastInvoices />
        </p>
      )}
    </section>
  );
}
