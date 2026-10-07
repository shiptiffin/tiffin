import { lazy, Suspense, useState } from "react";

// Split out of the main bundle; fetched the first time it's shown.
const Chunk = lazy(() => import("./Chunk.tsx"));

// A single-page app: Vite builds it to dist/, and the box's edge serves the
// files over HTTPS. React draws everything in the browser; no server runs.
export default function App() {
  const [count, setCount] = useState(0);

  return (
    <main>
      <p className="eyebrow">Vite + React</p>
      <h1>It's live.</h1>
      <p className="lede">
        Vite built this page into plain files with hashed names, and the box serves them. Everything you see is drawn by React in your browser.
      </p>

      <button type="button" className="counter" onClick={() => setCount((n) => n + 1)}>
        {count === 0 ? "Load a chunk" : `Clicked ${count} ${count === 1 ? "time" : "times"}`}
      </button>
      {count > 0 && (
        <Suspense fallback={<p className="chunk">Loading…</p>}>
          <Chunk />
        </Suspense>
      )}

      <ol className="steps">
        <li>
          <h2>Make it yours</h2>
          <p>
            Run <code>bun install</code> and <code>bun run dev</code>, then edit <code>src/App.tsx</code>. The page updates as you save.
          </p>
        </li>
        <li>
          <h2>Ship it</h2>
          <p>
            Run <code>tiffin deploy</code> in this folder. The box runs <code>bun run build</code> and serves <code>dist/</code>. Every deploy is kept, so a
            rollback takes a second.
          </p>
        </li>
        <li>
          <h2>Add pages</h2>
          <p>
            Add a client router such as TanStack Router or React Router, and load each page with <code>lazy()</code>. The box notices the router and
            serves <code>index.html</code> for every path.
          </p>
        </li>
      </ol>
    </main>
  );
}
