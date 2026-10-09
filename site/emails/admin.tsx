// For us, not customers: a Stripe action the outbox can't get through.
import { Code, Cta, Eyebrow, Facts, H1, Layout, P } from "./ui";

export type Stuck = { box: string; kind: string; key: string; subscription: string; since: string; attempts: string; error: string; admin: string };

export function StuckAction({ s }: { s: Stuck }) {
  return (
    <Layout title="A Stripe action is stuck" preview="Still being retried. A customer may be paying twice until it goes through." why="Sent to the CLOUD_ABUSE_NOTIFY address, once per stuck action.">
      <Eyebrow tone="danger">Stuck</Eyebrow>
      <H1>A Stripe action is stuck</H1>
      <P>A Stripe action has not gone through for over an hour, and is still being retried. A customer may be paying twice until it does.</P>
      <Facts
        labelWidth={116}
        rows={[
          { label: "Box", value: <Code>{s.box}</Code> },
          { label: "Action", value: <><Code>{s.kind}</Code> ({s.key})</> },
          { label: "Subscription", value: <Code>{s.subscription}</Code> },
          { label: "Queued", value: s.since },
          { label: "Attempts", value: s.attempts },
          { label: "Last error", value: s.error },
        ]}
      />
      <P>Look in Stripe, and at the admin page.</P>
      <Cta href={s.admin}>Open the admin page</Cta>
    </Layout>
  );
}

export type Abuse = { target: string; box: string | null; from: string | null; details: string; admin: string };

export function AbuseReport({ r }: { r: Abuse }) {
  return (
    <Layout title="Abuse report" preview={`About ${r.target}. Act on it from the admin page.`} why="Sent to the CLOUD_ABUSE_NOTIFY address for every report.">
      <Eyebrow tone="danger">Abuse report</Eyebrow>
      <H1>A report about {r.target}</H1>
      <Facts
        labelWidth={88}
        rows={[
          { label: "Target", value: <Code>{r.target}</Code> },
          { label: "Box", value: r.box ? <Code>{r.box}</Code> : "unknown" },
          { label: "From", value: r.from ?? "anonymous" },
        ]}
      />
      <P>
        <span style={{ whiteSpace: "pre-wrap" }}>{r.details}</span>
      </P>
      <Cta href={r.admin}>Act on it</Cta>
    </Layout>
  );
}
