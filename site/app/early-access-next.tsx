/** What happens after someone joins the list. Shared by the home page and /early-access. */
export function EarlyAccessNext() {
  return (
    <ol className="ea-next">
      <li>
        <h3>Confirm your email.</h3>
        <p>We send one link. Nothing else until your invite.</p>
      </li>
      <li>
        <h3>We invite people in small waves.</h3>
        <p>Each invite comes with a box, set up and ready for your first project.</p>
      </li>
      <li>
        <h3>You get the founding price.</h3>
        <p>25% off your first year, and your price locked for 24 months. You&rsquo;ll see it before you pay anything.</p>
      </li>
    </ol>
  );
}

/** A calm one-message page: confirm, removed, check your inbox. */
export function Notice({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="lost notice">
      <div className="wrap">
        <h1 className="h2">{title}</h1>
        <div className="notice-body">{children}</div>
      </div>
    </section>
  );
}
