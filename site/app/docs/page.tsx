import type { Metadata } from "next";
import { pageMeta } from "@/lib/meta";
import Link from "next/link";
import { DOC_PAGES } from "@/lib/docs";
import { DocsNav } from "./nav";

export const dynamic = "force-static";

export const metadata: Metadata = pageMeta({
  title: "Docs",
  description:
    "How ShipTiffin works: getting a box, deploying apps, Postgres, KV, files, sign-in, email, jobs, domains, monitoring, and working with coding agents.",
  path: "/docs",
});

const SECTIONS = ["Start here", "Services", "More"] as const;

export default function DocsHome() {
  return (
    <div className="wrap docs docs-home">
      <DocsNav />
      <div className="doc">
        <h1 className="doc-title">Docs</h1>
        <p className="doc-lede">
          Everything about running your apps on a ShipTiffin box. New here? Start with the{" "}
          <Link href="/docs/quickstart">quickstart</Link>, or hand your coding agent the{" "}
          <Link href="/docs/agent-onboarding">setup prompt</Link>. Agents can read it all at once from{" "}
          <a href="/llms-full.txt">llms-full.txt</a>.
        </p>
        {SECTIONS.map((s) => (
          <section key={s} className="docs-index" aria-labelledby={`s-${s}`}>
            <h2 id={`s-${s}`}>{s}</h2>
            <ul>
              {DOC_PAGES.filter((p) => p.section === s).map((p) => (
                <li key={p.name}>
                  <Link href={`/docs/${p.name}`}>{p.title}</Link>
                  <span>{p.note.charAt(0).toUpperCase() + p.note.slice(1)}.</span>
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
    </div>
  );
}
