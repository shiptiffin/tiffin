import { App, AppCta, appSample, appVars } from "../ui/app";
import { defineEmail, v, type PropsOf } from "../ui/compile";
import { H1, LinkOut, P } from "../ui/parts";

export const spec = defineEmail({
  id: "verify-email",
  family: "app",
  title: "Confirm a new account's email address",
  vars: { ...appVars, url: v.url("The confirmation link"), expiresIn: v.text('"1 hour"') },
  subject: (p) => `Confirm your email for ${p.app}`,
  preview: { ...appSample, url: "https://larder.app/api/auth/verify-email?token=eyJhbGciOi", expiresIn: "1 hour" },
});

export default function VerifyEmail(p: PropsOf<typeof spec.vars>) {
  return (
    <App p={p} preview={`One click and your ${p.app} account is ready.`} why={`You got this because someone signed up to ${p.app} with ${p.email}. If it wasn't you, ignore this email and the address won't be confirmed.`}>
      <H1>Confirm your email</H1>
      <P>Thanks for signing up to {p.app}. Confirm that {p.email} is your address and you're all set.</P>
      <AppCta p={p} href={p.url}>
        Confirm email
      </AppCta>
      <P quiet>The link expires in {p.expiresIn}.</P>
      <LinkOut href={p.url} />
    </App>
  );
}
VerifyEmail.PreviewProps = spec.preview;
