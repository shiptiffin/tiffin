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
    /** Sign-up and sign-in calls need a solved bot check (see prepareCaptcha). */
    captcha: boolean;
    social: {
        google: {
            configured: boolean;
        } | null;
        github: {
            configured: boolean;
        } | null;
    };
}
export declare const baseOf: (o: AuthOptions) => string;
/** The app's auth config, fetched once per origin. */
export declare function authConfig(o?: AuthOptions): Promise<AuthConfig>;
/** Turns a Better Auth client error into words for people. */
export declare function errorText(e: unknown, fallback?: string): string;
