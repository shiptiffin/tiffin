import { Box, boxSample, boxVars } from "../ui/box";
import { defineEmail, v, when, type PropsOf } from "../ui/compile";
import { Cta, Eyebrow, Facts, H1, P } from "../ui/parts";

export const spec = defineEmail({
  id: "new-sign-in",
  family: "box",
  title: "Someone signed in from a browser the box hasn't seen them use",
  vars: {
    ...boxVars,
    first: v.text('First name, or "there"'),
    device: v.text('"Chrome on macOS"'),
    when: v.text('"7 October 2026, 14:32 UTC"'),
    via: v.text('"a sign-in link", "a passkey"'),
    ip: v.optText("The address it came from; empty to leave it out"),
    owner: v.flag("They own the box (what to do if it wasn't them differs)"),
    url: v.optUrl("Where to check sign-ins and keys (the dashboard's Settings); empty for no button"),
  },
  subject: (p) => `New sign-in to ${p.host}`,
  preview: {
    ...boxSample,
    first: "Sam",
    device: "Safari on iPhone",
    when: "7 October 2026, 14:32 UTC",
    via: "a passkey",
    ip: "198.51.100.7",
    owner: true,
    url: "https://dashboard.shiptiffin.com/settings",
  },
});

export default function NewSignIn(p: PropsOf<typeof spec.vars>) {
  return (
    <Box p={p} preview={`You signed in from ${p.device}. If that was you, there's nothing to do.`} why="The box sends this once for each new browser, not on every sign-in.">
      <Eyebrow>Security notice</Eyebrow>
      <H1 serif>New sign-in to {p.host}</H1>
      <P>Hi {p.first},</P>
      <P>You just signed in to {p.host} from a browser the box hasn't seen you use before.</P>
      <Facts
        rows={[
          { label: "Browser", value: p.device },
          { label: "When", value: p.when },
          { label: "With", value: p.via },
          { label: "From", value: p.ip, wrap: (row) => when(p, "ip", row) },
        ]}
      />
      <P>If this was you, there's nothing to do.</P>
      {when(
        p,
        "owner",
        <P>If it wasn't, sign in now and check Settings for passkeys and API keys you don't recognise.</P>,
        <P>If it wasn't, tell the box's owner straight away. Removing your access ends every session.</P>,
      )}
      {when(p, "url", <Cta href={p.url}>Review sign-ins</Cta>)}
    </Box>
  );
}
NewSignIn.PreviewProps = spec.preview;
