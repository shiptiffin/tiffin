import { type ReactNode } from "react";
import { RoleChip, type Common } from "./ui.js";
export type UserButtonProps = Common & {
    /** Where to go after signing out. Default "/". */
    afterSignOut?: string;
    /** Extra menu items, rendered above "Sign out". */
    children?: ReactNode;
    /** Shown while signed out (e.g. a sign-in link). Default: nothing. */
    signedOut?: ReactNode;
};
/** The signed-in person's avatar with a menu: who they are, add a passkey, sign out. */
export declare function UserButton(props: UserButtonProps): import("react").JSX.Element | null;
export type OrgSwitcherProps = Common & {
    /** Called after switching (default: reload the page so server data follows). */
    onSwitch?: (organizationId: string) => void;
    /** Hide "Create organization". */
    hideCreate?: boolean;
};
/** Shows the active organization and switches between the person's organizations. */
export declare function OrgSwitcher(props: OrgSwitcherProps): import("react").JSX.Element | null;
export { RoleChip };
