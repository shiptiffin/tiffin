import { Box, boxSample, boxVars } from "../ui/box";
import { choose, defineEmail, v, when, type PropsOf } from "../ui/compile";
import { Cta, Eyebrow, Facts, H1, P } from "../ui/parts";

export const spec = defineEmail({
  id: "alert",
  family: "box",
  title: "An alert: firing, resolved, or a test of alert delivery",
  vars: {
    ...boxVars,
    state: v.oneOf(["firing", "resolved", "test"], "What happened"),
    box: v.text("The box's name or domain"),
    rule: v.text('The rule: "disk-space"'),
    summary: v.text('One sentence: "/ is 92% full (limit 90%)"'),
    watching: v.optText('What the rule watches: "/", "project:shop"; empty to leave it out'),
    value: v.optText('The reading: "92 (limit 90)"; empty to leave it out'),
    when: v.text('"7 October 2026, 14:32 UTC"'),
    description: v.optText("The rule's description; empty to leave it out"),
    url: v.optUrl("The dashboard; empty for no button"),
  },
  subject: (p) =>
    `${choose(p, "state", { firing: "Firing", resolved: "Resolved", test: "Test" })}: ${p.rule} on ${p.box}`,
  preview: {
    ...boxSample,
    state: "firing",
    box: "shiptiffin.com",
    rule: "disk-space",
    summary: "/ is 92% full (the limit is 90%).",
    watching: "/",
    value: "92% (limit 90%)",
    when: "7 October 2026, 14:32 UTC",
    description: "Warns before the disk fills up and the box stops accepting writes.",
    url: "https://dashboard.shiptiffin.com/observe",
  },
});

export default function Alert(p: PropsOf<typeof spec.vars>) {
  return (
    <Box p={p} preview={p.summary} why={`You get alerts for ${p.box} because this address is set in Observe › Settings.`}>
      {choose(
        p,
        "state",
        {
          firing: <Eyebrow tone="danger">Alert firing</Eyebrow>,
          resolved: <Eyebrow tone="ok">Resolved</Eyebrow>,
        },
        <Eyebrow>Test alert</Eyebrow>,
      )}
      <H1 serif>
        {choose(p, "state", { firing: <>{p.rule} needs a look</>, resolved: <>{p.rule} is back to normal</> }, "Alerts reach you", { inline: true })}
      </H1>
      <P>
        {choose(
          p,
          "state",
          { firing: <>{p.summary}</>, resolved: <>{p.summary}</> },
          <>This is a test from {p.box}. When a rule fires, you'll get an email like this one.</>,
          { inline: true },
        )}
      </P>
      {choose(p, "state", {
        firing: (
          <Facts
            rows={[
              { label: "Watching", value: p.watching, wrap: (r) => when(p, "watching", r) },
              { label: "Reading", value: p.value, wrap: (r) => when(p, "value", r) },
              { label: "Since", value: p.when },
              { label: "Box", value: p.box },
            ]}
          />
        ),
        resolved: (
          <Facts
            rows={[
              { label: "Watching", value: p.watching, wrap: (r) => when(p, "watching", r) },
              { label: "Reading", value: p.value, wrap: (r) => when(p, "value", r) },
              { label: "Cleared", value: p.when },
              { label: "Box", value: p.box },
            ]}
          />
        ),
      })}
      {when(p, "description", <P quiet>{p.description}</P>)}
      {when(p, "url", <Cta href={p.url}>Open the dashboard</Cta>)}
    </Box>
  );
}
Alert.PreviewProps = spec.preview;
