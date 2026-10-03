"use client";
import { useState, type FormEvent, type ReactNode } from "react";
import { errorText, useCaptcha, useTiffinAuth } from "./client";
import { IconArrowLeft, IconMail } from "./icons";
import { Skeleton, SocialButtons } from "./sign-in";
import { Alert, Button, Card, Field, Head, Or, PasswordField, Root, type Common } from "./ui";

export type SignUpProps = Common & {
  /** Where people land after confirming their email. Default "/". */
  redirectTo?: string;
  /** Link or handler for "Sign in". Omit to hide it. */
  signInUrl?: string;
  onSignIn?: () => void;
  logo?: ReactNode;
  title?: ReactNode;
};

/**
 * Create an account. With passwords on, people choose one and confirm their
 * email; with only email links on, they get a link that signs them up.
 */
export function SignUp(props: SignUpProps) {
  const { client, baseURL, config, configError } = useTiffinAuth();
  const captcha = useCaptcha(baseURL, config?.captcha ?? false);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [sent, setSent] = useState<"verify" | "link" | null>(null);
  const redirectTo = props.redirectTo ?? "/";
  const pw = !!config?.methods.includes("email");
  const link = !!config?.methods.includes("magic-link");

  const run = async (key: string, f: () => Promise<void>) => {
    setBusy(key);
    setError(null);
    try {
      await f();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(null);
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void run("submit", async () => {
      if (pw) {
        const { error } = await client.signUp.email({ name: name.trim() || email.split("@")[0]!, email, password, callbackURL: redirectTo }, { headers: await captcha() });
        if (error) throw error;
        setSent("verify");
      } else {
        const { error } = await client.signIn.magicLink({ email, name: name.trim() || undefined, callbackURL: redirectTo }, { headers: await captcha() });
        if (error) throw error;
        setSent("link");
      }
    });
  };
  const social = (provider: "google" | "github") =>
    run(provider, async () => {
      const { error } = await client.signIn.social({ provider, callbackURL: redirectTo });
      if (error) throw error;
    });

  const app = config?.appName ?? "";
  let body: ReactNode;
  if (!config) body = configError ? <Alert>{configError}</Alert> : <Skeleton />;
  else if (sent) {
    body = (
      <div className="tf-view">
        <div className="tf-badge-icon">
          <IconMail />
        </div>
        <Head
          title="Check your inbox"
          sub={
            sent === "verify" ? (
              <>
                We sent a link to <b>{email}</b>. Open it to confirm your email and you're in.
              </>
            ) : (
              <>
                We sent a sign-in link to <b>{email}</b>. It works once, for the next 10 minutes.
              </>
            )
          }
        />
        <div className="tf-alt">
          <button type="button" className="tf-link" onClick={() => setSent(null)}>
            <IconArrowLeft width={13} height={13} style={{ verticalAlign: "-2px", marginRight: 4 }} />
            Use a different email
          </button>
        </div>
      </div>
    );
  } else {
    body = (
      <form className="tf-view" onSubmit={submit}>
        <Head logo={props.logo} title={props.title ?? `Create your ${app} account`} sub="It takes a minute. No card, no spam." />
        <div className="tf-stack">
          <SocialButtons config={config} busy={busy} onClick={(p) => void social(p)} />
          {config.social.google || config.social.github ? <Or /> : null}
          <Field label="Name" name="name" autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Ada Lovelace" />
          <Field label="Email" type="email" name="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" />
          {pw ? (
            <PasswordField
              label="Password"
              name="password"
              autoComplete="new-password"
              required
              minLength={8}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              hint={password.length > 0 && password.length < 8 ? `${8 - password.length} more character${8 - password.length === 1 ? "" : "s"}` : "At least 8 characters. A short sentence works well."}
            />
          ) : null}
          {error ? <Alert>{error}</Alert> : null}
          <Button type="submit" busy={busy === "submit"} disabled={pw && password.length > 0 && password.length < 8}>
            {pw ? "Create account" : link ? "Email me a link" : "Continue"}
          </Button>
        </div>
      </form>
    );
  }
  return (
    <Root className={props.className} theme={props.theme}>
      <Card label="Create an account">{body}</Card>
      {props.signInUrl || props.onSignIn ? (
        <p className="tf-foot">
          Have an account?{" "}
          {props.onSignIn ? (
            <button type="button" className="tf-link" onClick={props.onSignIn}>
              Sign in
            </button>
          ) : (
            <a className="tf-link" href={props.signInUrl}>
              Sign in
            </a>
          )}
        </p>
      ) : null}
    </Root>
  );
}

/** The page the password reset email links to: reads ?token= and sets a new password. */
export function ResetPassword(props: Common & { token?: string; signInUrl?: string; onDone?: () => void }) {
  const { client } = useTiffinAuth();
  const token = props.token ?? (typeof window !== "undefined" ? (new URLSearchParams(window.location.search).get("token") ?? "") : "");
  const linkError = typeof window !== "undefined" ? new URLSearchParams(window.location.search).get("error") : null;
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(linkError ? "This reset link has expired or was already used. Ask for a new one." : null);
  const [done, setDone] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    const { error } = await client.resetPassword({ newPassword: password, token });
    setBusy(false);
    if (error) setError(errorText(error));
    else {
      setDone(true);
      props.onDone?.();
    }
  };
  return (
    <Root className={props.className} theme={props.theme}>
      <Card label="Reset password">
        {done ? (
          <div className="tf-view">
            <Head title="Password changed" sub="You've been signed out everywhere else. Sign in with your new password." />
            {props.signInUrl ? (
              <a className="tf-btn tf-btn-primary" href={props.signInUrl}>
                <span>Sign in</span>
              </a>
            ) : null}
          </div>
        ) : (
          <form className="tf-view" onSubmit={submit}>
            <Head title="Choose a new password" sub="At least 8 characters. You'll be signed out on your other devices." />
            <div className="tf-stack">
              <PasswordField label="New password" autoComplete="new-password" required minLength={8} autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />
              {error ? <Alert>{error}</Alert> : null}
              <Button type="submit" busy={busy} disabled={!token || password.length < 8}>
                Save password
              </Button>
            </div>
          </form>
        )}
      </Card>
    </Root>
  );
}
