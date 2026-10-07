// Loaded on demand: Vite splits this file into its own hashed chunk, so the
// first page load doesn't pay for it.
export default function Chunk() {
  const file = new URL(import.meta.url).pathname;
  return (
    <p className="chunk">
      This paragraph arrived just now as its own file, <code>{file}</code>. The hash in its name changes with its contents, so the box can cache it
      for a year.
    </p>
  );
}
