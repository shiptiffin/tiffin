export interface AuthOptions {
    /** Origin of the app; default this page's. */
    baseURL?: string;
    fetch?: typeof fetch;
}
/** What the engine says this app supports (GET /api/auth/tiffin/config). */
export interface AuthConfig {
    appName: string;
    /** "password", "magic-link", "email-otp", "passkey"... */
    methods: string[];
    organizations: boolean;
    /**
     * Email + password sign-up, magic links, one-time codes and resets work
     * here. False in production until the box can send email (they answer
     * EMAIL_NOT_SET_UP): show passkeys and sign-in providers only.
     */
    emailReady: boolean;
    /** Sign-up and sign-in calls need a solved bot check (see prepareCaptcha). */
    captcha: boolean;
    /**
     * For every sign-in service (google, github, apple, microsoft, discord,
     * facebook, twitter, linkedin, gitlab, slack, twitch, oidc): null when it
     * isn't turned on, else whether it has keys yet.
     */
    social: Record<SocialProvider, {
        configured: boolean;
    } | null>;
    /**
     * The sign-in buttons to show, in order: each service turned on, with its
     * name ("Google", "X", or the OpenID Connect provider's own name, like
     * "Okta"). Sign in with `signIn.social({ provider: id })` from Better
     * Auth's client. A button that isn't configured yet answers
     * SOCIAL_NOT_CONFIGURED with how to set it up: hide it from visitors.
     */
    providers: Array<{
        id: SocialProvider;
        name: string;
        configured: boolean;
    }>;
}
/** Better Auth's IDs of the sign-in services the box supports ("oidc": any OpenID Connect provider). */
export type SocialProvider = "google" | "github" | "apple" | "microsoft" | "discord" | "facebook" | "twitter" | "linkedin" | "gitlab" | "slack" | "twitch" | "oidc";
export declare const baseOf: (o: AuthOptions) => string;
/** The app's auth config, fetched once per origin. */
export declare function authConfig(o?: AuthOptions): Promise<AuthConfig>;
/** Turns a Better Auth client error into words for people. */
export declare function errorText(e: unknown, fallback?: string): string;
