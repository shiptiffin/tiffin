"use client";
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { errorText, useCaptcha, useTiffinAuth, type AuthConfig } from "./client";
import { IconArrowLeft, IconGitHub, IconGoogle, IconMail, IconPasskey, IconShield } from "./icons";
import { Alert, Button, Card, Field, Head, OtpInput, Or, PasswordField, Root, type Common } from "./ui";

export type SignInProps = Common & {
  /** Where to go after signing in (also used for magic links and social sign-in). Default "/". */
  redirectTo?: string;
  /** Called after a successful sign-in instead of navigating to redirectTo. */
  onSignedIn?: () => void;
  /** Link or handler for "Create an account". Omit to hide it. */
  signUpUrl?: string;
  onSignUp?: () => void;
  /** Your logo, shown above the title. */
  logo?: ReactNode;
  /** Override the title (default "Sign in to <app>"). */
  title?: ReactNode;
  /** Where the password reset link lands (mount <ResetPassword/> there). Default "/reset-password". */
  resetPasswordUrl?: string;
};

type View = "start" | "check-email" | "code" | "two-factor" | "forgot" | "forgot-sent";

function go(url: string) {
  if (typeof window !== "undefined") window.location.assign(url);
}

export function SignIn(props: SignInProps) {
  const { client, baseURL, config, configError } = useTiffinAuth();
  const captcha = useCaptcha(baseURL, config?.captcha ?? false);
  const [view, setView] = useState<View>("start");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [trust, setTrust] = useState(true);
  const redirectTo = props.redirectTo ?? "/";
  const done = () => (props.onSignedIn ? props.onSignedIn() : go(redirectTo));

  const has = (m: string) => !!config?.methods.includes(m);
  const run = async (key: string, f: () => Promise<void>) => {
    setBusy(key);
    setError(null);
    setNotice(null);
    try {
      await f();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(null);
    }
  };
  const fail = (e: unknown) => {
    throw e;
  };

  const withPassword = (e: FormEvent) => {
    e.preventDefault();
    void run("password", async () => {
      const { data, error } = await client.signIn.email({ email, password, callbackURL: redirectTo }, { headers: await captcha() });
      if (error) {
        if (error.code === "EMAIL_NOT_VERIFIED") {
          setView("check-email");
          setNotice("verify");
          return;
        }
        fail(error);
      }
      if ((data as { twoFactorRedirect?: boolean } | null)?.twoFactorRedirect) {
        setCode("");
        setView("two-factor");
        return;
      }
      done();
    });
  };
  const sendLink = () =>
    run("link", async () => {
      const { error } = await client.signIn.magicLink({ email, callbackURL: redirectTo }, { headers: await captcha() });
      if (error) fail(error);
      setNotice(null);
      setView("check-email");
    });
  const sendCode = () =>
    run("code", async () => {
      const { error } = await client.emailOtp.sendVerificationOtp({ email, type: "sign-in" }, { headers: await captcha() });
      if (error) fail(error);
      setCode("");
      setView("code");
    });
  const verifyCode = (otp: string) =>
    run("verify", async () => {
      const { error } = await client.signIn.emailOtp({ email, otp });
      if (error) fail(error);
      done();
    });
  const verifyTotp = (otp: string) =>
    run("verify", async () => {
      const { error } = await client.twoFactor.verifyTotp({ code: otp, trustDevice: trust });
      if (error) fail(error);
      done();
    });
  const social = (provider: "google" | "github") =>
    run(provider, async () => {
      const { error } = await client.signIn.social({ provider, callbackURL: redirectTo });
      if (error) fail(error);
    });
  const passkey = () =>
    run("passkey", async () => {
      const r = await client.signIn.passkey();
      if (r?.error) fail(r.error);
      done();
    });
  const forgot = (e: FormEvent) => {
    e.preventDefault();
    void run("forgot", async () => {
      const reset = props.resetPasswordUrl ?? "/reset-password";
      const { error } = await client.requestPasswordReset({ email, redirectTo: reset }, { headers: await captcha() });
      if (error) fail(error);
      setView("forgot-sent");
    });
  };

  // Offer passkey autofill on browsers that support conditional UI.
  useEffect(() => {
    if (!has("passkey") || typeof window === "undefined" || !window.PublicKeyCredential?.isConditionalMediationAvailable) return;
    let live = true;
    void window.PublicKeyCredential.isConditionalMediationAvailable().then(async (ok) => {
      if (!ok || !live) return;
      const r = await client.signIn.passkey({ autoFill: true }).catch(() => null);
      if (live && r && !r.error) done();
    });
    return () => {
      live = false;
    };
  }, [config]);

  const app = config?.appName ?? "";
  const back = (
    <button
      type="button"
      className="tf-link"
      onClick={() => {
        setView("start");
        setError(null);
        setNotice(null);
      }}
    >
      <IconArrowLeft width={13} height={13} style={{ verticalAlign: "-2px", marginRight: 4 }} />
      Use a different way
    </button>
  );

  let body: ReactNode;
  if (!config) {
    body = configError ? <Alert>{configError}</Alert> : <Skeleton />;
  } else if (view === "check-email") {
    body = (
      <div className="tf-view">
        <div className="tf-badge-icon">
          <IconMail />
        </div>
        <Head
          title="Check your inbox"
          sub={
            notice === "verify" ? (
              <>
                Confirm your email first. We sent a fresh link to <b>{email}</b>.
              </>
            ) : (
              <>
                We sent a sign-in link to <b>{email}</b>. It works once, for the next 10 minutes.
              </>
            )
          }
        />
        <div className="tf-stack">
          {error ? <Alert>{error}</Alert> : null}
          {notice === "resent" ? <Alert tone="ok">Sent again. Give it a minute and check your spam folder too.</Alert> : null}
          <div className="tf-alt">
            {notice !== "verify" && has("magic-link") ? (
              <button type="button" className="tf-link" disabled={!!busy} onClick={() => void sendLink().then(() => setNotice("resent"))}>
                {busy === "link" ? "Sending…" : "Send it again"}
              </button>
            ) : null}
            {back}
          </div>
        </div>
      </div>
    );
  } else if (view === "code" || view === "two-factor") {
    const totp = view === "two-factor";
    body = (
      <form
        className="tf-view"
        onSubmit={(e) => {
          e.preventDefault();
          if (code.length === 6) void (totp ? verifyTotp(code) : verifyCode(code));
        }}
      >
        <div className="tf-badge-icon">{totp ? <IconShield /> : <IconMail />}</div>
        <Head
          title={totp ? "Two-step check" : "Enter your code"}
          sub={
            totp ? (
              "Open your authenticator app and enter the 6-digit code for this account."
            ) : (
              <>
                We sent a 6-digit code to <b>{email}</b>. It expires in 5 minutes.
              </>
            )
          }
        />
        <div className="tf-stack">
          <OtpInput label={totp ? "Authenticator code" : "Code from your email"} value={code} onChange={setCode} onComplete={(v) => void (totp ? verifyTotp(v) : verifyCode(v))} disabled={busy === "verify"} />
          {error ? <Alert>{error}</Alert> : null}
          {totp ? (
            <label className="tf-hint" style={{ display: "flex", gap: 8, alignItems: "center" }}>
              <input type="checkbox" checked={trust} onChange={(e) => setTrust(e.target.checked)} /> Trust this device for 30 days
            </label>
          ) : null}
          <Button type="submit" busy={busy === "verify"} disabled={code.length !== 6}>
            Continue
          </Button>
          <div className="tf-alt">
            {!totp ? (
              <button type="button" className="tf-link" disabled={!!busy} onClick={() => void sendCode()}>
                {busy === "code" ? "Sending…" : "Send a new code"}
              </button>
            ) : null}
            {back}
          </div>
        </div>
      </form>
    );
  } else if (view === "forgot" || view === "forgot-sent") {
    body =
      view === "forgot-sent" ? (
        <div className="tf-view">
          <div className="tf-badge-icon">
            <IconMail />
          </div>
          <Head
            title="Check your inbox"
            sub={
              <>
                If <b>{email}</b> has an account, a reset link is on its way. It works for the next hour.
              </>
            }
          />
          <div className="tf-alt">{back}</div>
        </div>
      ) : (
        <form className="tf-view" onSubmit={forgot}>
          <Head title="Reset your password" sub="Enter your email and we'll send you a link to choose a new one." />
          <div className="tf-stack">
            <Field label="Email" type="email" name="email" autoComplete="email" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" />
            {error ? <Alert>{error}</Alert> : null}
            <Button type="submit" busy={busy === "forgot"}>
              Send reset link
            </Button>
            <div className="tf-alt">{back}</div>
          </div>
        </form>
      );
  } else {
    body = (
      <div className="tf-view">
        <Head logo={props.logo} title={props.title ?? `Sign in to ${app}`} sub="Welcome back. Pick up where you left off." />
        <div className="tf-stack">
          <SocialButtons config={config} busy={busy} onClick={social} />
          {has("passkey") ? (
            <Button variant="quiet" type="button" icon={<IconPasskey />} busy={busy === "passkey"} onClick={() => void passkey()}>
              Sign in with a passkey
            </Button>
          ) : null}
          {config.social.google || config.social.github || has("passkey") ? <Or /> : null}
          <EmailForm
            config={config}
            email={email}
            setEmail={setEmail}
            password={password}
            setPassword={setPassword}
            busy={busy}
            error={error}
            onPassword={withPassword}
            onLink={() => void sendLink()}
            onCode={() => void sendCode()}
            onForgot={() => {
              setError(null);
              setView("forgot");
            }}
          />
        </div>
      </div>
    );
  }

  return (
    <Root className={props.className} theme={props.theme}>
      <Card label="Sign in">{body}</Card>
      {props.signUpUrl || props.onSignUp ? (
        <p className="tf-foot">
          New here?{" "}
          {props.onSignUp ? (
            <button type="button" className="tf-link" onClick={props.onSignUp}>
              Create an account
            </button>
          ) : (
            <a className="tf-link" href={props.signUpUrl}>
              Create an account
            </a>
          )}
        </p>
      ) : null}
    </Root>
  );
}

export function SocialButtons({ config, busy, onClick }: { config: AuthConfig; busy: string | null; onClick: (p: "google" | "github") => void }) {
  const list = (["google", "github"] as const).filter((p) => config.social[p]);
  if (!list.length) return null;
  return (
    <div className="tf-social" data-count={list.length}>
      {list.map((p) => (
        <Button key={p} variant="quiet" type="button" busy={busy === p} icon={p === "google" ? <IconGoogle /> : <IconGitHub />} onClick={() => onClick(p)}>
          {list.length === 2 ? (p === "google" ? "Google" : "GitHub") : `Continue with ${p === "google" ? "Google" : "GitHub"}`}
        </Button>
      ))}
    </div>
  );
}

function EmailForm(p: {
  config: AuthConfig;
  email: string;
  setEmail: (v: string) => void;
  password: string;
  setPassword: (v: string) => void;
  busy: string | null;
  error: string | null;
  onPassword: (e: FormEvent) => void;
  onLink: () => void;
  onCode: () => void;
  onForgot: () => void;
}) {
  const pw = p.config.methods.includes("email");
  const link = p.config.methods.includes("magic-link");
  const otp = p.config.methods.includes("otp");
  const emailRef = useRef<HTMLInputElement>(null);
  // Without passwords, the main button sends a link (or a code).
  const primary = pw ? "password" : link ? "link" : otp ? "code" : null;
  if (!primary) return null;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (primary === "password") return p.onPassword(e);
    if (primary === "link") return p.onLink();
    return p.onCode();
  };
  const alt = (kind: "link" | "code") => () => {
    if (!emailRef.current?.reportValidity()) return;
    if (kind === "link") p.onLink();
    else p.onCode();
  };
  return (
    <form className="tf-stack" onSubmit={submit} noValidate={false}>
      <Field ref={emailRef} label="Email" type="email" name="email" autoComplete="username webauthn" required value={p.email} onChange={(e) => p.setEmail(e.target.value)} placeholder="you@example.com" />
      {pw ? (
        <PasswordField
          label="Password"
          name="password"
          autoComplete="current-password"
          required
          value={p.password}
          onChange={(e) => p.setPassword(e.target.value)}
          aside={
            <button type="button" className="tf-link" onClick={p.onForgot}>
              Forgot password?
            </button>
          }
        />
      ) : null}
      {p.error ? <Alert>{p.error}</Alert> : null}
      <Button type="submit" busy={p.busy === primary}>
        {primary === "password" ? "Sign in" : primary === "link" ? "Email me a sign-in link" : "Email me a code"}
      </Button>
      {(primary === "password" && (link || otp)) || (primary === "link" && otp) ? (
        <div className="tf-alt">
          {primary === "password" && link ? (
            <button type="button" className="tf-link" disabled={!!p.busy} onClick={alt("link")}>
              {p.busy === "link" ? "Sending…" : "Email me a link instead"}
            </button>
          ) : null}
          {otp ? (
            <button type="button" className="tf-link" disabled={!!p.busy} onClick={alt("code")}>
              {p.busy === "code" ? "Sending…" : "Use a one-time code"}
            </button>
          ) : null}
        </div>
      ) : null}
    </form>
  );
}

export function Skeleton() {
  return (
    <div aria-busy="true" aria-label="Loading" style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <div style={{ height: 30, width: "70%", borderRadius: 6, background: "var(--tf-sunk)" }} />
      <div style={{ height: 16, width: "85%", borderRadius: 6, background: "var(--tf-sunk)" }} />
      <div style={{ height: 44, borderRadius: 10, background: "var(--tf-sunk)", marginTop: 10 }} />
      <div style={{ height: 44, borderRadius: 10, background: "var(--tf-sunk)" }} />
    </div>
  );
}
