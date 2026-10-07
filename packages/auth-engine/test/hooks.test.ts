// Database hooks that carry state between before and after keep it per project.
import { expect, test } from "bun:test";
import pg from "pg";
import { buildOptions } from "../src/auth";
import { outbox } from "../src/mail";
import { projectConfig } from "./helpers";

type UserHooks = {
  before: (data: Record<string, unknown>, ctx: unknown) => Promise<unknown>;
  after: (user: { email: string }) => Promise<unknown>;
};

test("email-change notices go to each project's own old address, even moving to the same new one at once", async () => {
  const pool = new pg.Pool({ connectionString: "postgres://unused/x" });
  const hooks = (project: string) =>
    (buildOptions(project, projectConfig("postgres://unused/x", { appName: project }), pool).databaseHooks!.user!.update as unknown as UserHooks);
  const a = hooks("alpha");
  const b = hooks("beta");
  const as = (email: string) => ({ context: { session: { user: { email } } } });
  const shared = "shared@example.com";
  await a.before({ email: shared }, as("old-a@example.com"));
  await b.before({ email: shared }, as("old-b@example.com"));
  const from = outbox.length;
  await a.after({ email: shared });
  await b.after({ email: shared });
  const sent = outbox.slice(from).map((m) => `${m.project}:${m.to}`);
  expect(sent).toEqual(["alpha:old-a@example.com", "beta:old-b@example.com"]);
  await pool.end();
});
