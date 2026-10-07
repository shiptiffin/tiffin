import { App, appSample, appVars } from "../ui/app";
import { choose, defineEmail, v, type PropsOf } from "../ui/compile";
import { Code, H1, P } from "../ui/parts";

export const spec = defineEmail({
  id: "otp",
  family: "app",
  title: "A one-time code to sign in, confirm an email or reset a password",
  vars: {
    ...appVars,
    code: v.text("The code"),
    purpose: v.oneOf(["sign-in", "email-verification", "forget-password", "change-email"], "What the code is for (Better Auth's type)"),
    expiresIn: v.text('"5 minutes"'),
  },
  subject: (p) => `${p.code} is your ${p.app} code`,
  preview: { ...appSample, code: "482913", purpose: "sign-in", expiresIn: "5 minutes" },
});

export default function Otp(p: PropsOf<typeof spec.vars>) {
  const inline = { inline: true };
  return (
    <App p={p} preview={`Enter it in ${p.app} within ${p.expiresIn}.`} why="If you didn't ask for a code, ignore this email. Nobody can use it without access to your inbox.">
      <H1>
        {choose(
          p,
          "purpose",
          { "sign-in": "Your sign-in code", "email-verification": "Confirm your email", "forget-password": "Reset your password", "change-email": "Confirm your new email" },
          undefined,
          inline,
        )}
      </H1>
      <P>
        {choose(
          p,
          "purpose",
          {
            "sign-in": <>Enter this code in {p.app} to sign in as {p.email}.</>,
            "email-verification": <>Enter this code in {p.app} to confirm {p.email} is your address.</>,
            "forget-password": <>Enter this code in {p.app} to choose a new password for {p.email}.</>,
            "change-email": <>Enter this code in {p.app} to make {p.email} your new address.</>,
          },
          undefined,
          inline,
        )}
      </P>
      <Code>{p.code}</Code>
      <P quiet>It works once and expires in {p.expiresIn}. Nobody from {p.app} will ever ask you for it.</P>
    </App>
  );
}
Otp.PreviewProps = spec.preview;
