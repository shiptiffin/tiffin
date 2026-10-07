import { App, appSample, appVars } from "../ui/app";
import { defineEmail, v, type PropsOf } from "../ui/compile";
import { Code, H1, P } from "../ui/parts";

export const spec = defineEmail({
  id: "two-factor",
  family: "app",
  title: "The second step of a sign-in: a code by email",
  vars: { ...appVars, code: v.text("The code"), expiresIn: v.text('"3 minutes"') },
  subject: (p) => `${p.code} is your ${p.app} verification code`,
  preview: { ...appSample, code: "730148", expiresIn: "3 minutes" },
});

export default function TwoFactor(p: PropsOf<typeof spec.vars>) {
  return (
    <App p={p} preview={`Your code to finish signing in to ${p.app}.`} why={`You got this because two-step sign-in is on for ${p.email}.`}>
      <H1>Finish signing in</H1>
      <P>Someone just entered your password to sign in to {p.app}. If it was you, enter this code to finish.</P>
      <Code>{p.code}</Code>
      <P quiet>It works once and expires in {p.expiresIn}.</P>
      <P quiet>If it wasn't you, someone else knows your password. They can't get in without this code, but change your password now.</P>
    </App>
  );
}
TwoFactor.PreviewProps = spec.preview;
