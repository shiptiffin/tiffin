"use client";
import { jsx as _jsx, Fragment as _Fragment, jsxs as _jsxs } from "react/jsx-runtime";
import { useState } from "react";
import { errorText, useCaptcha, useTiffinAuth } from "./client.js";
import { IconArrowLeft, IconMail } from "./icons.js";
import { Skeleton, SocialButtons } from "./sign-in.js";
import { Alert, Button, Card, Field, Head, Or, PasswordField, Root } from "./ui.js";
/**
 * Create an account. With passwords on, people choose one and confirm their
 * email; with only email links on, they get a link that signs them up.
 */
export function SignUp(props) {
    const { client, baseURL, config, configError } = useTiffinAuth();
    const captcha = useCaptcha(baseURL, config?.captcha ?? false);
    const [name, setName] = useState("");
    const [email, setEmail] = useState("");
    const [password, setPassword] = useState("");
    const [busy, setBusy] = useState(null);
    const [error, setError] = useState(null);
    const [sent, setSent] = useState(null);
    const redirectTo = props.redirectTo ?? "/";
    const pw = !!config?.methods.includes("email");
    const link = !!config?.methods.includes("magic-link");
    const run = async (key, f) => {
        setBusy(key);
        setError(null);
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
    const submit = (e) => {
        e.preventDefault();
        void run("submit", async () => {
            if (pw) {
                const { error } = await client.signUp.email({ name: name.trim() || email.split("@")[0], email, password, callbackURL: redirectTo }, { headers: await captcha() });
                if (error)
                    throw error;
                setSent("verify");
            }
            else {
                const { error } = await client.signIn.magicLink({ email, name: name.trim() || undefined, callbackURL: redirectTo }, { headers: await captcha() });
                if (error)
                    throw error;
                setSent("link");
            }
        });
    };
    const social = (provider) => run(provider, async () => {
        const { error } = await client.signIn.social({ provider, callbackURL: redirectTo });
        if (error)
            throw error;
    });
    const app = config?.appName ?? "";
    let body;
    if (!config)
        body = configError ? _jsx(Alert, { children: configError }) : _jsx(Skeleton, {});
    else if (sent) {
        body = (_jsxs("div", { className: "tf-view", children: [_jsx("div", { className: "tf-badge-icon", children: _jsx(IconMail, {}) }), _jsx(Head, { title: "Check your inbox", sub: sent === "verify" ? (_jsxs(_Fragment, { children: ["We sent a link to ", _jsx("b", { children: email }), ". Open it to confirm your email and you're in."] })) : (_jsxs(_Fragment, { children: ["We sent a sign-in link to ", _jsx("b", { children: email }), ". It works once, for the next 10 minutes."] })) }), _jsx("div", { className: "tf-alt", children: _jsxs("button", { type: "button", className: "tf-link", onClick: () => setSent(null), children: [_jsx(IconArrowLeft, { width: 13, height: 13, style: { verticalAlign: "-2px", marginRight: 4 } }), "Use a different email"] }) })] }));
    }
    else {
        body = (_jsxs("form", { className: "tf-view", onSubmit: submit, children: [_jsx(Head, { logo: props.logo, title: props.title ?? `Create your ${app} account`, sub: "It takes a minute. No card, no spam." }), _jsxs("div", { className: "tf-stack", children: [_jsx(SocialButtons, { config: config, busy: busy, onClick: (p) => void social(p) }), config.social.google || config.social.github ? _jsx(Or, {}) : null, _jsx(Field, { label: "Name", name: "name", autoComplete: "name", value: name, onChange: (e) => setName(e.target.value), placeholder: "Ada Lovelace" }), _jsx(Field, { label: "Email", type: "email", name: "email", autoComplete: "email", required: true, value: email, onChange: (e) => setEmail(e.target.value), placeholder: "you@example.com" }), pw ? (_jsx(PasswordField, { label: "Password", name: "password", autoComplete: "new-password", required: true, minLength: 8, value: password, onChange: (e) => setPassword(e.target.value), hint: password.length > 0 && password.length < 8 ? `${8 - password.length} more character${8 - password.length === 1 ? "" : "s"}` : "At least 8 characters. A short sentence works well." })) : null, error ? _jsx(Alert, { children: error }) : null, _jsx(Button, { type: "submit", busy: busy === "submit", disabled: pw && password.length > 0 && password.length < 8, children: pw ? "Create account" : link ? "Email me a link" : "Continue" })] })] }));
    }
    return (_jsxs(Root, { className: props.className, theme: props.theme, children: [_jsx(Card, { label: "Create an account", children: body }), props.signInUrl || props.onSignIn ? (_jsxs("p", { className: "tf-foot", children: ["Have an account?", " ", props.onSignIn ? (_jsx("button", { type: "button", className: "tf-link", onClick: props.onSignIn, children: "Sign in" })) : (_jsx("a", { className: "tf-link", href: props.signInUrl, children: "Sign in" }))] })) : null] }));
}
/** The page the password reset email links to: reads ?token= and sets a new password. */
export function ResetPassword(props) {
    const { client } = useTiffinAuth();
    const token = props.token ?? (typeof window !== "undefined" ? (new URLSearchParams(window.location.search).get("token") ?? "") : "");
    const linkError = typeof window !== "undefined" ? new URLSearchParams(window.location.search).get("error") : null;
    const [password, setPassword] = useState("");
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState(linkError ? "This reset link has expired or was already used. Ask for a new one." : null);
    const [done, setDone] = useState(false);
    const submit = async (e) => {
        e.preventDefault();
        setBusy(true);
        setError(null);
        const { error } = await client.resetPassword({ newPassword: password, token });
        setBusy(false);
        if (error)
            setError(errorText(error));
        else {
            setDone(true);
            props.onDone?.();
        }
    };
    return (_jsx(Root, { className: props.className, theme: props.theme, children: _jsx(Card, { label: "Reset password", children: done ? (_jsxs("div", { className: "tf-view", children: [_jsx(Head, { title: "Password changed", sub: "You've been signed out everywhere else. Sign in with your new password." }), props.signInUrl ? (_jsx("a", { className: "tf-btn tf-btn-primary", href: props.signInUrl, children: _jsx("span", { children: "Sign in" }) })) : null] })) : (_jsxs("form", { className: "tf-view", onSubmit: submit, children: [_jsx(Head, { title: "Choose a new password", sub: "At least 8 characters. You'll be signed out on your other devices." }), _jsxs("div", { className: "tf-stack", children: [_jsx(PasswordField, { label: "New password", autoComplete: "new-password", required: true, minLength: 8, autoFocus: true, value: password, onChange: (e) => setPassword(e.target.value) }), error ? _jsx(Alert, { children: error }) : null, _jsx(Button, { type: "submit", busy: busy, disabled: !token || password.length < 8, children: "Save password" })] })] })) }) }));
}
