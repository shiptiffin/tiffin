import { building } from "$app/env";
import { defineEnvVars } from "@sveltejs/kit/env";

// Tiffin sets DATABASE_URL when the project has Postgres. Prerendering at
// build time doesn't touch the database, so it's only required at run time.
export const variables = defineEnvVars({
  DATABASE_URL: {
    description: "Postgres connection string (set by Tiffin)",
    schema: (value) => {
      if (!value && !building) throw new Error("DATABASE_URL is not set: add Postgres to the project");
      return value ?? "";
    },
  },
});
