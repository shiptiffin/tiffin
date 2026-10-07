import adapter from "@sveltejs/adapter-bun";
import { sveltekit } from "@sveltejs/kit/vite";
import { defineConfig } from "vite";

// SvelteKit 3 keeps its config here. adapter-bun builds a Bun.serve server
// (bun ./build); the box sets the proxy headers it reads, so form actions
// pass SvelteKit's origin check behind the edge.
export default defineConfig({
  plugins: [sveltekit({ adapter: adapter() })],
});
