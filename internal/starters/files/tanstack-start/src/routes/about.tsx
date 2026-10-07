import { createFileRoute, Link } from "@tanstack/react-router";

// Prerendered at build time (see vite.config.ts): the box serves this page as a
// file, and the router takes over once it loads.
export const Route = createFileRoute("/about")({
  head: () => ({ meta: [{ title: "About · TanStack Start on Tiffin" }] }),
  component: About,
});

function About() {
  return (
    <main>
      <p className="eyebrow">
        <Link to="/">Notes</Link>
      </p>
      <h1>About</h1>
      <p className="lede">
        This page was rendered once, when the app was built. Edit <code>src/routes/about.tsx</code>, or add a file to <code>src/routes/</code> for a new page.
      </p>
    </main>
  );
}
