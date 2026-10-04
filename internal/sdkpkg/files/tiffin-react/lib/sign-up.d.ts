import { type ReactNode } from "react";
import { type Common } from "./ui.js";
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
export declare function SignUp(props: SignUpProps): import("react").JSX.Element;
/** The page the password reset email links to: reads ?token= and sets a new password. */
export declare function ResetPassword(props: Common & {
    token?: string;
    signInUrl?: string;
    onDone?: () => void;
}): import("react").JSX.Element;
