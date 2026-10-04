"use client";
import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { forwardRef, useEffect, useId, useLayoutEffect, useRef, useState, } from "react";
import { IconAlert, IconCheck } from "./icons.js";
import { injectStyles } from "./styles.js";
const useIsoLayout = typeof window === "undefined" ? useEffect : useLayoutEffect;
/** Every component renders inside one of these: styles, theme, font. */
export function Root({ className, theme, children, as = "div" }) {
    useIsoLayout(() => injectStyles(), []);
    const El = as;
    return (_jsx(El, { className: "tf-root" + (className ? " " + className : ""), "data-tf-theme": theme && theme !== "system" ? theme : undefined, children: children }));
}
export function Card({ children, label }) {
    return (_jsx("section", { className: "tf-card", "aria-label": label, children: children }));
}
export function Head({ title, sub, logo }) {
    return (_jsxs("header", { className: "tf-head", children: [logo ? _jsx("div", { className: "tf-logo", children: logo }) : null, _jsx("h2", { className: "tf-title", children: title }), sub ? _jsx("p", { className: "tf-sub", children: sub }) : null] }));
}
export const Field = forwardRef(function Field({ label, hint, error, aside, id, ...rest }, ref) {
    const auto = useId();
    const fid = id ?? auto;
    const hintId = hint ? fid + "-hint" : undefined;
    return (_jsxs("div", { className: "tf-field", children: [_jsxs("div", { className: "tf-row", children: [_jsx("label", { className: "tf-label", htmlFor: fid, children: label }), aside] }), _jsx("input", { ref: ref, id: fid, className: "tf-input", "aria-invalid": error ? true : undefined, "aria-describedby": hintId, ...rest }), hint ? (_jsx("span", { className: "tf-hint", id: hintId, children: hint })) : null] }));
});
export function PasswordField(props) {
    const [show, setShow] = useState(false);
    const { label, hint, aside, id, ...rest } = props;
    const auto = useId();
    const fid = id ?? auto;
    const hintId = hint ? fid + "-hint" : undefined;
    return (_jsxs("div", { className: "tf-field", children: [_jsxs("div", { className: "tf-row", children: [_jsx("label", { className: "tf-label", htmlFor: fid, children: label }), aside] }), _jsxs("div", { className: "tf-pw", children: [_jsx("input", { id: fid, className: "tf-input", type: show ? "text" : "password", "aria-describedby": hintId, ...rest }), _jsx("button", { type: "button", className: "tf-pw-toggle", onClick: () => setShow((s) => !s), "aria-pressed": show, "aria-label": show ? "Hide password" : "Show password", children: show ? "Hide" : "Show" })] }), hint ? (_jsx("span", { className: "tf-hint", id: hintId, children: hint })) : null] }));
}
export const Button = forwardRef(function Button({ variant = "primary", busy, small, icon, children, className, disabled, ...rest }, ref) {
    return (_jsxs("button", { ref: ref, className: `tf-btn tf-btn-${variant}${small ? " tf-btn-sm" : ""}${className ? " " + className : ""}`, "aria-busy": busy || undefined, disabled: disabled || busy, ...rest, children: [busy ? _jsx("span", { className: "tf-spinner", "aria-hidden": true }) : icon, _jsx("span", { children: children })] }));
});
export function Alert({ children, tone = "error" }) {
    return (_jsxs("div", { className: "tf-alert", "data-tone": tone, role: tone === "error" ? "alert" : "status", children: [tone === "ok" ? _jsx(IconCheck, {}) : _jsx(IconAlert, {}), _jsx("div", { children: children })] }));
}
export const Or = () => (_jsx("div", { className: "tf-or", role: "separator", children: "or" }));
export function initials(name, email) {
    const n = (name ?? "").trim();
    if (n) {
        const parts = n.split(/\s+/).filter(Boolean);
        return ((parts[0]?.[0] ?? "") + (parts.length > 1 ? (parts.at(-1)?.[0] ?? "") : "")).toUpperCase();
    }
    return (email ?? "?").slice(0, 1).toUpperCase();
}
export function Avatar({ name, email, image, size = 32, square }) {
    const [broken, setBroken] = useState(false);
    return (_jsx("span", { className: "tf-avatar", "data-shape": square ? "square" : undefined, style: { ["--tf-size"]: `${size}px` }, "aria-hidden": true, children: image && !broken ? _jsx("img", { src: image, alt: "", onError: () => setBroken(true), referrerPolicy: "no-referrer" }) : initials(name, email) }));
}
/** Six boxes for a one-time code; paste fills them all; calls onComplete at six digits. */
export function OtpInput({ value, onChange, onComplete, label, disabled }) {
    const refs = useRef([]);
    useEffect(() => {
        refs.current[0]?.focus();
    }, []);
    const set = (next) => {
        const clean = next.replace(/\D/g, "").slice(0, 6);
        onChange(clean);
        if (clean.length === 6)
            onComplete?.(clean);
        refs.current[Math.min(clean.length, 5)]?.focus();
    };
    return (_jsx("div", { className: "tf-otp", role: "group", "aria-label": label, children: Array.from({ length: 6 }, (_, i) => (_jsx("input", { ref: (el) => {
                refs.current[i] = el;
            }, inputMode: "numeric", autoComplete: i === 0 ? "one-time-code" : "off", "aria-label": `Digit ${i + 1}`, maxLength: i === 0 ? 6 : 1, disabled: disabled, value: value[i] ?? "", "data-filled": value[i] ? "true" : undefined, onChange: (e) => {
                const v = e.target.value.replace(/\D/g, "");
                if (v.length > 1)
                    return set(v); // paste or autofill
                const arr = value.padEnd(6, " ").split("");
                arr[i] = v || " ";
                set(arr.join("").replace(/\s+$/, "").replace(/\s/g, ""));
            }, onKeyDown: (e) => {
                if (e.key === "Backspace" && !value[i] && i > 0) {
                    e.preventDefault();
                    set(value.slice(0, i - 1));
                }
                else if (e.key === "ArrowLeft" && i > 0)
                    refs.current[i - 1]?.focus();
                else if (e.key === "ArrowRight" && i < 5)
                    refs.current[i + 1]?.focus();
            }, onPaste: (e) => {
                const t = e.clipboardData.getData("text");
                if (/\d/.test(t)) {
                    e.preventDefault();
                    set(t);
                }
            } }, i))) }));
}
/**
 * A popover menu: click or Enter/Space opens it, arrows move between items,
 * Escape or a click outside closes it and returns focus to the trigger.
 */
export function useMenu() {
    const [open, setOpen] = useState(false);
    const anchor = useRef(null);
    const trigger = useRef(null);
    const menu = useRef(null);
    const items = () => Array.from(menu.current?.querySelectorAll('[role^="menuitem"]:not([disabled])') ?? []);
    useEffect(() => {
        if (!open)
            return;
        const onDown = (e) => {
            if (anchor.current && !anchor.current.contains(e.target))
                setOpen(false);
        };
        document.addEventListener("mousedown", onDown);
        requestAnimationFrame(() => items()[0]?.focus());
        return () => document.removeEventListener("mousedown", onDown);
    }, [open]);
    const close = (refocus = true) => {
        setOpen(false);
        if (refocus)
            trigger.current?.focus();
    };
    const onKeyDown = (e) => {
        if (e.key === "Escape") {
            e.preventDefault();
            close();
            return;
        }
        if (e.key === "Tab")
            return close(false);
        const list = items();
        const i = list.indexOf(document.activeElement);
        if (e.key === "ArrowDown") {
            e.preventDefault();
            list[(i + 1) % list.length]?.focus();
        }
        else if (e.key === "ArrowUp") {
            e.preventDefault();
            list[(i - 1 + list.length) % list.length]?.focus();
        }
        else if (e.key === "Home") {
            e.preventDefault();
            list[0]?.focus();
        }
        else if (e.key === "End") {
            e.preventDefault();
            list.at(-1)?.focus();
        }
    };
    const triggerProps = {
        ref: trigger,
        "aria-haspopup": "menu",
        "aria-expanded": open,
        onClick: () => setOpen((o) => !o),
        onKeyDown: (e) => {
            if (e.key === "ArrowDown" && !open) {
                e.preventDefault();
                setOpen(true);
            }
        },
    };
    return { open, setOpen, close, anchor, menu, onKeyDown, triggerProps };
}
/** Role chip: viewer, member, admin, owner. */
export const RoleChip = ({ role }) => (_jsx("span", { className: "tf-chip", "data-role": role, children: role }));
