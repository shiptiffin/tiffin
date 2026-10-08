/** What happens after a request. Shared by the form, the home page and the confirmation pages. */
export function InviteSteps({ compact = false }: { compact?: boolean }) {
  return (
    <ol className={compact ? "ea-next ea-next-compact" : "ea-next"}>
      <li>
        <h3>Confirm your email</h3>
        <p>One click on the link we send. Nothing else until your invite.</p>
      </li>
      <li>
        <h3>Invites go out weekly</h3>
        <p>We let people in in small groups, so every box gets attention. We write the week yours is ready.</p>
      </li>
      <li>
        <h3>You get the founding price</h3>
        <p>25% off your first year, and your price locked for 24 months. Starter is $22 a month instead of $29.</p>
      </li>
    </ol>
  );
}

/** A calm one-message page: check your inbox, confirmed, removed. */
export function Notice({ title, kicker, children }: { title: string; kicker?: string; children: React.ReactNode }) {
  return (
    <section className="lost notice">
      <div className="wrap">
        {kicker && <p className="kicker">{kicker}</p>}
        <h1 className="h2">{title}</h1>
        <div className="notice-body">{children}</div>
      </div>
    </section>
  );
}
