function load(): never {
  throw new Error("config exploded: DATABASE_REGION is required");
}
export default { project: load() };
