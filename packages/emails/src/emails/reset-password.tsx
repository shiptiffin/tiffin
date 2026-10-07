import { App, AppCta, appSample, appVars } from "../ui/app";
import { defineEmail, v, type PropsOf } from "../ui/compile";
import { H1, LinkOut, P } from "../ui/parts";

export const spec = defineEmail({
  id: "reset-password",
  family: "app",
  title: "A link to choose a new password",
  vars: { ...appVars, url: v.url("The reset link"), expiresIn: v.text('"1 hour"') },
  subject: (p) => `Reset your ${p.app} password`,
  preview: { ...appSample, url: "https://larder.app/reset-password?token=Qm9vdHN0cmFw", expiresIn: "1 hour" },
});

export default function ResetPassword(p: PropsOf<typeof spec.vars>) {
  return (
    <App p={p} preview={`Choose a new password for ${p.email}. The link works for ${p.expiresIn}.`} why="If you didn't ask for this, ignore this email. Your password won't change.">
      <H1>Reset your password</H1>
      <P>Someone asked to reset the password for {p.email} on {p.app}. If it was you, choose a new one.</P>
      <AppCta p={p} href={p.url}>
        Choose a new password
      </AppCta>
      <P quiet>
        The link works once and expires in {p.expiresIn}. Your current password keeps working until you change it, and changing it signs you out on your
        other devices.
      </P>
      <LinkOut href={p.url} />
    </App>
  );
}
ResetPassword.PreviewProps = spec.preview;
