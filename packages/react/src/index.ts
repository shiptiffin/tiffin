// tiffin-sdk/react: sign-in, account and organization components for apps
// on a Tiffin box with services.auth on. Styled with CSS variables (see
// styles.ts); no Tailwind or CSS import needed.
export { AcceptInvite, Invite, type AcceptInviteProps, type InviteProps } from "./invite";
export { OrgSwitcher, UserButton, type OrgSwitcherProps, type UserButtonProps } from "./account";
export { SignIn, type SignInProps } from "./sign-in";
export { ResetPassword, SignUp, type SignUpProps } from "./sign-up";
export { TiffinAuthProvider, createTiffinAuth, useTiffinAuth, type AuthConfig, type TiffinAuthClient } from "./client";
export { tiffinStyles } from "./styles";
export type { Theme } from "./ui";
