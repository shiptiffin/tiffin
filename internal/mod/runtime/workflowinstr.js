// SPDX-License-Identifier: Apache-2.0
// Written into the build by the Tiffin box: runs the Workflow DevKit's
// Postgres world in this server (https://workflow-sdk.dev/worlds/postgres).
/*APP*/ const app = {};
const world = /*WORLD*/ "";

export async function register() {
  if (process.env.NEXT_RUNTIME === "nodejs" && process.env.WORKFLOW_TARGET_WORLD === world) {
    await import(/* webpackIgnore: true */ /* turbopackIgnore: true */ "/app/.tiffin/workflow/setup.mjs");
    const { getWorld } = await import("workflow/runtime");
    await (await getWorld()).start?.();
  }
  await app.register?.();
}

export function onRequestError(...args) {
  return app.onRequestError?.(...args);
}
