"use client";
import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useEffect, useRef, useState } from "react";
import { errorText, useCaptcha, useTiffinAuth } from "./client.js";
import { IconArrowLeft, IconGitHub, IconGoogle, IconMail, IconPasskey, IconShield } from "./icons.js";
import { Alert, Button, Card, Field, Head, OtpInput, Or, PasswordField, Root } from "./ui.js";
function go(url) {
    if (typeof window !== "undefined")
        window.location.assign(url);
}
export function SignIn(props) {
    const { client, baseURL, config, configError } = useTiffinAuth();
    const captcha = useCaptcha(baseURL, config?.captcha ?? false);
    const [view, setView] = useState("start");
    const [email, setEmail] = useState("");
    const [password, setPassword] = useState("");
    const [code, setCode] = useState("");
    const [busy, setBusy] = useState(null);
    const [error, setError] = useState(null);
    const [notice, setNotice] = useState(null);
    const [trust, setTrust] = useState(true);
    const redirectTo = props.redirectTo ?? "/";
    const done = () => (props.onSignedIn ? props.onSignedIn() : go(redirectTo));
    const has = (m) => !!config?.methods.includes(m);
    const run = async (key, f) => {
        setBusy(key);
        setError(null);
        setNotice(null);
        try {
            await f();
        }
        catch (e) {
            setError(errorText(e));
        }
        finally {
            setBusy(null);
        }
    };
    const fail = (e) => {
        throw e;
    };
    const withPassword = (e) => {
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
            if (data?.twoFactorRedirect) {
                setCode("");
                setView("two-factor");
                return;
            }
            done();
        });
    };
    const sendLink = () => run("link", async () => {
        const { error } = await client.signIn.magicLink({ email, callbackURL: redirectTo }, { headers: await captcha() });
        if (error)
            fail(error);
        setNotice(null);
        setView("check-email");
    });
    const sendCode = () => run("code", async () => {
        const { error } = await client.emailOtp.sendVerificationOtp({ email, type: "sign-in" }, { headers: await captcha() });
        if (error)
            fail(error);
        setCode("");
        setView("code");
    });
    const verifyCode = (otp) => run("verify", async () => {
        const { error } = await client.signIn.emailOtp({ email, otp });
        if (error)
            fail(error);
        done();
    });
    const verifyTotp = (otp) => run("verify", async () => {
        const { error } = await client.twoFactor.verifyTotp({ code: otp, trustDevice: trust });
        if (error)
            fail(error);
        done();
    });
    const social = (provider) => run(provider, async () => {
        const { error } = await client.signIn.social({ provider, callbackURL: redirectTo });
        if (error)
            fail(error);
    });
    const passkey = () => run("passkey", async () => {
        const r = await client.signIn.passkey();
        if (r?.error)
            fail(r.error);
        done();
    });
    const forgot = (e) => {
        e.preventDefault();
        void run("forgot", async () => {
            const reset = props.resetPasswordUrl ?? "/reset-password";
            const { error } = await client.requestPasswordReset({ email, redirectTo: reset }, { headers: await captcha() });
            if (error)
                fail(error);
            setView("forgot-sent");
        });
    };
    // Offer passkey autofill on browsers that support conditional UI.
    useEffect(() => {
        if (!has("passkey") || typeof window === "undefined" || !window.PublicKeyCredential?.isConditionalMediationAvailable)
            return;
        let live = true;
        void window.PublicKeyCredential.isConditionalMediationAvailable().then(async (ok) => {
            if (!ok || !live)
                return;
            const r = await client.signIn.passkey({ autoFill: true }).catch(() => null);
            if (live && r && !r.error)
                done();
        });
        return () => {
            live = false;
        };
    }, [config]);
    const app = config?.appName ?? "";
    const back = (_jsxs("button", { type: "button", className: "tf-link", onClick: () => {
            setView("start");
            setError(null);
            setNotice(null);
        }, children: [_jsx(IconArrowLeft, { width: 13, height: 13, style: { verticalAlign: "-2px", marginRight: 4 } }), "Use a different way"] }));
    let body;
    if (!config) {
        body = configError ? _jsx(Alert, { children: configError }) : _jsx(Skeleton, {});
    }
    else if (view === "check-email") {
        body = (_jsxs("div", { className: "tf-view", children: [_jsx("div", { className: "tf-badge-icon", children: _jsx(IconMail, {}) }), _jsx(Head, { title: "Check your inbox", sub: notice === "verify" ? (_jsxs(_Fragment, { children: ["Confirm your email first. We sent a fresh link to ", _jsx("b", { children: email }), "."] })) : (_jsxs(_Fragment, { children: ["We sent a sign-in link to ", _jsx("b", { children: email }), ". It works once, for the next 10 minutes."] })) }), _jsxs("div", { className: "tf-stack", children: [error ? _jsx(Alert, { children: error }) : null, notice === "resent" ? _jsx(Alert, { tone: "ok", children: "Sent again. Give it a minute and check your spam folder too." }) : null, _jsxs("div", { className: "tf-alt", children: [notice !== "verify" && has("magic-link") ? (_jsx("button", { type: "button", className: "tf-link", disabled: !!busy, onClick: () => void sendLink().then(() => setNotice("resent")), children: busy === "link" ? "Sending…" : "Send it again" })) : null, back] })] })] }));
    }
    else if (view === "code" || view === "two-factor") {
        const totp = view === "two-factor";
        body = (_jsxs("form", { className: "tf-view", onSubmit: (e) => {
                e.preventDefault();
                if (code.length === 6)
                    void (totp ? verifyTotp(code) : verifyCode(code));
            }, children: [_jsx("div", { className: "tf-badge-icon", children: totp ? _jsx(IconShield, {}) : _jsx(IconMail, {}) }), _jsx(Head, { title: totp ? "Two-step check" : "Enter your code", sub: totp ? ("Open your authenticator app and enter the 6-digit code for this account.") : (_jsxs(_Fragment, { children: ["We sent a 6-digit code to ", _jsx("b", { children: email }), ". It expires in 5 minutes."] })) }), _jsxs("div", { className: "tf-stack", children: [_jsx(OtpInput, { label: totp ? "Authenticator code" : "Code from your email", value: code, onChange: setCode, onComplete: (v) => void (totp ? verifyTotp(v) : verifyCode(v)), disabled: busy === "verify" }), error ? _jsx(Alert, { children: error }) : null, totp ? (_jsxs("label", { className: "tf-hint", style: { display: "flex", gap: 8, alignItems: "center" }, children: [_jsx("input", { type: "checkbox", checked: trust, onChange: (e) => setTrust(e.target.checked) }), " Trust this device for 30 days"] })) : null, _jsx(Button, { type: "submit", busy: busy === "verify", disabled: code.length !== 6, children: "Continue" }), _jsxs("div", { className: "tf-alt", children: [!totp ? (_jsx("button", { type: "button", className: "tf-link", disabled: !!busy, onClick: () => void sendCode(), children: busy === "code" ? "Sending…" : "Send a new code" })) : null, back] })] })] }));
    }
    else if (view === "forgot" || view === "forgot-sent") {
        body =
            view === "forgot-sent" ? (_jsxs("div", { className: "tf-view", children: [_jsx("div", { className: "tf-badge-icon", children: _jsx(IconMail, {}) }), _jsx(Head, { title: "Check your inbox", sub: _jsxs(_Fragment, { children: ["If ", _jsx("b", { children: email }), " has an account, a reset link is on its way. It works for the next hour."] }) }), _jsx("div", { className: "tf-alt", children: back })] })) : (_jsxs("form", { className: "tf-view", onSubmit: forgot, children: [_jsx(Head, { title: "Reset your password", sub: "Enter your email and we'll send you a link to choose a new one." }), _jsxs("div", { className: "tf-stack", children: [_jsx(Field, { label: "Email", type: "email", name: "email", autoComplete: "email", required: true, autoFocus: true, value: email, onChange: (e) => setEmail(e.target.value), placeholder: "you@example.com" }), error ? _jsx(Alert, { children: error }) : null, _jsx(Button, { type: "submit", busy: busy === "forgot", children: "Send reset link" }), _jsx("div", { className: "tf-alt", children: back })] })] }));
    }
    else {
        body = (_jsxs("div", { className: "tf-view", children: [_jsx(Head, { logo: props.logo, title: props.title ?? `Sign in to ${app}`, sub: "Welcome back. Pick up where you left off." }), _jsxs("div", { className: "tf-stack", children: [_jsx(SocialButtons, { config: config, busy: busy, onClick: social }), has("passkey") ? (_jsx(Button, { variant: "quiet", type: "button", icon: _jsx(IconPasskey, {}), busy: busy === "passkey", onClick: () => void passkey(), children: "Sign in with a passkey" })) : null, config.social.google || config.social.github || has("passkey") ? _jsx(Or, {}) : null, _jsx(EmailForm, { config: config, email: email, setEmail: setEmail, password: password, setPassword: setPassword, busy: busy, error: error, onPassword: withPassword, onLink: () => void sendLink(), onCode: () => void sendCode(), onForgot: () => {
                                setError(null);
                                setView("forgot");
                            } })] })] }));
    }
    return (_jsxs(Root, { className: props.className, theme: props.theme, children: [_jsx(Card, { label: "Sign in", children: body }), props.signUpUrl || props.onSignUp ? (_jsxs("p", { className: "tf-foot", children: ["New here?", " ", props.onSignUp ? (_jsx("button", { type: "button", className: "tf-link", onClick: props.onSignUp, children: "Create an account" })) : (_jsx("a", { className: "tf-link", href: props.signUpUrl, children: "Create an account" }))] })) : null] }));
}
export function SocialButtons({ config, busy, onClick }) {
    const list = ["google", "github"].filter((p) => config.social[p]);
    if (!list.length)
        return null;
    return (_jsx("div", { className: "tf-social", "data-count": list.length, children: list.map((p) => (_jsx(Button, { variant: "quiet", type: "button", busy: busy === p, icon: p === "google" ? _jsx(IconGoogle, {}) : _jsx(IconGitHub, {}), onClick: () => onClick(p), children: list.length === 2 ? (p === "google" ? "Google" : "GitHub") : `Continue with ${p === "google" ? "Google" : "GitHub"}` }, p))) }));
}
function EmailForm(p) {
    const pw = p.config.methods.includes("email");
    const link = p.config.methods.includes("magic-link");
    const otp = p.config.methods.includes("otp");
    const emailRef = useRef(null);
    // Without passwords, the main button sends a link (or a code).
    const primary = pw ? "password" : link ? "link" : otp ? "code" : null;
    if (!primary)
        return null;
    const submit = (e) => {
        e.preventDefault();
        if (primary === "password")
            return p.onPassword(e);
        if (primary === "link")
            return p.onLink();
        return p.onCode();
    };
    const alt = (kind) => () => {
        if (!emailRef.current?.reportValidity())
            return;
        if (kind === "link")
            p.onLink();
        else
            p.onCode();
    };
    return (_jsxs("form", { className: "tf-stack", onSubmit: submit, noValidate: false, children: [_jsx(Field, { ref: emailRef, label: "Email", type: "email", name: "email", autoComplete: "username webauthn", required: true, value: p.email, onChange: (e) => p.setEmail(e.target.value), placeholder: "you@example.com" }), pw ? (_jsx(PasswordField, { label: "Password", name: "password", autoComplete: "current-password", required: true, value: p.password, onChange: (e) => p.setPassword(e.target.value), aside: _jsx("button", { type: "button", className: "tf-link", onClick: p.onForgot, children: "Forgot password?" }) })) : null, p.error ? _jsx(Alert, { children: p.error }) : null, _jsx(Button, { type: "submit", busy: p.busy === primary, children: primary === "password" ? "Sign in" : primary === "link" ? "Email me a sign-in link" : "Email me a code" }), (primary === "password" && (link || otp)) || (primary === "link" && otp) ? (_jsxs("div", { className: "tf-alt", children: [primary === "password" && link ? (_jsx("button", { type: "button", className: "tf-link", disabled: !!p.busy, onClick: alt("link"), children: p.busy === "link" ? "Sending…" : "Email me a link instead" })) : null, otp ? (_jsx("button", { type: "button", className: "tf-link", disabled: !!p.busy, onClick: alt("code"), children: p.busy === "code" ? "Sending…" : "Use a one-time code" })) : null] })) : null] }));
}
export function Skeleton() {
    return (_jsxs("div", { "aria-busy": "true", "aria-label": "Loading", style: { display: "flex", flexDirection: "column", gap: 14 }, children: [_jsx("div", { style: { height: 30, width: "70%", borderRadius: 6, background: "var(--tf-sunk)" } }), _jsx("div", { style: { height: 16, width: "85%", borderRadius: 6, background: "var(--tf-sunk)" } }), _jsx("div", { style: { height: 44, borderRadius: 10, background: "var(--tf-sunk)", marginTop: 10 } }), _jsx("div", { style: { height: 44, borderRadius: 10, background: "var(--tf-sunk)" } })] }));
}
