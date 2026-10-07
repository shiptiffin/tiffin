import { App, AppCta, appSample, appVars } from "../ui/app";
import { defineEmail, v, type PropsOf } from "../ui/compile";
import { H1, LinkOut, P } from "../ui/parts";

export const spec = defineEmail({
  id: "magic-link",
  family: "app",
  title: "A one-time link to sign in without a password",
  vars: { ...appVars, url: v.url("The sign-in link"), expiresIn: v.text('"10 minutes"') },
  subject: (p) => `Sign in to ${p.app}`,
  preview: { ...appSample, url: "https://larder.app/api/auth/magic-link/verify?token=c2lnbi1pbg", expiresIn: "10 minutes" },
});

export default function MagicLink(p: PropsOf<typeof spec.vars>) {
  return (
    <App p={p} preview={`Your link to sign in to ${p.app}. It works once, for ${p.expiresIn}.`} why="If you didn't ask to sign in, ignore this email. Nobody can sign in without the link.">
      <H1>Sign in to {p.app}</H1>
      <P>Use the button below to sign in as {p.email}.</P>
      <AppCta p={p} href={p.url}>
        Sign in to {p.app}
      </AppCta>
      <P quiet>The link works once and expires in {p.expiresIn}.</P>
      <LinkOut href={p.url} />
    </App>
  );
}
MagicLink.PreviewProps = spec.preview;
