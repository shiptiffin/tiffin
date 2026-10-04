import { type ReactNode } from "react";
import { type Common } from "./ui.js";
export type InviteProps = Common & {
    /** Organization to invite to. Default: the active one. */
    organizationId?: string;
    /** Hide the shareable-link section. */
    hideLinks?: boolean;
};
/**
 * Invite people to an organization by email or with a shareable link, and
 * see pending invitations. Roles above the inviter's own are not offered.
 */
export declare function Invite(props: InviteProps): import("react").JSX.Element | null;
export type AcceptInviteProps = Common & {
    /** Email invitation id (default: ?invitation= in the URL). */
    invitationId?: string;
    /** Invite link token (default: ?link= in the URL). */
    token?: string;
    /** Where to go after joining. Default "/". */
    redirectTo?: string;
    onAccepted?: (organizationId: string) => void;
    logo?: ReactNode;
};
/** The page invitation emails and invite links point at: shows the org, signs in if needed, joins. */
export declare function AcceptInvite(props: AcceptInviteProps): import("react").JSX.Element | null;
