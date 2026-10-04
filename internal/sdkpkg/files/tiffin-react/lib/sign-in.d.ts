import { type ReactNode } from "react";
import { type AuthConfig } from "./client.js";
import { type Common } from "./ui.js";
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
export declare function SignIn(props: SignInProps): import("react").JSX.Element;
export declare function SocialButtons({ config, busy, onClick }: {
    config: AuthConfig;
    busy: string | null;
    onClick: (p: "google" | "github") => void;
}): import("react").JSX.Element | null;
export declare function Skeleton(): import("react").JSX.Element;
