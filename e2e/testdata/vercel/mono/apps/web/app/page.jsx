import { greeting } from "@vx/words";

export default function Home() {
  return (
    <main>
      <h1>vx-home</h1>
      <p>{greeting}</p>
      <a href="/about/">About</a>
    </main>
  );
}
