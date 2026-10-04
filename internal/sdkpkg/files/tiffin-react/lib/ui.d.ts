import { type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode } from "react";
export type Theme = "light" | "dark" | "system";
export type Common = {
    className?: string | undefined;
    /** Force light or dark; default follows the system (or a .dark class on <html>). */
    theme?: Theme | undefined;
};
/** Every component renders inside one of these: styles, theme, font. */
export declare function Root({ className, theme, children, as }: Common & {
    children: ReactNode;
    as?: "div" | "span";
}): import("react").JSX.Element;
export declare function Card({ children, label }: {
    children: ReactNode;
    label?: string;
}): import("react").JSX.Element;
export declare function Head({ title, sub, logo }: {
    title: ReactNode;
    sub?: ReactNode;
    logo?: ReactNode;
}): import("react").JSX.Element;
type FieldProps = InputHTMLAttributes<HTMLInputElement> & {
    label: string;
    hint?: ReactNode;
    error?: string | null;
    aside?: ReactNode;
};
export declare const Field: import("react").ForwardRefExoticComponent<InputHTMLAttributes<HTMLInputElement> & {
    label: string;
    hint?: ReactNode;
    error?: string | null;
    aside?: ReactNode;
} & import("react").RefAttributes<HTMLInputElement>>;
export declare function PasswordField(props: Omit<FieldProps, "type"> & {
    autoComplete: "current-password" | "new-password";
}): import("react").JSX.Element;
export declare const Button: import("react").ForwardRefExoticComponent<ButtonHTMLAttributes<HTMLButtonElement> & {
    variant?: "primary" | "quiet" | "danger";
    busy?: boolean;
    small?: boolean;
    icon?: ReactNode;
} & import("react").RefAttributes<HTMLButtonElement>>;
export declare function Alert({ children, tone }: {
    children: ReactNode;
    tone?: "error" | "ok" | "info";
}): import("react").JSX.Element;
export declare const Or: () => import("react").JSX.Element;
export declare function initials(name?: string | null, email?: string | null): string;
export declare function Avatar({ name, email, image, size, square }: {
    name?: string | null | undefined;
    email?: string | null | undefined;
    image?: string | null | undefined;
    size?: number | undefined;
    square?: boolean | undefined;
}): import("react").JSX.Element;
/** Six boxes for a one-time code; paste fills them all; calls onComplete at six digits. */
export declare function OtpInput({ value, onChange, onComplete, label, disabled }: {
    value: string;
    onChange: (v: string) => void;
    onComplete?: (v: string) => void;
    label: string;
    disabled?: boolean;
}): import("react").JSX.Element;
/**
 * A popover menu: click or Enter/Space opens it, arrows move between items,
 * Escape or a click outside closes it and returns focus to the trigger.
 */
export declare function useMenu(): {
    open: boolean;
    setOpen: import("react").Dispatch<import("react").SetStateAction<boolean>>;
    close: (refocus?: boolean) => void;
    anchor: import("react").RefObject<HTMLDivElement | null>;
    menu: import("react").RefObject<HTMLDivElement | null>;
    onKeyDown: (e: React.KeyboardEvent) => void;
    triggerProps: {
        ref: import("react").RefObject<HTMLButtonElement | null>;
        "aria-haspopup": "menu";
        "aria-expanded": boolean;
        onClick: () => void;
        onKeyDown: (e: React.KeyboardEvent) => void;
    };
};
/** Role chip: viewer, member, admin, owner. */
export declare const RoleChip: ({ role }: {
    role: string;
}) => import("react").JSX.Element;
export {};
