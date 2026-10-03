"use client";
import {
  forwardRef,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
} from "react";
import { IconAlert, IconCheck } from "./icons";
import { injectStyles } from "./styles";

const useIsoLayout = typeof window === "undefined" ? useEffect : useLayoutEffect;

export type Theme = "light" | "dark" | "system";
export type Common = {
  className?: string | undefined;
  /** Force light or dark; default follows the system (or a .dark class on <html>). */
  theme?: Theme | undefined;
};

/** Every component renders inside one of these: styles, theme, font. */
export function Root({ className, theme, children, as = "div" }: Common & { children: ReactNode; as?: "div" | "span" }) {
  useIsoLayout(() => injectStyles(), []);
  const El = as;
  return (
    <El className={"tf-root" + (className ? " " + className : "")} data-tf-theme={theme && theme !== "system" ? theme : undefined}>
      {children}
    </El>
  );
}

export function Card({ children, label }: { children: ReactNode; label?: string }) {
  return (
    <section className="tf-card" aria-label={label}>
      {children}
    </section>
  );
}

export function Head({ title, sub, logo }: { title: ReactNode; sub?: ReactNode; logo?: ReactNode }) {
  return (
    <header className="tf-head">
      {logo ? <div className="tf-logo">{logo}</div> : null}
      <h2 className="tf-title">{title}</h2>
      {sub ? <p className="tf-sub">{sub}</p> : null}
    </header>
  );
}

type FieldProps = InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: ReactNode; error?: string | null; aside?: ReactNode };

export const Field = forwardRef<HTMLInputElement, FieldProps>(function Field({ label, hint, error, aside, id, ...rest }, ref) {
  const auto = useId();
  const fid = id ?? auto;
  const hintId = hint ? fid + "-hint" : undefined;
  return (
    <div className="tf-field">
      <div className="tf-row">
        <label className="tf-label" htmlFor={fid}>
          {label}
        </label>
        {aside}
      </div>
      <input ref={ref} id={fid} className="tf-input" aria-invalid={error ? true : undefined} aria-describedby={hintId} {...rest} />
      {hint ? (
        <span className="tf-hint" id={hintId}>
          {hint}
        </span>
      ) : null}
    </div>
  );
});

export function PasswordField(props: Omit<FieldProps, "type"> & { autoComplete: "current-password" | "new-password" }) {
  const [show, setShow] = useState(false);
  const { label, hint, aside, id, ...rest } = props;
  const auto = useId();
  const fid = id ?? auto;
  const hintId = hint ? fid + "-hint" : undefined;
  return (
    <div className="tf-field">
      <div className="tf-row">
        <label className="tf-label" htmlFor={fid}>
          {label}
        </label>
        {aside}
      </div>
      <div className="tf-pw">
        <input id={fid} className="tf-input" type={show ? "text" : "password"} aria-describedby={hintId} {...rest} />
        <button type="button" className="tf-pw-toggle" onClick={() => setShow((s) => !s)} aria-pressed={show} aria-label={show ? "Hide password" : "Show password"}>
          {show ? "Hide" : "Show"}
        </button>
      </div>
      {hint ? (
        <span className="tf-hint" id={hintId}>
          {hint}
        </span>
      ) : null}
    </div>
  );
}

type BtnProps = ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "primary" | "quiet" | "danger"; busy?: boolean; small?: boolean; icon?: ReactNode };

export const Button = forwardRef<HTMLButtonElement, BtnProps>(function Button({ variant = "primary", busy, small, icon, children, className, disabled, ...rest }, ref) {
  return (
    <button
      ref={ref}
      className={`tf-btn tf-btn-${variant}${small ? " tf-btn-sm" : ""}${className ? " " + className : ""}`}
      aria-busy={busy || undefined}
      disabled={disabled || busy}
      {...rest}
    >
      {busy ? <span className="tf-spinner" aria-hidden /> : icon}
      <span>{children}</span>
    </button>
  );
});

export function Alert({ children, tone = "error" }: { children: ReactNode; tone?: "error" | "ok" | "info" }) {
  return (
    <div className="tf-alert" data-tone={tone} role={tone === "error" ? "alert" : "status"}>
      {tone === "ok" ? <IconCheck /> : <IconAlert />}
      <div>{children}</div>
    </div>
  );
}

export const Or = () => (
  <div className="tf-or" role="separator">
    or
  </div>
);

export function initials(name?: string | null, email?: string | null): string {
  const n = (name ?? "").trim();
  if (n) {
    const parts = n.split(/\s+/).filter(Boolean);
    return ((parts[0]?.[0] ?? "") + (parts.length > 1 ? (parts.at(-1)?.[0] ?? "") : "")).toUpperCase();
  }
  return (email ?? "?").slice(0, 1).toUpperCase();
}

export function Avatar({ name, email, image, size = 32, square }: { name?: string | null | undefined; email?: string | null | undefined; image?: string | null | undefined; size?: number | undefined; square?: boolean | undefined }) {
  const [broken, setBroken] = useState(false);
  return (
    <span className="tf-avatar" data-shape={square ? "square" : undefined} style={{ ["--tf-size" as string]: `${size}px` }} aria-hidden>
      {image && !broken ? <img src={image} alt="" onError={() => setBroken(true)} referrerPolicy="no-referrer" /> : initials(name, email)}
    </span>
  );
}

/** Six boxes for a one-time code; paste fills them all; calls onComplete at six digits. */
export function OtpInput({ value, onChange, onComplete, label, disabled }: { value: string; onChange: (v: string) => void; onComplete?: (v: string) => void; label: string; disabled?: boolean }) {
  const refs = useRef<(HTMLInputElement | null)[]>([]);
  useEffect(() => {
    refs.current[0]?.focus();
  }, []);
  const set = (next: string) => {
    const clean = next.replace(/\D/g, "").slice(0, 6);
    onChange(clean);
    if (clean.length === 6) onComplete?.(clean);
    refs.current[Math.min(clean.length, 5)]?.focus();
  };
  return (
    <div className="tf-otp" role="group" aria-label={label}>
      {Array.from({ length: 6 }, (_, i) => (
        <input
          key={i}
          ref={(el) => {
            refs.current[i] = el;
          }}
          inputMode="numeric"
          autoComplete={i === 0 ? "one-time-code" : "off"}
          aria-label={`Digit ${i + 1}`}
          maxLength={i === 0 ? 6 : 1}
          disabled={disabled}
          value={value[i] ?? ""}
          data-filled={value[i] ? "true" : undefined}
          onChange={(e) => {
            const v = e.target.value.replace(/\D/g, "");
            if (v.length > 1) return set(v); // paste or autofill
            const arr = value.padEnd(6, " ").split("");
            arr[i] = v || " ";
            set(arr.join("").replace(/\s+$/, "").replace(/\s/g, ""));
          }}
          onKeyDown={(e) => {
            if (e.key === "Backspace" && !value[i] && i > 0) {
              e.preventDefault();
              set(value.slice(0, i - 1));
            } else if (e.key === "ArrowLeft" && i > 0) refs.current[i - 1]?.focus();
            else if (e.key === "ArrowRight" && i < 5) refs.current[i + 1]?.focus();
          }}
          onPaste={(e) => {
            const t = e.clipboardData.getData("text");
            if (/\d/.test(t)) {
              e.preventDefault();
              set(t);
            }
          }}
        />
      ))}
    </div>
  );
}

/**
 * A popover menu: click or Enter/Space opens it, arrows move between items,
 * Escape or a click outside closes it and returns focus to the trigger.
 */
export function useMenu() {
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLDivElement | null>(null);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const menu = useRef<HTMLDivElement | null>(null);
  const items = () => Array.from(menu.current?.querySelectorAll<HTMLElement>('[role^="menuitem"]:not([disabled])') ?? []);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (anchor.current && !anchor.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    requestAnimationFrame(() => items()[0]?.focus());
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);
  const close = (refocus = true) => {
    setOpen(false);
    if (refocus) trigger.current?.focus();
  };
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      close();
      return;
    }
    if (e.key === "Tab") return close(false);
    const list = items();
    const i = list.indexOf(document.activeElement as HTMLElement);
    if (e.key === "ArrowDown") {
      e.preventDefault();
      list[(i + 1) % list.length]?.focus();
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      list[(i - 1 + list.length) % list.length]?.focus();
    } else if (e.key === "Home") {
      e.preventDefault();
      list[0]?.focus();
    } else if (e.key === "End") {
      e.preventDefault();
      list.at(-1)?.focus();
    }
  };
  const triggerProps = {
    ref: trigger,
    "aria-haspopup": "menu" as const,
    "aria-expanded": open,
    onClick: () => setOpen((o) => !o),
    onKeyDown: (e: React.KeyboardEvent) => {
      if (e.key === "ArrowDown" && !open) {
        e.preventDefault();
        setOpen(true);
      }
    },
  };
  return { open, setOpen, close, anchor, menu, onKeyDown, triggerProps };
}

/** Role chip: viewer, member, admin, owner. */
export const RoleChip = ({ role }: { role: string }) => (
  <span className="tf-chip" data-role={role}>
    {role}
  </span>
);
