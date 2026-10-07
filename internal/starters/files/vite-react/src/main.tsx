import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import App from "./App.tsx";

// A deploy replaces the files. A tab left open across one reloads to the new
// version instead of failing to fetch a chunk that's gone.
window.addEventListener("vite:preloadError", () => window.location.reload());

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
