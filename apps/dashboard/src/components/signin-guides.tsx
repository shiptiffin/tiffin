import type { ReactNode } from "react";

// Setup guides for each sign-in provider: where to click in its console,
// what to paste, what to copy back, and what trips people up. Checked
// against the providers' own docs and Better Auth 1.7's in October 2026.
// "tested" turns true once someone has signed in through the box with it.

export type ProviderField = {
  key: "clientId" | "clientSecret" | "tenantId" | "issuer" | "label" | "teamId" | "keyId" | "privateKey" | "consentName";
  label: string;
  hint?: string;
  placeholder?: string;
  optional?: boolean;
  secret?: boolean;
  /** Apple's .p8: chosen as a file (or pasted). */
  file?: boolean;
};

export type Guide = {
  id: string;
  name: string;
  /** Someone has signed in through the box with it. */
  tested: boolean;
  console: { label: string; href: string };
  docs: string;
  /** Short numbered steps. `cb` is a marker for the callback URL, shown above the steps. */
  steps: ReactNode[];
  /** What Better Auth asks for, so the provider returns the email. */
  scopes: string;
  pitfalls: ReactNode[];
  fields: ProviderField[];
};

const id = (label = "Client ID", hint?: string): ProviderField => ({ key: "clientId", label, hint });
const secret = (label = "Client secret", hint?: string): ProviderField => ({ key: "clientSecret", label, secret: true, hint });

const B = ({ children }: { children: ReactNode }) => <b className="font-[550] text-ink">{children}</b>;
const CB = () => <B>the callback URL above</B>;

export const GUIDES: Guide[] = [
  {
    id: "google",
    name: "Google",
    tested: false,
    console: { label: "Google Auth Platform", href: "https://console.cloud.google.com/auth/clients" },
    docs: "https://developers.google.com/identity/protocols/oauth2/web-server",
    steps: [
      <>Open the Google Auth Platform and pick or create a project.</>,
      <>
        In <B>Branding</B>, fill in the app name and support email. Add your domain under <B>Authorized domains</B>.
      </>,
      <>
        In <B>Audience</B>, choose <B>External</B>.
      </>,
      <>
        In <B>Clients</B>, choose <B>Create client</B>, type <B>Web application</B>. Paste <CB /> under <B>Authorized redirect URIs</B>.
      </>,
      <>
        Copy the <B>Client ID</B> and <B>Client secret</B> at once: Google shows the secret only when it is made.
      </>,
      <>
        Back in <B>Audience</B>, choose <B>Publish app</B>.
      </>,
    ],
    scopes: "openid, email, profile (no review needed)",
    pitfalls: [
      <>While the app is in Testing, only the test users you list can sign in, and their access expires after 7 days. Publish it for everyone.</>,
      <>On a box without its own domain (an sslip.io address), Google may refuse the authorized domain. Give the box a domain first.</>,
      <>Google deletes clients that go unused for 6 months.</>,
    ],
    fields: [id(), secret()],
  },
  {
    id: "github",
    name: "GitHub",
    tested: false,
    console: { label: "GitHub OAuth Apps", href: "https://github.com/settings/applications/new" },
    docs: "https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app",
    steps: [
      <>
        Open <B>Settings → Developer settings → OAuth Apps</B> and choose <B>New OAuth App</B>. For an organization, use the organization’s settings.
      </>,
      <>Fill in the application name and homepage URL.</>,
      <>
        Paste <CB /> into <B>Authorization callback URL</B> and register the app.
      </>,
      <>
        Copy the <B>Client ID</B>, then <B>Generate a new client secret</B> and copy it.
      </>,
    ],
    scopes: "read:user, user:email",
    pitfalls: [
      <>Make an OAuth App, not a GitHub App. A GitHub App needs the Email addresses permission or sign-in fails with no email.</>,
    ],
    fields: [id(), secret()],
  },
  {
    id: "apple",
    name: "Apple",
    tested: false,
    console: { label: "Apple Developer: Identifiers", href: "https://developer.apple.com/account/resources/identifiers/list/serviceId" },
    docs: "https://developer.apple.com/help/account/capabilities/configure-sign-in-with-apple-for-the-web/",
    steps: [
      <>
        You need a paid Apple Developer account. In <B>Identifiers</B>, make an <B>App ID</B> with <B>Sign in with Apple</B> turned on.
      </>,
      <>
        Make a <B>Services ID</B> (for example com.example.signin). Its identifier is what you paste as the Services ID.
      </>,
      <>
        Open the Services ID, tick <B>Sign in with Apple</B>, choose <B>Configure</B>. Pick the App ID, add the dashboard’s host under <B>Domains and Subdomains</B> and <CB /> under <B>Return URLs</B>. Save.
      </>,
      <>
        In <B>Keys</B>, make a key with <B>Sign in with Apple</B> on, for the same App ID. Download the <B>.p8</B> file: Apple lets you download it once.
      </>,
      <>
        Copy the <B>Key ID</B>, and your <B>Team ID</B> from the top right of the account page.
      </>,
    ],
    scopes: "email, name",
    pitfalls: [
      <>Apple’s client secret is a token signed with the .p8 key, valid for 6 months at most. The box signs it and makes a new one a month before it runs out.</>,
      <>Apple sends the email address only the first time someone signs in.</>,
      <>
        People can hide their address behind privaterelay.appleid.com. To email them, register your sending domain under <B>Sign in with Apple for Email Communication</B>.
      </>,
      <>Return URLs must be https on a real domain: no localhost or IP addresses.</>,
    ],
    fields: [
      { key: "clientId", label: "Services ID", placeholder: "com.example.signin" },
      { key: "teamId", label: "Team ID", placeholder: "A1B2C3D4E5" },
      { key: "keyId", label: "Key ID", placeholder: "F6G7H8I9J0" },
      { key: "privateKey", label: "Private key (.p8)", secret: true, file: true },
    ],
  },
  {
    id: "microsoft",
    name: "Microsoft",
    tested: false,
    console: { label: "Microsoft Entra: App registrations", href: "https://entra.microsoft.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade" },
    docs: "https://learn.microsoft.com/en-us/entra/identity-platform/quickstart-register-app",
    steps: [
      <>
        Open <B>App registrations</B> and choose <B>New registration</B>.
      </>,
      <>
        Under <B>Supported account types</B>, choose <B>Any Entra ID Tenant + Personal Microsoft accounts</B>, so both work and personal accounts can sign in.
      </>,
      <>
        Under <B>Redirect URI</B>, pick <B>Web</B> and paste <CB />. Register.
      </>,
      <>
        Copy the <B>Application (client) ID</B> from Overview.
      </>,
      <>
        In <B>Certificates &amp; secrets</B>, add a <B>New client secret</B> and copy its <B>Value</B> (not the Secret ID). It is shown once.
      </>,
    ],
    scopes: "openid, profile, email, User.Read, offline_access",
    pitfalls: [
      <>Leave the tenant as common for personal accounts. A single company can put its Directory (tenant) ID here instead.</>,
      <>Client secrets expire (24 months at most). Put a reminder in your calendar and replace the secret here before then.</>,
      <>Work accounts may not share an email unless you add the optional email claim under Token configuration.</>,
    ],
    fields: [
      id("Application (client) ID"),
      secret("Client secret value"),
      { key: "tenantId", label: "Tenant", optional: true, placeholder: "common", hint: "common (default), organizations, consumers, or a Directory (tenant) ID." },
    ],
  },
  {
    id: "discord",
    name: "Discord",
    tested: false,
    console: { label: "Discord Developer Portal", href: "https://discord.com/developers/applications" },
    docs: "https://discord.com/developers/docs/topics/oauth2",
    steps: [
      <>
        Choose <B>New Application</B> and name it.
      </>,
      <>
        Open <B>OAuth2</B>. Under <B>Redirects</B>, add <CB /> and save.
      </>,
      <>
        Copy the <B>Client ID</B>. Choose <B>Reset Secret</B> and copy the <B>Client Secret</B>.
      </>,
    ],
    scopes: "identify, email",
    pitfalls: [<>Accounts made with only a phone number have no email and can’t sign in.</>],
    fields: [id(), secret()],
  },
  {
    id: "facebook",
    name: "Facebook",
    tested: false,
    console: { label: "Meta for Developers", href: "https://developers.facebook.com/apps" },
    docs: "https://developers.facebook.com/docs/facebook-login/web",
    steps: [
      <>
        Choose <B>Create App</B> and the use case <B>Authenticate and request data from users with Facebook Login</B>.
      </>,
      <>
        In the Facebook Login use case, open <B>Customize</B> and make sure the <B>email</B> permission is added.
      </>,
      <>
        In <B>Settings</B>, paste <CB /> into <B>Valid OAuth Redirect URIs</B>.
      </>,
      <>
        In <B>App settings → Basic</B>, copy the <B>App ID</B> and <B>App Secret</B>. Add a privacy policy URL and a data deletion URL.
      </>,
      <>
        Switch the app to <B>Live</B> (Publish).
      </>,
    ],
    scopes: "email, public_profile (no review needed)",
    pitfalls: [
      <>Until the app is Live, only people with a role on the app can sign in.</>,
      <>Without the email permission, sign-in fails: the box needs an email for every account.</>,
    ],
    fields: [id("App ID"), secret("App Secret")],
  },
  {
    id: "twitter",
    name: "X (Twitter)",
    tested: false,
    console: { label: "X Developer Console", href: "https://console.x.com" },
    docs: "https://docs.x.com/fundamentals/authentication/oauth-2-0/authorization-code",
    steps: [
      <>Create an app in the X Developer Console.</>,
      <>
        Open <B>User authentication settings</B>. Permissions: <B>Read</B>. Type of app: <B>Web App, Automated App or Bot</B>.
      </>,
      <>
        Turn on <B>Request email from users</B>, and add your terms and privacy policy URLs (X asks for them).
      </>,
      <>
        Paste <CB /> into <B>Callback URI / Redirect URL</B> and save.
      </>,
      <>
        Under <B>Keys and tokens</B>, copy the <B>OAuth 2.0 Client ID</B> and <B>Client Secret</B> (not the API key).
      </>,
    ],
    scopes: "users.read, tweet.read, users.email, offline.access",
    pitfalls: [
      <>The X API is pay-per-use for new developers: each sign-in uses a few API reads. Add credits to the developer account.</>,
      <>Without Request email, people get a placeholder address instead of their own.</>,
    ],
    fields: [id("OAuth 2.0 Client ID"), secret()],
  },
  {
    id: "linkedin",
    name: "LinkedIn",
    tested: false,
    console: { label: "LinkedIn Developers", href: "https://www.linkedin.com/developers/apps/new" },
    docs: "https://learn.microsoft.com/en-us/linkedin/consumer/integrations/self-serve/sign-in-with-linkedin-v2",
    steps: [
      <>Create an app. LinkedIn asks for a company page to link it to.</>,
      <>
        On the <B>Products</B> tab, add <B>Sign In with LinkedIn using OpenID Connect</B>.
      </>,
      <>
        On the <B>Auth</B> tab, add <CB /> under <B>Authorized redirect URLs for your app</B>.
      </>,
      <>
        Copy the <B>Client ID</B> and <B>Primary Client Secret</B> from the Auth tab.
      </>,
    ],
    scopes: "openid, profile, email",
    pitfalls: [<>Without the OpenID Connect product, sign-in fails with an unauthorized scope error.</>],
    fields: [id(), secret("Primary Client Secret")],
  },
  {
    id: "gitlab",
    name: "GitLab",
    tested: false,
    console: { label: "GitLab: Applications", href: "https://gitlab.com/-/user_settings/applications" },
    docs: "https://docs.gitlab.com/integration/oauth_provider/",
    steps: [
      <>
        Open <B>Edit profile → Applications</B> (or a group’s <B>Settings → Applications</B>) and add a new application.
      </>,
      <>
        Paste <CB /> into <B>Redirect URI</B>. Keep <B>Confidential</B> ticked.
      </>,
      <>
        Tick the <B>read_user</B> scope and save.
      </>,
      <>
        Copy the <B>Application ID</B> and the <B>Secret</B>.
      </>,
    ],
    scopes: "read_user",
    pitfalls: [<>For a self-managed GitLab, put its address under GitLab URL, and make the application there.</>],
    fields: [
      id("Application ID"),
      secret("Secret"),
      { key: "issuer", label: "GitLab URL", optional: true, placeholder: "https://gitlab.com", hint: "Only for a self-managed GitLab." },
    ],
  },
  {
    id: "slack",
    name: "Slack",
    tested: false,
    console: { label: "Slack API: Your Apps", href: "https://api.slack.com/apps?new_app=1" },
    docs: "https://docs.slack.dev/authentication/sign-in-with-slack/",
    steps: [
      <>
        Choose <B>Create New App → From scratch</B> and pick a workspace to develop in.
      </>,
      <>
        In <B>OAuth &amp; Permissions</B>, add <CB /> under <B>Redirect URLs</B> and save.
      </>,
      <>
        In <B>Basic Information → App Credentials</B>, copy the <B>Client ID</B> and <B>Client Secret</B>.
      </>,
      <>
        To let people from other workspaces sign in, turn on <B>Manage Distribution → Public Distribution</B>.
      </>,
    ],
    scopes: "openid, profile, email",
    pitfalls: [<>Without public distribution, only members of your development workspace can sign in.</>],
    fields: [id(), secret()],
  },
  {
    id: "twitch",
    name: "Twitch",
    tested: false,
    console: { label: "Twitch Developer Console", href: "https://dev.twitch.tv/console/apps" },
    docs: "https://dev.twitch.tv/docs/authentication/register-app/",
    steps: [
      <>Turn on two-factor authentication for your Twitch account: the console requires it.</>,
      <>
        Choose <B>Register Your Application</B>. Add <CB /> under <B>OAuth Redirect URLs</B>, pick a category, and make it a <B>Confidential</B> client.
      </>,
      <>
        Open <B>Manage</B>, copy the <B>Client ID</B>, then choose <B>New Secret</B> and copy it.
      </>,
    ],
    scopes: "openid, user:read:email",
    pitfalls: [<>The app’s name must be unique across Twitch.</>],
    fields: [id(), secret()],
  },
  {
    id: "oidc",
    name: "OpenID Connect",
    tested: false,
    console: { label: "Your identity provider", href: "https://openid.net/developers/certified-openid-connect-implementations/" },
    docs: "https://openid.net/developers/how-connect-works/",
    steps: [
      <>
        For Okta, Auth0, Keycloak, Microsoft Entra or your company’s sign-in. In Okta: <B>Applications → Create App Integration → OIDC → Web Application</B>. In Auth0:{" "}
        <B>Applications → Create Application → Regular Web Application</B>. In Keycloak: <B>Clients → Create client</B> with client authentication on.
      </>,
      <>
        Paste <CB /> as the sign-in redirect URI (Okta: Sign-in redirect URIs; Auth0: Allowed Callback URLs; Keycloak: Valid redirect URIs).
      </>,
      <>
        Copy the <B>Client ID</B> and <B>Client secret</B>.
      </>,
      <>
        Find the <B>issuer URL</B>: the address before /.well-known/openid-configuration. Okta: https://your-org.okta.com (or …/oauth2/default). Auth0: https://your-tenant.auth0.com. Keycloak:
        https://host/realms/your-realm.
      </>,
    ],
    scopes: "openid, email, profile",
    pitfalls: [
      <>The box reads the provider’s settings from the issuer URL. If sign-in says the provider isn’t set up, check that the issuer URL opens in a browser with /.well-known/openid-configuration added.</>,
      <>Give the button a name people know, like Okta or Acme SSO.</>,
    ],
    fields: [
      { key: "label", label: "Button name", optional: true, placeholder: "Okta", hint: "What the sign-in button says." },
      { key: "issuer", label: "Issuer URL", placeholder: "https://your-org.okta.com" },
      id(),
      secret(),
    ],
  },
];

export const guideFor = (id: string) => GUIDES.find((g) => g.id === id);

/** The secrets a project sets to use its own keys for a provider. */
export function projectSecretNames(id: string, env: string): string {
  if (id === "apple") return `${env}_CLIENT_ID, ${env}_TEAM_ID, ${env}_KEY_ID and ${env}_PRIVATE_KEY`;
  if (id === "oidc") return `${env}_ISSUER, ${env}_CLIENT_ID and ${env}_CLIENT_SECRET`;
  return `${env}_CLIENT_ID and ${env}_CLIENT_SECRET`;
}
