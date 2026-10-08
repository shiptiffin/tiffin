// A managed box's name: its address is <name>.shiptiffin.app. The same rules
// as the cloud worker's (internal/cloud/names.go); keep the lists in step.
// Safe in the browser.

export const NAME_MIN = 3;
export const NAME_MAX = 30;
export const ZONE = "shiptiffin.app";

export const RESERVED = new Set([
  "abuse", "account", "accounts", "admin", "administrator", "api", "app", "apps", "assets", "auth",
  "billing", "blog", "box", "boxes", "cdn", "cloud", "console", "control", "dashboard", "dev", "dns",
  "docs", "download", "email", "files", "ftp", "git", "help", "hetzner", "home", "imap", "info",
  "internal", "invoice", "legal", "login", "logout", "mail", "manage", "monitor", "mx", "news", "ns",
  "ns1", "ns2", "ns3", "ns4", "oauth", "official", "password", "pay", "payment", "payments", "pop",
  "pop3", "portal", "postmaster", "preview", "privacy", "root", "s3", "secure", "security", "server",
  "shiptiffin", "signin", "signup", "smtp", "sso", "staging", "start", "static", "status", "stripe",
  "support", "system", "team", "terms", "test", "tiffin", "update", "updates", "verify", "wallet",
  "webmail", "www",
  "apple", "amazon", "google", "gmail", "microsoft", "outlook", "office", "paypal", "facebook",
  "instagram", "whatsapp", "netflix", "binance", "coinbase", "metamask", "github", "chase", "wellsfargo",
]);

export const RESERVED_PARTS = ["shiptiffin", "paypal", "google", "microsoft", "amazon", "signin", "login", "verify", "wallet"];

/** Why a name can't be used, in words, or null when it can. */
export function nameProblem(name: string): string | null {
  if (name.length < NAME_MIN || name.length > NAME_MAX) return `Use ${NAME_MIN} to ${NAME_MAX} characters.`;
  if (!/^[a-z0-9-]+$/.test(name)) return "Use lowercase letters, digits and dashes.";
  if (!/^[a-z]/.test(name)) return "Start with a letter.";
  if (name.endsWith("-")) return "End with a letter or a digit.";
  if (name.includes("--")) return "No double dashes.";
  if (RESERVED.has(name)) return `“${name}” is reserved.`;
  for (const p of RESERVED_PARTS) if (name.includes(p)) return `Names with “${p}” in them are reserved.`;
  return null;
}

export const boxDomain = (name: string) => `${name}.${ZONE}`;
export const dashboardUrl = (name: string) => `https://dashboard.${name}.${ZONE}`;

/** Only paths on this site, so a link can't send people elsewhere after signing in. */
export function safeNext(next: string | undefined): string {
  return next && /^\/(?!\/)[\w\-/?=&%.]*$/.test(next) ? next : "/account";
}
