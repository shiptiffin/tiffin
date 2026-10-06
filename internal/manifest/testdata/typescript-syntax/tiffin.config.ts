import { defineConfig, type TiffinConfig } from "@shiptiffin/sdk";
import type { Something } from "@shiptiffin/sdk/config";

interface Extra {
  region: string;
}

enum Tier {
  Small = 256,
  Large = 2048,
}

const extra: Extra = { region: "eu" };
const frameworks = ["next", "hono"] as const;

function pick<T>(xs: readonly T[], i: number): T {
  return xs[i]!;
}

const config = {
  project: "typed",
  env: { REGION: extra.region },
  apps: {
    web: { framework: pick(frameworks, 0), memoryMB: Tier.Large },
    api: { framework: pick(frameworks, 1), memoryMB: Tier.Small, routes: ["api"] },
  },
} satisfies TiffinConfig;

export default defineConfig(config);
