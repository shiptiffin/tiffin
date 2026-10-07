import { Box, boxSample, boxVars } from "../ui/box";
import { defineEmail, v, whenInline, type PropsOf } from "../ui/compile";
import { Cta, H1, LinkOut, P } from "../ui/parts";

export const spec = defineEmail({
  id: "sign-in",
  family: "box",
  title: 'The link someone asked for with "Email me a sign-in link"',
  vars: {
    ...boxVars,
    first: v.text('First name, or "there"'),
    url: v.url("The one-time sign-in link"),
    until: v.text('How long the link works: "for the next 15 minutes"'),
    ip: v.optText("The address the request came from; empty to leave it out"),
  },
  subject: (p) => `Sign in to ${p.host}`,
  preview: { ...boxSample, first: "Sam", url: "https://dashboard.shiptiffin.com/login#eml_9Qw3zL6pK", until: "for the next 15 minutes", ip: "203.0.113.24" },
});

export default function SignIn(p: PropsOf<typeof spec.vars>) {
  return (
    <Box p={p} preview={`Your link to sign in to ${p.host}. It works once, ${p.until}.`} why="If you didn't ask for this, you can ignore it. Nobody can sign in without the link.">
      <H1 serif>Your sign-in link</H1>
      <P>Hi {p.first},</P>
      <P>Here's the sign-in link you asked for.</P>
      <Cta href={p.url}>Sign in</Cta>
      <P quiet>
        The link works once, {p.until}.{whenInline(p, "ip", <> It was asked for from {p.ip}.</>)}
      </P>
      <LinkOut href={p.url} />
    </Box>
  );
}
SignIn.PreviewProps = spec.preview;
