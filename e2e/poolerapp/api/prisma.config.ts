import { defineConfig } from "prisma/config";

export default defineConfig({
  schema: "prisma/schema.prisma",
  datasource: { url: process.env.DIRECT_DATABASE_URL ?? "postgresql://build@localhost/build" },
});
