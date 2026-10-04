// A sign-in link's one-time code arrives in the hash (/login#tfl_…). It is
// read here, the moment the app starts (before the router and the lazily
// loaded login page), and removed from the address bar and history at once.
let code = "";
if (location.pathname === "/login" && location.hash.length > 1) {
  code = decodeURIComponent(location.hash.replace(/^#/, "")).trim();
  history.replaceState(null, "", location.pathname + location.search);
}

/** The code the page was opened with (once; later calls get ""). Falls back to the current hash for a link pasted into an open tab. */
export function takeLoginCode(): string {
  const c = code || decodeURIComponent(location.hash.replace(/^#/, "")).trim();
  code = "";
  if (location.hash) history.replaceState(null, "", location.pathname + location.search);
  return c;
}
