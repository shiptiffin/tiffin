import { FatalError, sleep } from "workflow";

// Two steps around a durable sleep; each step records that it ran (and on
// which release) in the project's database.
export async function twoSteps(tag, wait) {
  "use workflow";
  const one = await record(tag, "one");
  await sleep(wait);
  const two = await record(tag, "two");
  return [one, two];
}

export async function failing(tag) {
  "use workflow";
  await fail(tag);
}

async function record(tag, step) {
  "use step";
  await save(tag, step);
  return `${step}@${process.env.TIFFIN_DEPLOY ?? "local"}`;
}

async function fail(tag) {
  "use step";
  await save(tag, "fatal");
  throw new FatalError("e2e: this step fails for good");
}

async function save(tag, step) {
  const sql = globalThis.Bun.sql;
  await sql`create table if not exists e2e_steps (tag text, step text, release text, at timestamptz default now())`;
  await sql`insert into e2e_steps (tag, step, release) values (${tag}, ${step}, ${process.env.TIFFIN_DEPLOY ?? "local"})`;
}
