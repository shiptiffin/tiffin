import { App, AppCta, appSample, appVars } from "../ui/app";
import { defineEmail, v, type PropsOf } from "../ui/compile";
import { Facts, H1, LinkOut, P } from "../ui/parts";

export const spec = defineEmail({
  id: "change-email",
  family: "app",
  title: "Approve a change of email address (sent to the current address)",
  vars: { ...appVars, url: v.url("The approve link"), newEmail: v.text("The address they want to change to"), expiresIn: v.text('"1 hour"') },
  subject: (p) => `Approve your new email for ${p.app}`,
  preview: { ...appSample, url: "https://larder.app/api/auth/verify-email?token=Y2hhbmdl", newEmail: "sam.lee@example.org", expiresIn: "1 hour" },
});

export default function ChangeEmail(p: PropsOf<typeof spec.vars>) {
  return (
    <App p={p} preview={`Someone asked to move your ${p.app} account to ${p.newEmail}.`} why="If you didn't ask for this, ignore this email; your address stays the same. Consider changing your password.">
      <H1>Approve your new email</H1>
      <P>Someone asked to change the email on your {p.app} account. If it was you, approve the change.</P>
      <Facts
        rows={[
          { label: "From", value: p.email },
          { label: "To", value: p.newEmail },
        ]}
      />
      <AppCta p={p} href={p.url}>
        Approve the change
      </AppCta>
      <P quiet>The link expires in {p.expiresIn}. Nothing changes unless you approve.</P>
      <LinkOut href={p.url} />
    </App>
  );
}
ChangeEmail.PreviewProps = spec.preview;
