/**
 * tailwind-merge needs to know our custom theme scales (styles.css @theme):
 * without this it reads `shadow-raised` as a shadow colour and never merges
 * `animate-pop` with `animate-fade`. Shared by the dev-only guard in cn.ts
 * and scripts/check-overrides.mjs. Never imported by production code.
 */
export const twMergeConfig = {
  extend: {
    theme: {
      shadow: ["raised", "overlay"],
      animate: ["rise", "fade", "pop"],
    },
  },
};
