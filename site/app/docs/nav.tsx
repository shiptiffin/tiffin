import Link from "next/link";
import { DOC_PAGES } from "@/lib/docs";

const SECTIONS = ["Start here", "Services", "More"] as const;

function Lists({ current }: { current?: string }) {
  return (
    <>
      {SECTIONS.map((s) => (
        <div key={s} className="docs-nav-group">
          <p className="docs-nav-title">{s}</p>
          <ul>
            {DOC_PAGES.filter((p) => p.section === s).map((p) => (
              <li key={p.name}>
                <Link href={`/docs/${p.name}`} aria-current={p.name === current ? "page" : undefined}>
                  {p.title}
                </Link>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </>
  );
}

/** The guides, in reading order: a sidebar on wide screens, a menu above the page on narrow ones. */
export function DocsNav({ current }: { current?: string }) {
  const here = DOC_PAGES.find((p) => p.name === current);
  return (
    <>
      <nav className="docs-nav" aria-label="Docs">
        <Link href="/docs" className="docs-nav-home" aria-current={current ? undefined : "page"}>
          Docs
        </Link>
        <Lists current={current} />
      </nav>
      <details className="docs-menu">
        <summary>
          <span>Docs</span>
          {here ? <span className="docs-menu-here">{here.title}</span> : null}
        </summary>
        <nav aria-label="Docs">
          <Lists current={current} />
        </nav>
      </details>
    </>
  );
}
