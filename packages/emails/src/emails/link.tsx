import { Box, boxSample, boxVars } from "../ui/box";
import { defineEmail, v, type PropsOf } from "../ui/compile";
import { Cta, H1, LinkOut, P } from "../ui/parts";

export const spec = defineEmail({
  id: "link",
  family: "box",
  title: "A fresh sign-in link an admin made for someone",
  vars: {
    ...boxVars,
    first: v.text('First name, or "there"'),
    by: v.text('Who made the link: a name, or "The box\'s owner"'),
    url: v.url("The one-time sign-in link"),
    until: v.text('How long the link works: "for the next 30 minutes"'),
  },
  subject: (p) => `Your new sign-in link for ${p.host}`,
  preview: { ...boxSample, first: "Sam", by: "Bilal Tahir", url: "https://dashboard.shiptiffin.com/login#lnk_4Rt8sV2mQ", until: "for the next 30 minutes" },
});

export default function SignInLinkFromAdmin(p: PropsOf<typeof spec.vars>) {
  return (
    <Box p={p} preview={`${p.by} made you a new sign-in link. It works once, ${p.until}.`} why="If you didn't expect this, you can ignore it. Nobody can sign in without the link.">
      <H1 serif>A new sign-in link</H1>
      <P>Hi {p.first},</P>
      <P>{p.by} made you a new sign-in link for {p.host}.</P>
      <Cta href={p.url}>Sign in to {p.host}</Cta>
      <P quiet>The link works once, {p.until}.</P>
      <LinkOut href={p.url} />
    </Box>
  );
}
SignInLinkFromAdmin.PreviewProps = spec.preview;
