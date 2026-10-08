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
          <Link href="/#replaces" className="nav-link hide-md">
            What it replaces
          </Link>
          <Link href="/#pricing" className="nav-link hide-sm">
            Pricing
          </Link>
          <Link href="/#faq" className="nav-link hide-md">
            Questions
          </Link>
          <a href={DASHBOARD} className="nav-link">
            Sign in
          </a>
          <Link href="/#invite" className="btn btn-primary btn-sm">
            Request an invite
          </Link>
        </nav>
      </div>
    </header>
  );
}

export function Footer() {
  return (
    <footer className="site-footer">
      <div className="wrap footer-row">
        <div className="footer-brand">
          <Mark />
          <span>
            © 2026 ShipTiffin · <a href={`mailto:${EMAIL}`}>{EMAIL}</a>
          </span>
        </div>
        <nav className="footer-nav" aria-label="Footer">
          <Link href="/privacy" className="footer-privacy">
            Privacy policy
          </Link>
          <Link href="/terms">Terms of service</Link>
          <a href={DASHBOARD}>Sign in</a>
        </nav>
      </div>
    </footer>
  );
}
