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
