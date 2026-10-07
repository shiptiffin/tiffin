import { Link } from "react-router";
import type { Route } from "./+types/about";

export const meta: Route.MetaFunction = () => [{ title: "About · React Router on Tiffin" }];

// Prerendered (react-router.config.ts): written to HTML at build time.
export default function About() {
  return (
    <>
      <p className="eyebrow">React Router</p>
      <h1>About</h1>
      <p className="lede">This page is prerendered: React Router wrote it to HTML at build time.</p>
      <ul className="facts">
        <li>
          <strong>Server.</strong> On Bun, Tiffin serves the build with <code>Bun.serve</code> and React Router's own request handler.
        </li>
        <li>
          <strong>Data.</strong> Bun's built-in Postgres client (<code>Bun.SQL</code>) reads <code>DATABASE_URL</code>.
        </li>
        <li>
          <strong>Forms.</strong> The route action takes a plain POST, so the form works without JavaScript.
        </li>
        <li>
          <strong>Assets.</strong> Files under <code>/assets/</code> are named by content hash; the box serves them, cached for a year.
        </li>
      </ul>
      <p>
        <Link to="/">Back to the notes</Link>
      </p>
    </>
  );
}
