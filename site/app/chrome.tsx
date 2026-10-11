import Link from "next/link";

export const DASHBOARD = "https://dashboard.shiptiffin.com";
export const EMAIL = "hello@shiptiffin.com";

/**
 * The mark: a stacked steel tin with a carry handle and a face on the middle
 * tier, on a 32-unit grid. Colours come from --mark-* tokens, so it follows
 * the theme. Same drawing as the dashboard's.
 */
export function Mark({ className = "mark" }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" className={className} aria-hidden="true">
      <g fill="none" stroke="var(--mark-line)" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
        <path d="M11.5 9V6.5a3 3 0 0 1 3-3h3a3 3 0 0 1 3 3V9" stroke="var(--mark-handle)" />
        <circle cx="4.6" cy="20.5" r="1.9" fill="var(--mark-hand)" />
        <circle cx="27.4" cy="20.5" r="1.9" fill="var(--mark-hand)" />
        <path d="M7 12q0-3.2 9-3.2t9 3.2v13q0 3.5-9 3.5t-9-3.5z" fill="var(--mark-body)" />
        <path d="M7 12.6q9 2.6 18 0M7 17.4q9 2.6 18 0M7 24.2q9 2.6 18 0" />
        <circle cx="12.6" cy="21" r="1.15" fill="var(--mark-line)" stroke="none" />
        <circle cx="19.4" cy="21" r="1.15" fill="var(--mark-line)" stroke="none" />
        <path d="M14.9 22q1.1 1 2.2 0" strokeWidth="1.3" />
      </g>
    </svg>
  );
}

export function Header() {
  return (
    <header className="site-header">
      <div className="wrap header-row">
        <Link href="/" className="wordmark" aria-label="ShipTiffin, home">
          <Mark />
          <span>ShipTiffin</span>
        </Link>
        <nav className="header-nav" aria-label="Main">
          <Link href="/pricing" className="nav-link hide-sm">
            Pricing
          </Link>
          <Link href="/docs" className="nav-link hide-sm">
            Docs
          </Link>
          <Link href="/compare" className="nav-link hide-md">
            Compare
          </Link>
          <Link href="/agents" className="nav-link hide-md">
            Agents
          </Link>
          <a href="/account" className="nav-link">
            Sign in
          </a>
          <Link href="/start" className="btn btn-primary btn-sm">
            Get started
          </Link>
        </nav>
      </div>
    </header>
  );
}

const FOOTER: [string, [string, string][]][] = [
  [
    "Product",
    [
      ["Pricing", "/pricing"],
      ["Compare", "/compare"],
      ["For coding agents", "/agents"],
      ["Security", "/security"],
      ["Open source", "/open-source"],
    ],
  ],
  [
    "Learn",
    [
      ["Docs", "/docs"],
      ["Quickstart", "/docs/quickstart"],
      ["Guides", "/guides"],
      ["What works today", "/docs/limits"],
      ["llms.txt", "/llms.txt"],
    ],
  ],
  [
    "Company",
    [
      ["Terms of service", "/terms"],
      ["Report abuse", "/abuse"],
      ["GitHub", "https://github.com/shiptiffin/tiffin"],
      ["Sign in", "/account"],
    ],
  ],
];

export function Footer() {
  return (
    <footer className="site-footer">
      <div className="wrap footer-grid">
        <div className="footer-brand">
          <Link href="/" className="wordmark" aria-label="ShipTiffin, home">
            <Mark />
            <span>ShipTiffin</span>
          </Link>
          <p className="footer-about">
            All your apps on one server in your own Hetzner account, with everything they need already on it.
          </p>
          <p className="footer-legal">
            © 2026 ShipTiffin · <a href={`mailto:${EMAIL}`}>{EMAIL}</a> ·{" "}
            <Link href="/privacy" className="footer-privacy">
              Privacy policy
            </Link>
          </p>
        </div>
        {FOOTER.map(([title, links]) => (
          <nav key={title} className="footer-col" aria-label={title}>
            <p className="footer-title">{title}</p>
            <ul>
              {links.map(([label, href]) => (
                <li key={href}>
                  {href.startsWith("/") && !href.endsWith(".txt") && href !== "/account" ? (
                    <Link href={href}>{label}</Link>
                  ) : (
                    <a href={href}>{label}</a>
                  )}
                </li>
              ))}
            </ul>
          </nav>
        ))}
      </div>
    </footer>
  );
}
