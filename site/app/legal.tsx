export const UPDATED = "7 October 2026";
export const UPDATED_ISO = "2026-10-07";

export type Section = { id: string; title: string; body: React.ReactNode };

export function LegalPage({ title, intro, sections }: { title: string; intro: React.ReactNode; sections: Section[] }) {
  return (
    <article className="doc">
      <div className="wrap doc-grid">
        <header className="doc-head">
          <h1 className="doc-title">{title}</h1>
          <p className="doc-updated">
            Last updated: <time dateTime={UPDATED_ISO}>{UPDATED}</time>
          </p>
          <p className="doc-intro">{intro}</p>
        </header>
        <nav className="toc" aria-label="On this page">
          <p className="toc-title">On this page</p>
          <ol>
            {sections.map((s) => (
              <li key={s.id}>
                <a href={`#${s.id}`}>{s.title}</a>
              </li>
            ))}
          </ol>
        </nav>
        <div className="prose">
          {sections.map((s) => (
            <section key={s.id} id={s.id} aria-labelledby={`${s.id}-h`}>
              <h2 id={`${s.id}-h`}>{s.title}</h2>
              {s.body}
            </section>
          ))}
        </div>
      </div>
    </article>
  );
}
