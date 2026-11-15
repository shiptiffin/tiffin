/** Set on globalThis from astro:build:start to astro:build:done: prerendering runs in the build process. */
export const BUILDING = Symbol.for("@shiptiffin/astro.building");
