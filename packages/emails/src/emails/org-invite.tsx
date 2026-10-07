import { App, AppCta, appSample, appVars } from "../ui/app";
import { defineEmail, v, whenInline, type PropsOf } from "../ui/compile";
import { H1, LinkOut, P } from "../ui/parts";

export const spec = defineEmail({
  id: "org-invite",
  family: "app",
  title: "An invitation to join an organization in the app",
  vars: {
    ...appVars,
    url: v.url("The accept link"),
    inviter: v.text("Who invited them: a name, or their email"),
    inviterEmail: v.optText("The inviter's email, when inviter is a name; empty to leave it out"),
    org: v.text("The organization's name"),
    role: v.text('The role they get: "member", "admin"'),
    expiresIn: v.text('"48 hours"'),
  },
  subject: (p) => `${p.inviter} invited you to ${p.org} on ${p.app}`,
  preview: {
    ...appSample,
    url: "https://larder.app/accept-invite?invitation=inv_2Lk9",
    inviter: "Priya Shah",
    inviterEmail: "priya@example.com",
    org: "Northside Kitchen",
    role: "member",
    expiresIn: "48 hours",
  },
});

export default function OrgInvite(p: PropsOf<typeof spec.vars>) {
  return (
    <App p={p} preview={`Join ${p.org} on ${p.app}. The invitation lasts ${p.expiresIn}.`} why={`If you don't know ${p.inviter} or weren't expecting this, ignore this email. You won't be added.`}>
      <H1>Join {p.org}</H1>
      <P>
        {p.inviter}
        {whenInline(p, "inviterEmail", <> ({p.inviterEmail})</>)} invited you to join {p.org} on {p.app}, with the {p.role} role.
      </P>
      <AppCta p={p} href={p.url}>
        Accept invitation
      </AppCta>
      <P quiet>The invitation expires in {p.expiresIn}. If you don't have an account yet, you can make one when you accept.</P>
      <LinkOut href={p.url} />
    </App>
  );
}
OrgInvite.PreviewProps = spec.preview;
