// What ShipTiffin tells a customer about their box: ready, setup trouble,
// billing, the address, the monitor. One component per email, with its
// subject; lib/cloud/emails.ts picks one for each outbox row and renders it.
// Short, plain words: what happened, what it means for the box, what to do.
import type { ReactElement, ReactNode } from "react";
import { boxDomain, dashboardUrl, ZONE } from "../lib/cloud/names";
import { NO_REFUND } from "../lib/cloud/money";
import { READY } from "./theme";
import { A, Code, Cta, Eyebrow, Facts, H1, Layout, LinkOut, P, Panel, Picture, Rule, Steps } from "./ui";

export type BoxCtx = {
  name: string | null;
  /** The box's id: the "Open your dashboard" button goes through the account (/api/cloud/boxes/:id/open). */
  id?: string;
  /** https://shiptiffin.com, or SITE_URL. */
  site: string;
};

export type Built = { subject: string; element: ReactElement };

const account = (b: BoxCtx) => `${b.site}/account`;
const domain = (b: BoxCtx) => (b.name ? boxDomain(b.name) : null);

/** "shop (shop.shiptiffin.app)", or "your box". */
function Named({ b }: { b: BoxCtx }) {
  return b.name ? (
    <>
      {b.name} (<Code>{boxDomain(b.name)}</Code>)
    </>
  ) : (
    <>your box</>
  );
}

const ownerWhy = (b: BoxCtx): ReactNode =>
  b.name ? (
    <>
      Sent to you as the owner of <Code>{boxDomain(b.name)}</Code>.
    </>
  ) : (
    "Sent to you about your ShipTiffin box."
  );

/** "8 October 2026" */
export const day = (d: Date) => d.toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric", timeZone: "UTC" });
/** "8 October 2026, 14:32 UTC" */
export const moment = (d: Date) =>
  `${day(d)}, ${d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", hour12: false, timeZone: "UTC" })} UTC`;

function Notice({ b, title, preview, children, why }: { b: BoxCtx; title: string; preview: string; children: ReactNode; why?: ReactNode }) {
  return (
    <Layout title={title} preview={preview} why={why ?? ownerWhy(b)}>
      {children}
    </Layout>
  );
}

// ---- the one worth celebrating -------------------------------------------

export function Ready({ b }: { b: BoxCtx }) {
  const name = b.name ?? "your-box";
  const dash = dashboardUrl(name);
  const open = b.id ? `${b.site}/api/cloud/boxes/${encodeURIComponent(b.id)}/open` : account(b);
  return (
    <Layout
      title="Your box is ready"
      preview={`dashboard.${name}.${ZONE} is up, with HTTPS. Open your dashboard to sign in.`}
      why={ownerWhy(b)}
    >
      <HeroPicture />
      <Eyebrow tone="accent">Ready</Eyebrow>
      <H1 hero>Your box is ready</H1>
      <P>
        {b.name ? <strong>{b.name}</strong> : "Your box"} is running in your Hetzner project, with HTTPS on its own address. Everything below is
        yours.
      </P>
      <Facts
        rows={[
          { label: "Dashboard", value: <A href={dash}><Code>{`dashboard.${name}.${ZONE}`}</Code></A> },
          { label: "Box", value: <Code>{`${name}.${ZONE}`}</Code> },
          { label: "Apps", value: <Code>{`<project>.${name}.${ZONE}`}</Code> },
        ]}
      />
      <Cta href={open}>Open your dashboard</Cta>
      <P quiet>
        The button signs you in with a one-time link your box made. It works once, within 24 hours; if it runs out, ask for a new one in{" "}
        <A href={account(b)}>your account</A>.
      </P>
      <Rule />
      <Eyebrow>Next</Eyebrow>
      <Steps
        items={[
          {
            title: "Add a passkey",
            body: "In the dashboard, under Settings. From then on you sign in on the box itself.",
          },
          {
            title: "Connect your agent",
            body: (
              <>
                Create an API key in the dashboard, then give your agent{" "}
                <A href="https://shiptiffin.com/agent-setup.md">shiptiffin.com/agent-setup.md</A>. It connects itself over MCP.
              </>
            ),
          },
          {
            title: "Deploy your first app",
            body: (
              <>
                Ask your agent, or run <Code>tiffin deploy</Code> in a folder. Each project gets its own address, like <Code>{`blog.${name}.${ZONE}`}</Code>.
              </>
            ),
          },
        ]}
      />
      <Panel
        title="What we did"
        items={[
          "Built the server, its firewall and its data volume in your own Hetzner project.",
          "Removed our access: our setup key is off the server and SSH is closed.",
          "Forgot your Hetzner key. The server is yours to keep.",
        ]}
      />
    </Layout>
  );
}

/** The open tin, on a plate of the page's paper (the picture's own background), in both themes. */
function HeroPicture() {
  return (
    <table role="presentation" width="100%" cellPadding={0} cellSpacing={0} border={0} style={{ margin: "0 0 28px" }}>
      <tbody>
        <tr>
          <td align="center" style={{ backgroundColor: "#f9f6f1", borderRadius: "10px", padding: "14px 0 6px" }}>
            <Picture src={READY} width={176} height={176} alt="The ShipTiffin tin, open, with its tiers unpacked" />
          </td>
        </tr>
      </tbody>
    </table>
  );
}

// ---- setup ----------------------------------------------------------------

export function Paid({ b }: { b: BoxCtx }) {
  const start = `${b.site}/start`;
  return (
    <Notice b={b} title="Payment received" preview="Next, connect your Hetzner project and pick a name for your box." why="Sent because you paid for a ShipTiffin box.">
      <Eyebrow tone="ok">Payment received</Eyebrow>
      <H1>Next, connect Hetzner</H1>
      <P>Thanks, your payment went through.</P>
      <P>Connect your Hetzner project, then pick a name, a size and a place for your box. Setup takes about five minutes.</P>
      <Cta href={start}>Continue setup</Cta>
      <LinkOut href={start} />
    </Notice>
  );
}

export function SetupFailed({ b, error }: { b: BoxCtx; error: string }) {
  const start = `${b.site}/start`;
  return (
    <Notice b={b} title="Setup didn't finish" preview="We cleaned up what this attempt made. You can try again.">
      <Eyebrow tone="danger">Setup stopped</Eyebrow>
      <H1>Setting up {b.name ?? "your box"} didn't work</H1>
      <P>
        Setting up <Named b={b} /> stopped before it was ready.
      </P>
      {error ? <Facts rows={[{ label: "What stopped it", value: error }]} labelWidth={124} /> : null}
      <Panel
        title="What we cleaned up"
        items={[
          "Removed its shiptiffin.app address.",
          "Deleted what this attempt made in your Hetzner project (only what carries this box's shiptiffin-box label).",
          "Forgot your Hetzner key.",
        ]}
      />
      <Cta href={start}>Try again</Cta>
      <P quiet>If it keeps failing, reply to this email and we'll look into it.</P>
    </Notice>
  );
}

export function Attention({ b, why }: { b: BoxCtx; why: string }) {
  return (
    <Notice b={b} title="Your box needs a look" preview="Nothing was deleted. We've been told and are looking into it.">
      <Eyebrow tone="danger">Needs a look</Eyebrow>
      <H1>{b.name ?? "Your box"} needs a look</H1>
      <P>
        {why || (
          <>
            Something went wrong with <Named b={b} /> after Tiffin was installed.
          </>
        )}
      </P>
      <P>Nothing was deleted: your server, its data and its address stay as they are. We've been told and will look into it.</P>
      <Cta href={account(b)}>See where it stands</Cta>
      <P quiet>Questions? Reply to this email.</P>
    </Notice>
  );
}

export function ServerOff({ b }: { b: BoxCtx }) {
  return (
    <Notice b={b} title="Your server may be off" preview="A resize stopped half way. Here's how to start the server again.">
      <Eyebrow tone="danger">Action needed</Eyebrow>
      <H1>Your server may be off</H1>
      <P>
        A resize of <Named b={b} /> stopped half way, and we couldn't start the server again ourselves: we no longer hold a Hetzner key for it.
      </P>
      <Steps
        items={[
          { title: "Start it in Hetzner", body: "In the Hetzner console: Servers, your server, Power on." },
          { title: "Or let us finish the resize", body: "Paste your Hetzner key in your account and resize again." },
        ]}
      />
      <Cta href={account(b)}>Open your account</Cta>
      <P quiet>Your data is untouched.</P>
    </Notice>
  );
}

// ---- deleted ----------------------------------------------------------------

export function Deleted({ b, dataDeleted, billing }: { b: BoxCtx; dataDeleted: boolean; billing: string | null }) {
  const d = domain(b);
  const title = `${b.name ? `${b.name}'s server` : "Your server"} is deleted`;
  return (
    <Notice
      b={b}
      title={title}
      preview={dataDeleted ? "The server, its data and its address are gone. Nothing more is billed." : "The server and its address are gone. Your data volume stays in Hetzner."}
    >
      <Eyebrow>Deleted</Eyebrow>
      <H1>{title}</H1>
      <P>
        As you asked, we deleted <Named b={b} />.
      </P>
      <Panel
        title="What's gone"
        items={[
          ...(d ? [<><Code>{d}</Code> and its dashboard address.</>] : []),
          "The server and its firewall, in your Hetzner project.",
          ...(dataDeleted ? ["Its data volume, with everything your apps stored."] : []),
          "The subscription: you won't be charged again.",
          "Your Hetzner API token: we forgot it.",
        ]}
      />
      {billing ? <Facts rows={[{ label: "Billing", value: billing }]} /> : null}
      {dataDeleted ? null : (
        <P>
          The data volume stays in your Hetzner project, and Hetzner bills it until you delete it there (Volumes, in the Hetzner console).
        </P>
      )}
      <P quiet>{NO_REFUND} Past invoices stay in your account.</P>
      <Cta href={`${b.site}/start?new=1`}>Set up a new box</Cta>
    </Notice>
  );
}

// ---- billing ----------------------------------------------------------------

export function DuplicateRefunded({ b }: { b: BoxCtx }) {
  return (
    <Notice b={b} title="We refunded a second payment" preview="A box needs one subscription, so we refunded the second in full.">
      <Eyebrow>Billing</Eyebrow>
      <H1>We refunded a second payment</H1>
      <P>
        Two payments came in for <Named b={b} />, probably from two checkout pages. A box needs one subscription, so we cancelled the second one and
        refunded it in full.
      </P>
      <P quiet>Stripe shows the refund within a few days.</P>
    </Notice>
  );
}

export function Refunded({ b }: { b: BoxCtx }) {
  return (
    <Notice b={b} title="Refunded" preview="Your first payment is refunded in full and the subscription has ended.">
      <Eyebrow>Billing</Eyebrow>
      <H1>Refunded</H1>
      <P>
        As asked, we refunded your first payment for <Named b={b} /> in full and ended the subscription.
      </P>
      <P>Your server and apps keep running in your Hetzner account. To stop Hetzner billing you, delete the server there.</P>
      <P quiet>The shiptiffin.app address stays for 30 days.</P>
    </Notice>
  );
}

export function ExtrasPaused({ b, until }: { b: BoxCtx; until: Date }) {
  const d = domain(b);
  return (
    <Notice b={b} title="Your subscription has ended" preview="Your server and apps keep running. Here's what stops, and when.">
      <Eyebrow>Subscription ended</Eyebrow>
      <H1>Your subscription has ended</H1>
      <P>
        The subscription for <Named b={b} /> is no longer active. Your server and every app on it keep running, untouched.
      </P>
      <Facts
        rows={[
          { label: "What stops", value: "Automatic Tiffin updates, our monitoring emails and support" },
          ...(d ? [{ label: "The address", value: <><Code>{d}</Code> works until {day(until)}</> }] : []),
        ]}
      />
      <P>
        After that we remove the address. Before then, point a domain of your own at the box: <Code>tiffin domain set</Code>, or Settings › Your box ›
        Domain in its dashboard.
      </P>
      <Cta href={account(b)}>Resume the subscription</Cta>
    </Notice>
  );
}

export function ExtrasResumed({ b }: { b: BoxCtx }) {
  return (
    <Notice b={b} title="Managed again" preview="Updates, monitoring and the shiptiffin.app address are back on.">
      <Eyebrow tone="ok">Subscription active</Eyebrow>
      <H1>{b.name ?? "Your box"} is managed again</H1>
      <P>
        The subscription for <Named b={b} /> is active again: updates, monitoring and the shiptiffin.app address are back on.
      </P>
    </Notice>
  );
}

export function PaymentFailed({ b }: { b: BoxCtx }) {
  return (
    <Notice b={b} title="A payment didn't go through" preview="Stripe will try again. Your server and apps keep running.">
      <Eyebrow tone="danger">Billing</Eyebrow>
      <H1>A payment didn't go through</H1>
      <P>
        We couldn't take this month's payment for <Named b={b} />. Stripe will try again over the next two weeks.
      </P>
      <P>Your server and apps keep running whatever happens.</P>
      <Cta href={account(b)}>Update your card</Cta>
      <P quiet>It's under Billing in your account.</P>
    </Notice>
  );
}

// ---- the address ---------------------------------------------------------------

export function DnsSoon({ b, on }: { b: BoxCtx; on: Date }) {
  const d = domain(b) ?? "Your address";
  return (
    <Notice b={b} title={`${d} goes on ${day(on)}`} preview={`The shiptiffin.app address stops on ${day(on)}. Your server and apps keep running.`}>
      <Eyebrow>Reminder</Eyebrow>
      <H1>
        {domain(b) ? <Code>{d}</Code> : d} goes on {day(on)}
      </H1>
      <P>
        The subscription for <Named b={b} /> ended, so its shiptiffin.app address stops on {day(on)}. Your server and apps keep running.
      </P>
      <P>Before then, point a domain of your own at the box (Settings › Your box › Domain in its dashboard), or pick the subscription up again.</P>
      <Cta href={account(b)}>Keep the address</Cta>
    </Notice>
  );
}

export function DnsRemoved({ b }: { b: BoxCtx }) {
  const d = domain(b) ?? "Your address";
  return (
    <Notice b={b} title={`${d} has been removed`} preview="Your server and apps still run at their own IP and any domain you set.">
      <Eyebrow>Address removed</Eyebrow>
      <H1>{domain(b) ? <Code>{d}</Code> : d} has been removed</H1>
      <P>
        As announced, we removed the shiptiffin.app address of <Named b={b} />. The server and apps still run at their own IP address and any domain
        you set.
      </P>
      <P>Pick the subscription up again and the address comes back.</P>
      <Cta href={account(b)}>Resume the subscription</Cta>
    </Notice>
  );
}

export function Parked({ b }: { b: BoxCtx }) {
  const d = domain(b) ?? "your box's address";
  return (
    <Notice b={b} title={`We parked ${d}`} preview="Your box hasn't checked in for 72 hours. It comes back at its next check-in.">
      <Eyebrow>Address parked</Eyebrow>
      <H1>We parked {domain(b) ? <Code>{d}</Code> : d}</H1>
      <P>
        <Named b={b} /> hasn't checked in for 72 hours, so we took its shiptiffin.app address off the server's IP (if the server was deleted,
        Hetzner may give that IP to someone else).
      </P>
      <P>If the server is still yours, start it in the Hetzner console: the address comes back within a few hours of its next check-in.</P>
      <Cta href={account(b)}>Open your account</Cta>
    </Notice>
  );
}

export function Killed({ b, reason }: { b: BoxCtx; reason: string }) {
  const d = domain(b) ?? "your shiptiffin.app address";
  return (
    <Notice b={b} title={`We turned off ${d}`} preview="After an abuse report. Your server and apps are untouched.">
      <Eyebrow tone="danger">Address turned off</Eyebrow>
      <H1>We turned off {domain(b) ? <Code>{d}</Code> : d}</H1>
      <P>After an abuse report we removed the address {domain(b) ? <Code>{d}</Code> : null}.</P>
      {reason ? <Facts rows={[{ label: "Reason", value: reason }]} /> : null}
      <P>Your server and apps are untouched, and still reachable at their own IP and domains.</P>
      <P quiet>If you think this is a mistake, reply to this email.</P>
    </Notice>
  );
}

// ---- the monitor -------------------------------------------------------------

export function Down({ b, since }: { b: BoxCtx; since: Date }) {
  return (
    <Notice b={b} title={`${b.name ?? "Your box"} isn't answering`} preview={`No answer to our checks since ${moment(since)}.`}>
      <Eyebrow tone="danger">Not answering</Eyebrow>
      <H1>{b.name ?? "Your box"} isn't answering</H1>
      <P>
        {b.name ? <Code>{`dashboard.${boxDomain(b.name)}`}</Code> : "Your box"} hasn't answered our checks since {moment(since)}.
      </P>
      <P>Check the server in the Hetzner console: is it running, is its disk full? We'll email again when it's back.</P>
      <Cta href={account(b)}>Open your account</Cta>
    </Notice>
  );
}

export function Up({ b }: { b: BoxCtx }) {
  return (
    <Notice b={b} title={`${b.name ?? "Your box"} is answering again`} preview="It's answering our checks again. Nothing to do.">
      <Eyebrow tone="ok">Back</Eyebrow>
      <H1>{b.name ?? "Your box"} is answering again</H1>
      <P>
        {b.name ? <Code>{`dashboard.${boxDomain(b.name)}`}</Code> : "Your box"} is answering our checks again. There's nothing to do.
      </P>
    </Notice>
  );
}

export function Silent({ b, last }: { b: BoxCtx; last: Date | null }) {
  return (
    <Notice b={b} title={`${b.name ?? "Your box"} hasn't checked in`} preview="Is the server running? Look in the Hetzner console.">
      <Eyebrow>No check-in</Eyebrow>
      <H1>{b.name ?? "Your box"} hasn't checked in</H1>
      <P>
        <Named b={b} /> checks in with us every six hours. Its last check-in was {last ? moment(last) : "never"}.
      </P>
      <P>Is the server running? Look in the Hetzner console.</P>
      <P quiet>
        After 72 hours without a check-in we take its shiptiffin.app address off the server's IP (if the server was deleted, Hetzner may give that
        IP to someone else). It comes back at the next check-in.
      </P>
    </Notice>
  );
}

// ---- subjects ---------------------------------------------------------------

const label = (b: BoxCtx) => b.name ?? "Your box";

/** Every box email: its subject and its body. */
export const boxEmails = {
  paid: (b: BoxCtx): Built => ({ subject: "Payment received: next, connect Hetzner", element: <Paid b={b} /> }),
  ready: (b: BoxCtx): Built => ({ subject: b.name ? `Your box ${b.name} is ready` : "Your box is ready", element: <Ready b={b} /> }),
  setup_failed: (b: BoxCtx, error: string): Built => ({ subject: `Setting up ${b.name ?? "your box"} didn't work`, element: <SetupFailed b={b} error={error} /> }),
  attention: (b: BoxCtx, why: string): Built => ({ subject: `${label(b)} needs a look`, element: <Attention b={b} why={why} /> }),
  server_off: (b: BoxCtx): Built => ({ subject: `${label(b)}: your server may be off`, element: <ServerOff b={b} /> }),
  duplicate_refunded: (b: BoxCtx): Built => ({ subject: "We refunded a second payment for the same box", element: <DuplicateRefunded b={b} /> }),
  refunded: (b: BoxCtx): Built => ({ subject: `Refunded: ${b.name ?? "your box"}`, element: <Refunded b={b} /> }),
  extras_paused: (b: BoxCtx, until: Date): Built => ({
    subject: `Your ShipTiffin subscription for ${b.name ?? "your box"} has ended`,
    element: <ExtrasPaused b={b} until={until} />,
  }),
  extras_resumed: (b: BoxCtx): Built => ({ subject: `${label(b)}: managed again`, element: <ExtrasResumed b={b} /> }),
  payment_failed: (b: BoxCtx): Built => ({ subject: "A payment for your box didn't go through", element: <PaymentFailed b={b} /> }),
  dns_soon: (b: BoxCtx, on: Date): Built => ({ subject: `${domain(b) ?? "Your address"} goes on ${day(on)}`, element: <DnsSoon b={b} on={on} /> }),
  dns_removed: (b: BoxCtx): Built => ({ subject: `${domain(b) ?? "Your address"} has been removed`, element: <DnsRemoved b={b} /> }),
  down: (b: BoxCtx, since: Date): Built => ({ subject: `${label(b)} isn't answering`, element: <Down b={b} since={since} /> }),
  up: (b: BoxCtx): Built => ({ subject: `${label(b)} is answering again`, element: <Up b={b} /> }),
  silent: (b: BoxCtx, last: Date | null): Built => ({ subject: `${label(b)} hasn't checked in`, element: <Silent b={b} last={last} /> }),
  parked: (b: BoxCtx): Built => ({ subject: `We parked ${domain(b) ?? "your box's address"}`, element: <Parked b={b} /> }),
  deleted: (b: BoxCtx, dataDeleted: boolean, billing: string | null): Built => ({
    subject: `${b.name ? `${b.name}'s server` : "Your server"} is deleted`,
    element: <Deleted b={b} dataDeleted={dataDeleted} billing={billing} />,
  }),
  killed: (b: BoxCtx, reason: string): Built => ({ subject: `We turned off ${domain(b) ?? "your shiptiffin.app address"}`, element: <Killed b={b} reason={reason} /> }),
};
